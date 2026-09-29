package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/controller"
	"github.com/kgskr/fortigate-external-dns/internal/fortigate"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	"github.com/kgskr/fortigate-external-dns/internal/ownership"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	"github.com/kgskr/fortigate-external-dns/internal/policy"
	"github.com/kgskr/fortigate-external-dns/internal/source"
	statuswriter "github.com/kgskr/fortigate-external-dns/internal/status"
	"github.com/kgskr/fortigate-external-dns/internal/target"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
)

type platformClientFactory struct {
	logger  *slog.Logger
	metrics *metrics.Metrics
}

func (f platformClientFactory) NewClient(_ context.Context, definition target.Definition, material *target.CredentialMaterial) (target.ProviderClient, error) {
	if material == nil {
		return nil, fmt.Errorf("credential material is required")
	}
	timeout := definition.Timeout
	if timeout <= 0 {
		timeout = config.DefaultTimeout
	}
	vdom := definition.VDOM
	if vdom == "" {
		vdom = "root"
	}
	providerConfig := config.FortiGateConfig{
		BaseURL: definition.URL, APIToken: string(material.APIToken()), VDOM: vdom, Zone: definition.Zone,
		InsecureSkipVerify: definition.InsecureSkipVerify, CAData: material.CABundle(), Timeout: timeout, Retries: definition.Retries,
		ExclusiveZoneOwnership: definition.OwnershipMode == v1alpha1.OwnershipModeExclusive,
	}
	return fortigate.NewClient(providerConfig, f.logger.With("target", definition.Key()), f.metrics)
}

type platformResourceFactory struct {
	dynamicClient source.KubernetesClients
	retention     int
}

func (f platformResourceFactory) NewResources(_ context.Context, definition target.Definition) (target.StoreHandles, error) {
	client := f.dynamicClient.Dynamic
	planStore, err := plan.NewChangePlanStore(client)
	if err != nil {
		return target.StoreHandles{}, err
	}
	claimStore, err := ownership.NewDynamicStore(client, definition.Namespace)
	if err != nil {
		return target.StoreHandles{}, err
	}
	claimRepository, err := ownership.NewRepository(claimStore)
	if err != nil {
		return target.StoreHandles{}, err
	}
	claimManager, err := ownership.NewManager(claimRepository)
	if err != nil {
		return target.StoreHandles{}, err
	}
	writer, err := statuswriter.NewWriter(client, definition.Namespace, definition.Name, int32(f.retention))
	if err != nil {
		return target.StoreHandles{}, err
	}
	return target.StoreHandles{PlanStore: planStore, OwnershipStore: &sharedOwnershipHandles{manager: claimManager, repository: claimRepository}, StatusStore: writer}, nil
}

// Fixed reason codes for target-loop diagnostics that have no target key.
const (
	reasonTargetListFailed  = "target-list-failed"
	reasonStatusWriteFailed = "status-write-failed"
	reasonTargetSyncFailed  = "target-sync-failed"
)

func runTargetMode(ctx context.Context, cfg config.Config, clients source.KubernetesClients, recorder *metrics.Metrics, logger *slog.Logger, heartbeat *controller.Heartbeat) error {
	manager, err := newTargetRuntimeManager(clients, cfg, recorder, logger)
	if err != nil {
		return err
	}
	defer func() { _, _ = manager.Sync(context.Background(), nil) }()
	if cfg.EventDriven {
		return runEventTargetMode(ctx, cfg, clients, manager, recorder, logger, heartbeat)
	}

	for {
		cycleErr := runTargetCycle(ctx, cfg, clients, manager, recorder, logger)
		// Every cycle is an attempt, including one that failed to load targets:
		// an erroring Kubernetes API or provider is not a reason to restart.
		heartbeat.MarkAttempt()
		if cfg.Once {
			return cycleErr
		}
		if !sleepContext(ctx, cfg.Resync) {
			return ctx.Err()
		}
	}
}

