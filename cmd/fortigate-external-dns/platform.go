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
		definitions, loadErr := loadTargetDefinitions(ctx, cfg, clients)
		if loadErr != nil {
			return loadErr
		}
		syncResult, syncErr := manager.Sync(ctx, definitions)
		if syncErr != nil {
			return syncErr
		}
		for key, reason := range syncResult.Failures {
			logger.Error("target setup failed", "target", key, "reason", reason)
		}
		results := manager.RunAll(ctx, func(runCtx context.Context, runtime *target.Runtime) error {
			return runTargetAudit(runCtx, cfg, clients, runtime, recorder, logger)
		})
		for key, result := range results {
			if !result.Succeeded {
				logger.Error("target audit failed", "target", key, "reason", result.Reason)
			}
		}
		heartbeat.MarkAttempt()
		if cfg.Once {
			for _, failure := range syncResult.Failures {
				if failure != "" {
					return fmt.Errorf("one or more targets failed setup")
				}
			}
			for _, result := range results {
				if !result.Succeeded {
					return fmt.Errorf("one or more targets failed audit")
				}
			}
			return nil
		}
		timer := time.NewTimer(cfg.Resync)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
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
}

func (e eventTargetExecutor) Audit(ctx context.Context, key platformqueue.TargetKey) (controller.TargetAudit, error) {
	definitions, err := loadTargetDefinitions(ctx, e.cfg, e.clients)
	if err != nil {
		return controller.TargetAudit{}, err
	}
	if !platformScopesEqual(e.scope, platformEventScope(e.cfg, definitions)) {
		return controller.TargetAudit{}, controller.ErrPlatformInformerScopeChanged
	}
	result, err := e.manager.Sync(ctx, definitions)
	if err != nil {
		return controller.TargetAudit{}, err
	}
	if reason := result.Failures[key.String()]; reason != "" {
		return controller.TargetAudit{}, target.Fail(reason)
	}
	runtime, ok := e.manager.Runtime(key.String())
	if !ok {
		return controller.TargetAudit{}, fmt.Errorf("target runtime is unavailable")
	}
	runner, err := buildTargetRunner(e.cfg, e.clients, runtime, e.recorder, e.logger)
	if err != nil {
		return controller.TargetAudit{}, err
	}
	prepared, err := runner.Prepare(ctx)
	if err != nil {
		writeTargetStatus(ctx, runtime, nil, err, false)
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
		State:                  &preparedTargetAudit{runtime: runtime, runner: runner, audit: prepared},
	}, nil
}

func (e eventTargetExecutor) Apply(ctx context.Context, _ platformqueue.TargetKey, audit controller.TargetAudit) error {
	prepared, ok := audit.State.(*preparedTargetAudit)
	if !ok || prepared == nil || prepared.runtime == nil {
		return fmt.Errorf("target runtime audit state is invalid")
	}
	err := prepared.runner.ApplyPrepared(ctx, prepared.audit)
	writeTargetStatus(ctx, prepared.runtime, &prepared.audit, err, err == nil)
	e.heartbeat.MarkAttempt()
	return err
}

func (e eventTargetExecutor) TargetDeleted(ctx context.Context, _ platformqueue.TargetKey) error {
	definitions, err := loadTargetDefinitions(ctx, e.cfg, e.clients)
	if err != nil {
		return err
	}
	if !platformScopesEqual(e.scope, platformEventScope(e.cfg, definitions)) {
		return controller.ErrPlatformInformerScopeChanged
	}
	_, err = e.manager.Sync(ctx, definitions)
	return err
}

func runEventTargetMode(ctx context.Context, cfg config.Config, clients source.KubernetesClients, manager *target.RuntimeManager, recorder *metrics.Metrics, logger *slog.Logger, heartbeat *controller.Heartbeat) error {
	for {
		definitions, err := loadTargetDefinitions(ctx, cfg, clients)
		if err != nil {
			return err
		}
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
		err = runtime.Run(runCtx)
		cancel()
		if errors.Is(err, controller.ErrPlatformInformerScopeChanged) && ctx.Err() == nil {
			logger.Info("rebuilding platform informer caches after target scope change")
			continue
		}
		return err
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

func loadTargetDefinitions(ctx context.Context, cfg config.Config, clients source.KubernetesClients) ([]target.Definition, error) {
	list, err := clients.Dynamic.Resource(v1alpha1.TargetGVR).Namespace(cfg.PlatformNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list FortiGate targets: %w", err)
	}
	objects := make([]v1alpha1.FortiGateDNSTarget, 0, len(list.Items))
	for i := range list.Items {
		var object v1alpha1.FortiGateDNSTarget
		if err := v1alpha1.FromUnstructured(&list.Items[i], &object); err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return target.BuildDefinitions(cfg, true, objects)
}

func runTargetAudit(ctx context.Context, root config.Config, clients source.KubernetesClients, runtime *target.Runtime, recorder *metrics.Metrics, logger *slog.Logger) error {
	runner, err := buildTargetRunner(root, clients, runtime, recorder, logger)
	if err != nil {
		writeTargetStatus(ctx, runtime, nil, err, false)
		return err
	}
	prepared, err := runner.Prepare(ctx)
	if err != nil {
		writeTargetStatus(ctx, runtime, nil, err, false)
		return err
	}
	err = runner.ApplyPrepared(ctx, prepared)
	writeTargetStatus(ctx, runtime, &prepared, err, err == nil)
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
	if definition.Timeout > 0 {
		targetConfig.ReconcileTimeout = definition.Timeout
	}
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

func writeTargetStatus(ctx context.Context, runtime *target.Runtime, audit *controller.ReconcileAudit, auditErr error, applied bool) {
	writer, ok := runtime.Stores.StatusStore.(*statuswriter.Writer)
	if !ok {
		return
	}
	state := func(ok bool, success, failure statuswriter.Reason) statuswriter.ConditionState {
		value := metav1.ConditionFalse
		reason := failure
		if ok {
			value = metav1.ConditionTrue
			reason = success
		}
		return statuswriter.ConditionState{Status: value, Reason: reason, ObservedGeneration: runtime.Definition.Generation}
	}
	complete := audit != nil && audit.DiscoveryComplete
	providerReady := audit != nil && audit.ProviderSnapshotStable && audit.ProviderRevision != ""
	ready := auditErr == nil && audit != nil
	driftFree := ready && len(audit.Operations) == 0
	conditions := map[statuswriter.ConditionType]statuswriter.ConditionState{
		statuswriter.ConditionReady:             state(ready, statuswriter.ReasonReady, statuswriter.ReasonApplyFailed),
		statuswriter.ConditionDiscoveryComplete: state(complete, statuswriter.ReasonDiscoveryComplete, statuswriter.ReasonDiscoveryIncomplete),
		statuswriter.ConditionProviderReachable: state(providerReady, statuswriter.ReasonProviderReachable, statuswriter.ReasonProviderUnavailable),
		statuswriter.ConditionOwnershipHealthy:  state(ready, statuswriter.ReasonOwnershipHealthy, statuswriter.ReasonOwnershipConflict),
		statuswriter.ConditionPolicyAccepted:    state(complete, statuswriter.ReasonPolicyAccepted, statuswriter.ReasonPolicyRejected),
		statuswriter.ConditionPlanApproved:      state(ready, statuswriter.ReasonPlanApproved, statuswriter.ReasonPendingApproval),
		statuswriter.ConditionDriftFree:         state(driftFree, statuswriter.ReasonDriftFree, statuswriter.ReasonDriftDetected),
	}
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
	_ = writer.Write(ctx, snapshot)
}