// runTargetCycle performs one polling-mode pass over all targets. Failures are
// logged and returned so --once can exit non-zero; the long-running loop just
// retries on the next tick. Invalid or conflicting targets and transient list
// errors never end the process.
func runTargetCycle(ctx context.Context, cfg config.Config, clients source.KubernetesClients, manager *target.RuntimeManager, recorder *metrics.Metrics, logger *slog.Logger) error {
	start := time.Now()
	load, err := loadTargetDefinitions(ctx, cfg, clients)
	if err != nil {
		logger.Error("target load failed; retrying next cycle", "reason", reasonTargetListFailed, "error", err)
		recorder.RecordReconcile(time.Since(start), err)
		return err
	}
	syncResult, err := syncTargets(ctx, cfg, clients, manager, recorder, logger, load, "")
	if err != nil {
		logger.Error("target sync failed; retrying next cycle", "reason", reasonTargetSyncFailed, "error", err)
		recorder.RecordReconcile(time.Since(start), err)
		return err
	}
	results := manager.RunAll(ctx, func(runCtx context.Context, runtime *target.Runtime) error {
		return runTargetAudit(runCtx, cfg, clients, runtime, recorder, logger)
	})
	failed := false
	for _, reason := range syncResult.Failures {
		if reason != "" {
			failed = true
		}
	}
	for key, result := range results {
		if !result.Succeeded {
			failed = true
			logger.Error("target audit failed", "target", key, "reason", result.Reason)
		}
	}
	if failed {
		return errors.New("one or more targets failed setup or audit")
	}
	return nil
}

// sleepContext waits for d or context cancellation and reports whether the
// full wait elapsed.
func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// syncTargets reconciles the runtime manager with the valid targets and folds
// the excluded (invalid or conflicting) targets into the result's failures,
// with metrics and target status reported like any other setup failure. Status
// is written from the Kubernetes clients alone so it does not depend on a
// successfully built provider client. When only is non-empty just that key's
// status is written (event mode syncs once per key).
func syncTargets(ctx context.Context, cfg config.Config, clients source.KubernetesClients, manager *target.RuntimeManager, recorder *metrics.Metrics, logger *slog.Logger, load targetLoad, only string) (target.SyncResult, error) {
	result, err := manager.Sync(ctx, load.definitions)
	if err != nil {
		return target.SyncResult{}, err
	}
	if result.Failures == nil {
		result.Failures = map[string]target.FailureReason{}
	}
	type identity struct {
		namespace, name string
		generation      int64
	}
	identities := map[string]identity{}
	for _, definition := range load.definitions {
		identities[definition.Key()] = identity{definition.Namespace, definition.Name, definition.Generation}
	}
	for key, invalid := range load.invalid {
		result.Failures[key] = invalid.Reason
		identities[key] = identity{invalid.Namespace, invalid.Name, invalid.Generation}
		recorder.SetTargetReadiness(key, false)
		if only == "" || only == key {
			logger.Error("target excluded", "target", key, "reason", invalid.Reason, "error", invalid.Err)
		}
	}
	for key, reason := range result.Failures {
		if only != "" && only != key {
			continue
		}
		if _, excluded := load.invalid[key]; !excluded {
			attrs := []any{"target", key, "reason", reason}
			if credentialReason := result.CredentialReasons[key]; credentialReason != "" {
				attrs = append(attrs, "credentialReason", credentialReason)
			}
			logger.Error("target setup failed", attrs...)
		}
		id := identities[key]
		writeSetupFailureStatus(ctx, clients, cfg.StatusRetention, logger, id.namespace, id.name, id.generation, reason)
	}
	return result, nil
}

func newTargetRuntimeManager(clients source.KubernetesClients, cfg config.Config, recorder *metrics.Metrics, logger *slog.Logger) (*target.RuntimeManager, error) {
	resolver, err := target.NewResolver(clients.Core.CoreV1())
	if err != nil {
		return nil, err
	}
	manager, err := target.NewRuntimeManager(
		resolver,
		platformClientFactory{logger: logger, metrics: recorder},
		platformResourceFactory{dynamicClient: clients, retention: cfg.StatusRetention},
		recorder,
		nil,
	)
	if err != nil {
		return nil, err
	}
	manager.SetLogger(logger)
	return manager, nil
}

type eventTargetExecutor struct {
	cfg       config.Config
	clients   source.KubernetesClients
	manager   *target.RuntimeManager
	recorder  *metrics.Metrics
	logger    *slog.Logger
	heartbeat *controller.Heartbeat
	scope     platformScope
}

type preparedTargetAudit struct {
	runtime *target.Runtime
	runner  controller.Runner
	audit   controller.ReconcileAudit
	start   time.Time
}

// Audit marks a heartbeat attempt on every return path: a target that cannot be
// audited (invalid, unreachable provider, failing credentials) is a failed
// attempt, not a wedged loop, so it must not push /healthz towards a restart.
func (e eventTargetExecutor) Audit(ctx context.Context, key platformqueue.TargetKey) (controller.TargetAudit, error) {
	defer e.heartbeat.MarkAttempt()
	start := time.Now()
	audit, err := e.audit(ctx, key, start)
	if err != nil && !errors.Is(err, controller.ErrPlatformInformerScopeChanged) && !errors.Is(err, context.Canceled) {
		e.recorder.RecordReconcile(time.Since(start), err)
	}
	return audit, err
}

func (e eventTargetExecutor) audit(ctx context.Context, key platformqueue.TargetKey, start time.Time) (controller.TargetAudit, error) {
	load, err := loadTargetDefinitions(ctx, e.cfg, e.clients)
	if err != nil {
		return controller.TargetAudit{}, err
	}
	if !platformScopesEqual(e.scope, platformEventScope(e.cfg, load.definitions)) {
		return controller.TargetAudit{}, controller.ErrPlatformInformerScopeChanged
	}
	result, err := syncTargets(ctx, e.cfg, e.clients, e.manager, e.recorder, e.logger, load, key.String())
	if err != nil {
		return controller.TargetAudit{}, err
	}
	// Only this key's own validity fails its audit; an invalid sibling never does.
	if reason := result.Failures[key.String()]; reason != "" {
		return controller.TargetAudit{}, target.Fail(reason)
	}
	runtime, ok := e.manager.Runtime(key.String())
	if !ok {
		return controller.TargetAudit{}, fmt.Errorf("target runtime is unavailable")
	}
	runner, err := buildTargetRunner(e.cfg, e.clients, runtime, e.recorder, e.logger)
	if err != nil {
		writeTargetStatus(ctx, runtime, nil, err, false, e.logger)
		return controller.TargetAudit{}, err
	}
	prepared, err := runner.Prepare(ctx)
	if err != nil {
		writeTargetStatus(ctx, runtime, nil, err, false, e.logger)
		return controller.TargetAudit{}, err
	}
	cleanupCapable := false
	for _, operation := range prepared.Operations {
		if operation.Type == plan.OperationDelete || operation.Type == plan.OperationDeactivate {
			cleanupCapable = true
			break
		}
	}
	return controller.TargetAudit{
		CleanupCapable: cleanupCapable, DiscoveryComplete: prepared.DiscoveryComplete,
		ProviderSnapshotStable: prepared.ProviderSnapshotStable,
		State:                  &preparedTargetAudit{runtime: runtime, runner: runner, audit: prepared, start: start},
	}, nil
}

func (e eventTargetExecutor) Apply(ctx context.Context, _ platformqueue.TargetKey, audit controller.TargetAudit) error {
	prepared, ok := audit.State.(*preparedTargetAudit)
	if !ok || prepared == nil || prepared.runtime == nil {
		return fmt.Errorf("target runtime audit state is invalid")
	}
	err := prepared.runner.ApplyPrepared(ctx, prepared.audit)
	writeTargetStatus(ctx, prepared.runtime, &prepared.audit, err, err == nil, e.logger)
	if !errors.Is(err, context.Canceled) {
		e.recorder.RecordReconcile(time.Since(prepared.start), err)
	}
	e.heartbeat.MarkAttempt()
	return err
}

func (e eventTargetExecutor) TargetDeleted(ctx context.Context, _ platformqueue.TargetKey) error {
	load, err := loadTargetDefinitions(ctx, e.cfg, e.clients)
	if err != nil {
		return err
	}
	if !platformScopesEqual(e.scope, platformEventScope(e.cfg, load.definitions)) {
		return controller.ErrPlatformInformerScopeChanged
	}
	// Status is written per key during Audit; here only the runtime set matters.
	_, err = e.manager.Sync(ctx, load.definitions)
	return err
}

func runEventTargetMode(ctx context.Context, cfg config.Config, clients source.KubernetesClients, manager *target.RuntimeManager, recorder *metrics.Metrics, logger *slog.Logger, heartbeat *controller.Heartbeat) error {
	for {
		load, err := loadTargetDefinitions(ctx, cfg, clients)
		if err != nil {
			// A transient LIST failure must not end the process: log, count the
			// attempt, and retry.
			logger.Error("target load failed; retrying", "reason", reasonTargetListFailed, "error", err)
			recorder.RecordReconcile(0, err)
			heartbeat.MarkAttempt()
			if !sleepContext(ctx, min(cfg.Resync, eventLoadRetryMax)) {
				return ctx.Err()
			}
			continue
		}
		definitions := load.definitions
		eventScope := platformEventScope(cfg, definitions)
		executor := eventTargetExecutor{cfg: cfg, clients: clients, manager: manager, recorder: recorder, logger: logger, heartbeat: heartbeat, scope: eventScope}
		runtime, err := controller.NewPlatformRuntime(
			controller.PlatformClients{Kubernetes: clients.Core, Gateway: clients.Gateway, Dynamic: clients.Dynamic},
			controller.PlatformRuntimeConfig{
				Namespace: cfg.PlatformNamespace, Sources: eventScope.sources, SourceNamespaces: eventScope.namespaces,
				GatewayNamespaces: eventScope.gatewayNamespaces, Headless: eventScope.headless, Policy: cfg.PolicyEnforcement && len(definitions) > 0,
				Ownership: eventScope.ownership, PlanApproval: eventScope.planApproval,
				InformerResync: cfg.Resync, PeriodicInterval: cfg.Resync, Workers: 2,
				Queue: platformqueue.Config{Name: "fortigate-targets", Debounce: cfg.Debounce, RetryMax: time.Minute, MaxRetries: 8},
			}, executor,
		)
		if err != nil {
			return err
		}
		runCtx, cancel := context.WithCancel(ctx)
		go func() {
			for {
				select {
				case <-runCtx.Done():
					return
				case asyncErr := <-runtime.Errors():
					logger.Warn("platform informer event failed", "error", asyncErr)
				}
			}
		}()
		// The workqueue only audits existing targets, so with zero targets no
		// attempt would ever be marked. The tick beats only when the worker has
		// nothing to audit; otherwise audits mark attempts themselves, and a
		// wedged worker must still fail liveness.
		go runResyncHeartbeat(runCtx, cfg.Resync, heartbeat, func() bool {
			load, listErr := loadTargetDefinitions(runCtx, cfg, clients)
			if listErr != nil {
				if runCtx.Err() == nil {
					logger.Warn("periodic target list failed", "reason", reasonTargetListFailed, "error", listErr)
				}
				// An API outage is not a reason to restart the pod.
				return true
			}
			return len(load.definitions) == 0
		})
		err = runtime.Run(runCtx)
		cancel()
		if errors.Is(err, controller.ErrPlatformInformerScopeChanged) && ctx.Err() == nil {
			logger.Info("rebuilding platform informer caches after target scope change")
			continue
		}
		return err
	}
}

// eventLoadRetryMax bounds the wait before retrying a failed initial target
// load in event mode.
const eventLoadRetryMax = 30 * time.Second

// runResyncHeartbeat runs probe on every interval tick until ctx ends and marks
// a heartbeat attempt when probe reports that no audit worker would.
func runResyncHeartbeat(ctx context.Context, interval time.Duration, heartbeat *controller.Heartbeat, probe func() bool) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if probe == nil || probe() {
				heartbeat.MarkAttempt()
			}
		}
	}
}

type platformScope struct {
	sources, namespaces, gatewayNamespaces []string
	headless, ownership, planApproval      bool
}

func platformEventScope(root config.Config, definitions []target.Definition) platformScope {
	scope := platformScope{}
	sourceSet, namespaceSet, gatewaySet := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	allNamespaces := false
	for _, definition := range definitions {
		for _, value := range definition.Sources {
			sourceSet[value] = struct{}{}
		}
		if len(definition.Namespaces) == 0 {
			allNamespaces = true
		}
		for _, value := range definition.Namespaces {
			namespaceSet[value] = struct{}{}
		}
		for _, value := range definition.GatewayTargetNamespaces {
			gatewaySet[value] = struct{}{}
		}
		scope.headless = scope.headless || (root.PublishHeadless && definition.HeadlessEnabled)
		scope.ownership = scope.ownership || definition.OwnershipMode == v1alpha1.OwnershipModeShared
		scope.planApproval = scope.planApproval || definition.ApprovalMode == v1alpha1.ApprovalModeRequired
	}
	for value := range sourceSet {
		scope.sources = append(scope.sources, value)
	}
	if !allNamespaces {
		for value := range namespaceSet {
			scope.namespaces = append(scope.namespaces, value)
		}
	}
	for value := range gatewaySet {
		scope.gatewayNamespaces = append(scope.gatewayNamespaces, value)
	}
	sort.Strings(scope.sources)
	sort.Strings(scope.namespaces)
	sort.Strings(scope.gatewayNamespaces)
	return scope
}

func platformScopesEqual(left, right platformScope) bool {
	return slices.Equal(left.sources, right.sources) && slices.Equal(left.namespaces, right.namespaces) &&
		slices.Equal(left.gatewayNamespaces, right.gatewayNamespaces) && left.headless == right.headless &&
		left.ownership == right.ownership && left.planApproval == right.planApproval
}

// targetLoad is one listing of FortiGateDNSTargets after per-target
// validation: definitions are runnable and conflict-free, invalid holds the
// excluded targets by key.
type targetLoad struct {
	definitions []target.Definition
	invalid     map[string]target.InvalidTarget
}

// loadTargetDefinitions lists the Targets and validates each independently. Only
// a failed LIST (or a global misconfiguration) is an error; an undecodable,
// invalid, or conflicting Target is excluded so healthy siblings keep running.
func loadTargetDefinitions(ctx context.Context, cfg config.Config, clients source.KubernetesClients) (targetLoad, error) {
	list, err := clients.Dynamic.Resource(v1alpha1.TargetGVR).Namespace(cfg.PlatformNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return targetLoad{}, fmt.Errorf("list FortiGate targets: %w", err)
	}
	objects := make([]v1alpha1.FortiGateDNSTarget, 0, len(list.Items))
	undecodable := map[string]target.InvalidTarget{}
	for i := range list.Items {
		var object v1alpha1.FortiGateDNSTarget
		if err := v1alpha1.FromUnstructured(&list.Items[i], &object); err != nil {
			invalid := target.InvalidTarget{
				Namespace: list.Items[i].GetNamespace(), Name: list.Items[i].GetName(), Generation: list.Items[i].GetGeneration(),
				Reason: target.FailureInvalid, Err: fmt.Errorf("decode target: %w", err),
			}
			undecodable[invalid.Key()] = invalid
			continue
		}
		objects = append(objects, object)
	}
	set, err := target.BuildIsolatedDefinitions(cfg, objects)
	if err != nil {
		return targetLoad{}, err
	}
	for key, invalid := range undecodable {
		set.Invalid[key] = invalid
	}
	return targetLoad{definitions: set.Valid, invalid: set.Invalid}, nil
}

// runTargetAudit reconciles one target and records the outcome in the
// reconcile metrics (Runner.RunOnce does that only for the direct mode).
func runTargetAudit(ctx context.Context, root config.Config, clients source.KubernetesClients, runtime *target.Runtime, recorder *metrics.Metrics, logger *slog.Logger) error {
	start := time.Now()
	err := auditTarget(ctx, root, clients, runtime, recorder, logger)
	if !errors.Is(err, context.Canceled) {
		recorder.RecordReconcile(time.Since(start), err)
	}
	return err
}

func auditTarget(ctx context.Context, root config.Config, clients source.KubernetesClients, runtime *target.Runtime, recorder *metrics.Metrics, logger *slog.Logger) error {
	runner, err := buildTargetRunner(root, clients, runtime, recorder, logger)
	if err != nil {
		writeTargetStatus(ctx, runtime, nil, err, false, logger)
		return err
	}
	prepared, err := runner.Prepare(ctx)
	if err != nil {
		writeTargetStatus(ctx, runtime, nil, err, false, logger)
		return err
	}
	err = runner.ApplyPrepared(ctx, prepared)
	writeTargetStatus(ctx, runtime, &prepared, err, err == nil, logger)
	return err
}

func buildTargetRunner(root config.Config, clients source.KubernetesClients, runtime *target.Runtime, recorder *metrics.Metrics, logger *slog.Logger) (controller.Runner, error) {
	definition := runtime.Definition
	if definition.OwnershipMode == v1alpha1.OwnershipModeShared {
		if _, ok := runtime.Stores.OwnershipStore.(*sharedOwnershipHandles); !ok {
			return controller.Runner{}, target.Fail(target.FailureOwnership)
		}
	}
	targetConfig := root
	targetConfig.TargetMode = false
	targetConfig.Once = true
	targetConfig.DryRun = definition.DryRun
	if definition.Interval > 0 {
		targetConfig.Interval = definition.Interval
	}
	// spec.timeout is the per-request HTTP timeout (platformClientFactory); the
	// whole-cycle budget stays the global --reconcile-timeout inherited from root.
	targetConfig.Sources = append([]string(nil), definition.Sources...)
	targetConfig.Namespaces = append([]string(nil), definition.Namespaces...)
	targetConfig.GatewayTargetNamespaces = append([]string(nil), definition.GatewayTargetNamespaces...)
	targetConfig.DomainFilters = append([]string(nil), definition.DomainFilters...)
	if definition.DefaultTTL > 0 {
		targetConfig.DefaultTTL = definition.DefaultTTL
	}
	targetConfig.OwnerID = definition.ControllerID
	targetConfig.CleanupPolicy = string(definition.CleanupPolicy)
	targetConfig.PublishExternalName = root.PublishExternalName && definition.ExternalNameEnabled
	targetConfig.PublishHeadless = root.PublishHeadless && definition.HeadlessEnabled
	targetConfig.PlanOutput = ""
	targetConfig.ApprovedPlanHash = ""
	targetConfig.PlanOutputOverwrite = false
	targetConfig.FortiGate = config.FortiGateConfig{
		Zone: definition.Zone, VDOM: definition.VDOM,
		ExclusiveZoneOwnership: definition.OwnershipMode == v1alpha1.OwnershipModeExclusive,
	}

	dnsClient := runtime.ProviderClient()
	if definition.OwnershipMode == v1alpha1.OwnershipModeShared {
		dnsClient = &sharedDNSClient{
			client: dnsClient, handles: runtime.Stores.OwnershipStore.(*sharedOwnershipHandles),
			namespace: definition.Namespace, targetName: definition.Name, controller: definition.ControllerID,
		}
	}
	runner := controller.Runner{
		Config: targetConfig, Kube: clients, DNSClient: dnsClient, Logger: logger.With("target", definition.Key()), Metrics: recorder,
		TargetName:     definition.Name,
		TargetIdentity: plan.TargetIdentity{Namespace: definition.Namespace, Name: definition.Name, UID: definition.UID, Generation: definition.Generation, VDOM: definition.VDOM, Zone: definition.Zone},
	}
	if root.PolicyEnforcement {
		provider, err := policy.NewDynamicProvider(clients.Dynamic)
		if err != nil {
			return controller.Runner{}, target.Fail(target.FailurePolicy)
		}
		runner.PolicyProvider = provider
	}
	if definition.ApprovalMode == v1alpha1.ApprovalModeRequired {
		store, ok := runtime.Stores.PlanStore.(*plan.ChangePlanStore)
		if !ok {
			return controller.Runner{}, target.Fail(target.FailureApproval)
		}
		runner.ChangePlanStore = store
		runner.ChangePlanNamespace = definition.Namespace
		runner.ApprovalRequired = true
		runner.PlanRetention = root.PlanRetention
	}
	runner.RequireStableRevision = true
	return runner, nil
}

// targetErrorReason extracts the fixed runtime reason carried by err, if any.
func targetErrorReason(err error) (target.FailureReason, bool) {
	var runtimeErr *target.RuntimeError
	if errors.As(err, &runtimeErr) {
		return runtimeErr.Reason, true
	}
	return "", false
}

func isOwnershipError(err error) bool {
	if reason, ok := targetErrorReason(err); ok && reason == target.FailureOwnership {
		return true
	}
	return errors.Is(err, ownership.ErrProviderConflict) || errors.Is(err, ownership.ErrClaimConflict) || errors.Is(err, ownership.ErrDuplicateClaim)
}

func isApprovalError(err error) bool {
	if reason, ok := targetErrorReason(err); ok && reason == target.FailureApproval {
		return true
	}
	return errors.Is(err, ownership.ErrApprovalRequired)
}

// readyFailureReason picks the Ready=False reason from where the reconcile
// stopped rather than reporting every failure as an apply failure.
func readyFailureReason(audit *controller.ReconcileAudit, err error) statuswriter.Reason {
	switch {
	case isOwnershipError(err):
		return statuswriter.ReasonOwnershipConflict
	case isApprovalError(err):
		return statuswriter.ReasonPendingApproval
	}
	if reason, ok := targetErrorReason(err); ok {
		switch reason {
		case target.FailurePolicy:
			return statuswriter.ReasonPolicyRejected
		case target.FailureCredentials:
			return statuswriter.ReasonCredentialsUnavailable
		case target.FailureClient, target.FailureInvalid:
			return statuswriter.ReasonInvalidConfiguration
		}
	}
	switch {
	case audit == nil:
		// Discovery or the provider snapshot failed before a plan existed.
		return statuswriter.ReasonProviderUnavailable
	case !audit.DiscoveryComplete:
		return statuswriter.ReasonDiscoveryIncomplete
	default:
		return statuswriter.ReasonApplyFailed
	}
}

// targetConditions derives every condition from what actually happened: a
// provider outage does not read as an ownership conflict, and ownership
// conflicts do not read as healthy. Conditions whose state is unknowable (no
// audit was produced) are Unknown rather than guessed.
func targetConditions(generation int64, approvalRequired bool, audit *controller.ReconcileAudit, auditErr error) map[statuswriter.ConditionType]statuswriter.ConditionState {
	state := func(ok bool, success, failure statuswriter.Reason) statuswriter.ConditionState {
		value := metav1.ConditionFalse
		reason := failure
		if ok {
			value = metav1.ConditionTrue
			reason = success
		}
		return statuswriter.ConditionState{Status: value, Reason: reason, ObservedGeneration: generation}
	}
	unknown := statuswriter.ConditionState{Status: metav1.ConditionUnknown, Reason: statuswriter.ReasonUnknown, ObservedGeneration: generation}

	complete := audit != nil && audit.DiscoveryComplete
	providerReady := audit != nil && audit.ProviderSnapshotStable && audit.ProviderRevision != ""
	ready := auditErr == nil && audit != nil
	driftFree := ready && len(audit.Operations) == 0

	ownershipState := unknown
	switch {
	case isOwnershipError(auditErr) || (audit != nil && audit.ConflictCount > 0):
		ownershipState = state(false, statuswriter.ReasonOwnershipHealthy, statuswriter.ReasonOwnershipConflict)
	case audit != nil:
		ownershipState = state(true, statuswriter.ReasonOwnershipHealthy, statuswriter.ReasonOwnershipConflict)
	}

	// Without an approval requirement, or with nothing to change, the plan needs
	// no approval. Otherwise it is approved only if the apply got past approval
	// (a failed apply of a plan that requires approval is reported as pending;
	// the runner does not yet expose a typed approval error to tell them apart).
	planState := unknown
	switch {
	case audit == nil && !isApprovalError(auditErr):
	case !approvalRequired || (audit != nil && len(audit.Operations) == 0):
		planState = state(true, statuswriter.ReasonPlanApproved, statuswriter.ReasonPendingApproval)
	default:
		planState = state(auditErr == nil, statuswriter.ReasonPlanApproved, statuswriter.ReasonPendingApproval)
	}

	readyState := state(ready, statuswriter.ReasonReady, readyFailureReason(audit, auditErr))
	return map[statuswriter.ConditionType]statuswriter.ConditionState{
		statuswriter.ConditionReady:             readyState,
		statuswriter.ConditionDiscoveryComplete: state(complete, statuswriter.ReasonDiscoveryComplete, statuswriter.ReasonDiscoveryIncomplete),
		statuswriter.ConditionProviderReachable: state(providerReady, statuswriter.ReasonProviderReachable, statuswriter.ReasonProviderUnavailable),
		statuswriter.ConditionOwnershipHealthy:  ownershipState,
		statuswriter.ConditionPolicyAccepted:    state(complete, statuswriter.ReasonPolicyAccepted, statuswriter.ReasonPolicyRejected),
		statuswriter.ConditionPlanApproved:      planState,
		statuswriter.ConditionDriftFree:         state(driftFree, statuswriter.ReasonDriftFree, statuswriter.ReasonDriftDetected),
	}
}

// writeStatusSnapshot persists a snapshot. A failure (typically missing RBAC)
// is logged with a fixed reason and never fails the reconcile.
func writeStatusSnapshot(ctx context.Context, writer *statuswriter.Writer, snapshot statuswriter.Snapshot, logger *slog.Logger, targetKey string) {
	if err := writer.Write(ctx, snapshot); err != nil {
		logger.Warn("target status write failed", "target", targetKey, "reason", reasonStatusWriteFailed, "error", err)
	}
}

func writeTargetStatus(ctx context.Context, runtime *target.Runtime, audit *controller.ReconcileAudit, auditErr error, applied bool, logger *slog.Logger) {
	writer, ok := runtime.Stores.StatusStore.(*statuswriter.Writer)
	if !ok {
		return
	}
	ready := auditErr == nil && audit != nil
	conditions := targetConditions(runtime.Definition.Generation, runtime.Definition.ApprovalMode == v1alpha1.ApprovalModeRequired, audit, auditErr)
	snapshot := statuswriter.Snapshot{TargetGeneration: runtime.Definition.Generation, AuditTime: time.Now(), Conditions: conditions}
	if audit != nil {
		snapshot.ProviderRevision = audit.ProviderRevision
		snapshot.PlanHash = audit.PlanHash
		snapshot.Counts = v1alpha1.ReconcileCounts{Desired: int32(audit.DesiredCount), Current: int32(audit.CurrentCount), Drift: int32(len(audit.Operations)), Conflicts: int32(audit.ConflictCount)}
		phase := v1alpha1.ChangePlanFailed
		if ready {
			phase = v1alpha1.ChangePlanSucceeded
		}
		snapshot.Audit = &statuswriter.Audit{PlanHash: audit.PlanHash, Phase: phase, Timestamp: time.Now(), Counts: snapshot.Counts}
	}
	if applied {
		now := time.Now()
		snapshot.ApplyTime = &now
	}
	writeStatusSnapshot(ctx, writer, snapshot, logger, runtime.Definition.Key())
}

// setupFailureConditions maps a fixed setup failure reason onto Ready (and
// OwnershipHealthy for scope conflicts). Other conditions stay Unknown.
func setupFailureConditions(generation int64, reason target.FailureReason) map[statuswriter.ConditionType]statuswriter.ConditionState {
	failure := func(reason statuswriter.Reason) statuswriter.ConditionState {
		return statuswriter.ConditionState{Status: metav1.ConditionFalse, Reason: reason, ObservedGeneration: generation}
	}
	switch reason {
	case target.FailureCredentials:
		return map[statuswriter.ConditionType]statuswriter.ConditionState{statuswriter.ConditionReady: failure(statuswriter.ReasonCredentialsUnavailable)}
	case target.FailureInvalid, target.FailureClient:
		return map[statuswriter.ConditionType]statuswriter.ConditionState{statuswriter.ConditionReady: failure(statuswriter.ReasonInvalidConfiguration)}
	case target.FailureConflict:
		return map[statuswriter.ConditionType]statuswriter.ConditionState{
			statuswriter.ConditionReady:            failure(statuswriter.ReasonOwnershipConflict),
			statuswriter.ConditionOwnershipHealthy: failure(statuswriter.ReasonOwnershipConflict),
		}
	default:
		return map[statuswriter.ConditionType]statuswriter.ConditionState{statuswriter.ConditionReady: failure(statuswriter.ReasonApplyFailed)}
	}
}

// writeSetupFailureStatus records a target that could not be set up (invalid,
// conflicting, credentials, client, or resources). It needs only the
// Kubernetes clients: when client construction fails no Runtime (and so no
// status store) exists, yet the CR must not keep its last Ready state.
func writeSetupFailureStatus(ctx context.Context, clients source.KubernetesClients, retention int, logger *slog.Logger, namespace, name string, generation int64, reason target.FailureReason) {
	key := target.Definition{Namespace: namespace, Name: name}.Key()
	writer, err := statuswriter.NewWriter(clients.Dynamic, namespace, name, int32(retention))
	if err != nil {
		logger.Warn("target status write failed", "target", key, "reason", reasonStatusWriteFailed, "error", err)
		return
	}
	writeStatusSnapshot(ctx, writer, statuswriter.Snapshot{
		TargetGeneration: generation, AuditTime: time.Now(), Conditions: setupFailureConditions(generation, reason),
	}, logger, key)
}
