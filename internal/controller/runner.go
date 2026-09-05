package controller

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	"github.com/kgskr/fortigate-external-dns/internal/policy"
	"github.com/kgskr/fortigate-external-dns/internal/source"
)

type DNSClient interface {
	ListRecords(ctx context.Context) ([]dns.Endpoint, error)
	Apply(ctx context.Context, operations []plan.Operation, dryRun bool) error
}

type revisionedDNSClient interface {
	ListRecordsWithRevision(ctx context.Context) ([]dns.Endpoint, string, error)
}

type ownershipPlanPreconditioner interface {
	PlanOwnershipPreconditions(context.Context, []plan.Operation, string) ([]plan.OwnershipPrecondition, error)
}

type resultDNSClient interface {
	ApplyWithResults(context.Context, []plan.Operation, bool) ([]plan.OperationOutcome, error)
}

type Runner struct {
	Config    config.Config
	Kube      source.KubernetesClients
	DNSClient DNSClient
	Logger    *slog.Logger
	Metrics   *metrics.Metrics
	// PolicyProvider is nil when governance is disabled. A configured provider
	// must return a complete snapshot or cleanup is suppressed for the cycle.
	PolicyProvider        policy.Provider
	TargetName            string
	TargetIdentity        plan.TargetIdentity
	ChangePlanStore       *plan.ChangePlanStore
	ChangePlanNamespace   string
	ApprovalRequired      bool
	PlanRetention         int
	RequireStableRevision bool
	// Heartbeat, when set, is marked after every completed reconcile attempt so
	// the liveness probe can detect a wedged loop.
	Heartbeat *Heartbeat
}

// ReconcileAudit is the immutable handoff from discovery/snapshot/planning to
// the mutation boundary. ApplyPrepared consumes exactly these operations and
// re-prepares the complete provider, source, and policy snapshot immediately
// before issuing any request.
type ReconcileAudit struct {
	Operations             []plan.Operation
	Document               plan.Document
	PlanHash               string
	ProviderRevision       string
	DesiredCount           int
	CurrentCount           int
	ConflictCount          int
	PlanRequested          bool
	DiscoveryComplete      bool
	ProviderSnapshotStable bool
}

func (r Runner) Run(ctx context.Context) error {
	if err := r.RunOnce(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.logger().Error("reconcile failed", "error", err)
	}
	ticker := time.NewTicker(r.Config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				r.logger().Error("reconcile failed", "error", err)
			}
		}
	}
}

func (r Runner) RunOnce(ctx context.Context) error {
	start := time.Now()
	err := r.reconcile(ctx)
	r.Metrics.RecordReconcile(time.Since(start), err)
	r.Metrics.SetTargetReadiness(r.metricTargetName(), err == nil)
	r.Heartbeat.MarkAttempt()
	return err
}

func (r Runner) reconcile(ctx context.Context) error {
	audit, err := r.Prepare(ctx)
	if err != nil {
		return err
	}
	return r.ApplyPrepared(ctx, audit)
}

func (r Runner) Prepare(ctx context.Context) (ReconcileAudit, error) {
	if r.Config.ReconcileTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Config.ReconcileTimeout)
		defer cancel()
	}

	opts := source.Options{
		Sources:                     r.Config.Sources,
		Namespaces:                  r.Config.Namespaces,
		GatewayTargetNamespaces:     r.Config.GatewayTargetNamespaces,
		DomainFilters:               r.Config.DomainFilters,
		DefaultTTL:                  r.Config.DefaultTTL,
		Zone:                        r.Config.FortiGate.Zone,
		OwnerID:                     r.Config.OwnerID,
		PublishExternalNameServices: r.Config.PublishExternalName,
		PublishHeadlessServices:     r.Config.PublishHeadless,
	}
	discovery, err := source.Discover(ctx, r.Kube, opts)
	if err != nil {
		return ReconcileAudit{}, err
	}
	if r.PolicyProvider != nil {
		evaluator, policyErr := r.PolicyProvider.Evaluator(ctx, r.Config.Namespaces, policy.Bounds{
			SourceKinds: r.Config.Sources, HostnameSuffixes: r.Config.DomainFilters,
			TTL: &v1alpha1.TTLRange{Minimum: 1, Maximum: config.MaxDefaultTTL},
		})
		if policyErr != nil {
			return ReconcileAudit{}, fmt.Errorf("load DNS policy snapshot: %w", policyErr)
		} else {
			candidates := make([]policy.Candidate, 0, len(discovery.Endpoints))
			for _, endpoint := range discovery.Endpoints {
				metadata := discovery.MetadataFor(endpoint.Source)
				candidates = append(candidates, policy.Candidate{
					Endpoint: endpoint, TargetName: r.targetName(), Labels: metadata.Labels, Annotations: metadata.Annotations,
				})
			}
			policyResult := evaluator.Evaluate(candidates)
			discovery.Endpoints = discovery.Endpoints[:0]
			for _, allowed := range policyResult.Allowed {
				discovery.Endpoints = append(discovery.Endpoints, allowed.Endpoint)
			}
			for _, rejection := range policyResult.Rejected {
				discovery.AddEvent(rejection.Candidate.Endpoint.Source, rejection.Candidate.Endpoint.DNSName, "DNS policy rejected publication: "+string(rejection.Reason))
			}
		}
	}
	for _, event := range discovery.Events {
		logger := r.logger()
		if event.Level == source.EventInfo {
			logger.Info("source event", "resource", event.Resource.String(), "hostname", event.Hostname, "message", event.Message)
		} else {
			logger.Warn("source event", "resource", event.Resource.String(), "hostname", event.Hostname, "message", event.Message)
		}
	}

	var current []dns.Endpoint
	providerRevision := ""
	planRequested := r.Config.PlanOutput != "" || r.Config.ApprovedPlanHash != "" || r.ChangePlanStore != nil || r.RequireStableRevision
	if planRequested {
		revisioned, ok := r.DNSClient.(revisionedDNSClient)
		if !ok {
			return ReconcileAudit{}, fmt.Errorf("one-shot plan output or approval requires a revisioned DNS client")
		}
		current, providerRevision, err = revisioned.ListRecordsWithRevision(ctx)
		if err != nil {
			return ReconcileAudit{}, err
		}
		if strings.TrimSpace(providerRevision) == "" {
			return ReconcileAudit{}, fmt.Errorf("one-shot plan output or approval requires a non-empty provider snapshot revision")
		}
	} else {
		current, err = r.DNSClient.ListRecords(ctx)
		if err != nil {
			return ReconcileAudit{}, err
		}
	}
	var restrictedOwnershipConflicts map[string]restrictedOwnershipConflict
	if r.Config.FortiGate.ExclusiveZoneOwnership {
		restricted := len(r.Config.Namespaces) > 0 || sourcesAreRestrictive(r.Config.Sources)
		restrictedOwnershipConflicts = prepareExclusiveOwnership(current, discovery.Endpoints, r.Config.OwnerID, restricted)
	}
	cleanupSuppressed := discovery.HasIncompleteSources()
	if cleanupSuppressed {
		r.logger().Warn("source discovery incomplete; suppressing all cleanup operations for this cycle")
	}
	operations := plan.BuildWithCleanupScope(
		discovery.Endpoints,
		current,
		r.Config.OwnerID,
		plan.CleanupPolicy(r.Config.CleanupPolicy),
		func(endpoint dns.Endpoint) bool {
			if cleanupSuppressed {
				return false
			}
			return cleanupAllowed(endpoint, opts, r.Config.FortiGate.ExclusiveZoneOwnership)
		},
	)
	operations = enforceRestrictedOwnershipConflicts(operations, restrictedOwnershipConflicts)
	operations, refusal := guardCleanup(operations, len(discovery.Endpoints), r.Config)
	if refusal.count > 0 {
		r.logger().Error("mass-cleanup guard refused this cycle's cleanup operations",
			"reason", refusal.reason,
			"plannedCleanup", refusal.count,
			"maxCleanupPerCycle", r.Config.MaxCleanupPerCycle,
			"allowEmptyDesiredCleanup", r.Config.AllowEmptyDesiredCleanup)
		r.Metrics.RecordCleanupRefused(refusal.reason)
	}
	r.logger().Info("reconcile plan built", "desired", len(discovery.Endpoints), "current", len(current), "operations", len(operations), "dryRun", r.Config.DryRun)
	if len(operations) > 0 {
		r.logger().Info("planned operations", "plan", plan.Format(operations))
	}
	var document plan.Document
	planHash := ""
	if planRequested {
		document = oneShotDocument(r.Config, discovery, providerRevision, operations)
		if r.TargetIdentity.Name != "" {
			document.Target = r.TargetIdentity
		} else {
			document.Target.Name = r.targetName()
		}
		if preconditioner, ok := r.DNSClient.(ownershipPlanPreconditioner); ok {
			ownershipPreconditions, ownershipErr := preconditioner.PlanOwnershipPreconditions(ctx, operations, providerRevision)
			if ownershipErr != nil {
				return ReconcileAudit{}, ownershipErr
			}
			document.Preconditions.Ownership = ownershipPreconditions
		}
		var planErr error
		planHash, planErr = document.ID()
		if planErr != nil {
			return ReconcileAudit{}, fmt.Errorf("identify reconciliation plan: %w", planErr)
		}
	}
	conflictCount := 0
	for _, operation := range operations {
		if operation.Type == plan.OperationConflict {
			conflictCount++
		}
	}
	r.Metrics.SetTargetCounts(r.metricTargetName(), metrics.TargetCounts{
		Desired: int64(len(discovery.Endpoints)), Current: int64(len(current)),
		Drift: int64(len(operations)), Conflicts: int64(conflictCount),
	})
	r.Metrics.SetProviderSnapshotAge(r.metricTargetName(), 0)
	for _, sourceName := range r.Config.Sources {
		r.Metrics.SetSourceIncomplete(r.metricTargetName(), metricSource(sourceName), !discovery.SourceComplete(sourceName))
	}
	return ReconcileAudit{
		Operations: append([]plan.Operation(nil), operations...), Document: document, PlanHash: planHash, ProviderRevision: providerRevision,
		DesiredCount: len(discovery.Endpoints), CurrentCount: len(current), ConflictCount: conflictCount, PlanRequested: planRequested,
		DiscoveryComplete: !discovery.HasIncompleteSources(), ProviderSnapshotStable: !planRequested || providerRevision != "",
	}, nil
}

func (r Runner) ApplyPrepared(ctx context.Context, audit ReconcileAudit) error {
	if r.Config.ReconcileTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Config.ReconcileTimeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	operations := append([]plan.Operation(nil), audit.Operations...)
	document := audit.Document
	var changePlanName string
	if audit.PlanRequested {
		planID, planErr := document.ID()
		if planErr != nil {
			return fmt.Errorf("identify reconciliation plan: %w", planErr)
		}
		if r.Config.PlanOutput != "" {
			if err := plan.WriteCanonicalFile(r.Config.PlanOutput, document, r.Config.PlanOutputOverwrite); err != nil {
				return err
			}
		}
		r.logger().Info("canonical one-shot plan ready", "planHash", planID, "planOutput", r.Config.PlanOutput)
		if r.Config.ApprovedPlanHash != "" {
			if err := plan.VerifyApprovedHash(document, r.Config.ApprovedPlanHash); err != nil {
				return err
			}
		}
		if r.ChangePlanStore != nil {
			retention := r.PlanRetention
			if retention == 0 {
				retention = 20
			}
			persisted, persistErr := r.ChangePlanStore.PersistCurrent(ctx, r.ChangePlanNamespace, document, nil, retention)
			if persistErr != nil {
				return persistErr
			}
			changePlanName = persisted.Name
			r.Metrics.SetCurrentPlanPhase(r.metricTargetName(), v1alpha1.ChangePlanPendingApproval)
			if r.ApprovalRequired {
				if approvalErr := r.ChangePlanStore.RequireExactApproval(persisted); approvalErr != nil {
					return approvalErr
				}
				if _, statusErr := r.ChangePlanStore.UpdatePhase(ctx, r.ChangePlanNamespace, changePlanName, v1alpha1.ChangePlanApproved, nil); statusErr != nil {
					return statusErr
				}
				r.Metrics.SetCurrentPlanPhase(r.metricTargetName(), v1alpha1.ChangePlanApproved)
			}
		}

		// Approval authorizes one exact canonical plan. Rebuild that plan from a
		// fresh provider, source, policy, and ownership snapshot immediately before
		// mutation so source deletion or policy drift cannot reuse stale approval.
		current, prepareErr := r.Prepare(ctx)
		if prepareErr != nil {
			if r.ChangePlanStore != nil && changePlanName != "" {
				_, _ = r.ChangePlanStore.UpdatePhase(ctx, r.ChangePlanNamespace, changePlanName, v1alpha1.ChangePlanStale, nil)
				r.Metrics.SetCurrentPlanPhase(r.metricTargetName(), v1alpha1.ChangePlanStale)
			}
			return fmt.Errorf("revalidate approved plan: %w", prepareErr)
		}
		if current.PlanHash == "" || current.PlanHash != planID {
			if r.ChangePlanStore != nil && changePlanName != "" {
				_, _ = r.ChangePlanStore.UpdatePhase(ctx, r.ChangePlanNamespace, changePlanName, v1alpha1.ChangePlanStale, nil)
				r.Metrics.SetCurrentPlanPhase(r.metricTargetName(), v1alpha1.ChangePlanStale)
			}
			return plan.ErrPreconditionDrift
		}
		if r.ChangePlanStore != nil {
			if _, statusErr := r.ChangePlanStore.UpdatePhase(ctx, r.ChangePlanNamespace, changePlanName, v1alpha1.ChangePlanApplying, nil); statusErr != nil {
				return statusErr
			}
			r.Metrics.SetCurrentPlanPhase(r.metricTargetName(), v1alpha1.ChangePlanApplying)
		}
	}
	for _, operation := range operations {
		r.Metrics.RecordOperation(operation.Type, "planned")
	}
	var outcomes []plan.OperationOutcome
	var applyErr error
	if client, ok := r.DNSClient.(resultDNSClient); ok {
		outcomes, applyErr = client.ApplyWithResults(ctx, operations, r.Config.DryRun)
	} else {
		applyErr = r.DNSClient.Apply(ctx, operations, r.Config.DryRun)
		for _, operation := range operations {
			fallback := plan.ApplySucceeded
			reason := ""
			switch {
			case operation.Type == plan.OperationConflict:
				fallback, reason = plan.ApplyBlocked, "planning-conflict"
			case applyErr != nil:
				// The legacy interface cannot identify completed operations after a
				// partial failure. Preserve that uncertainty instead of claiming that
				// every request failed.
				fallback, reason = plan.ApplyBlocked, "provider-request-failed"
			case r.Config.DryRun:
				fallback, reason = plan.ApplyBlocked, "dry-run"
			}
			outcomes = append(outcomes, plan.OperationOutcome{OperationID: plan.SanitizeOperation(operation).ID, Result: fallback, Reason: reason})
		}
	}
	var valid bool
	outcomes, valid = validateOperationOutcomes(operations, outcomes, r.Config.DryRun)
	if !valid {
		applyErr = errors.Join(applyErr, errors.New("provider returned invalid operation results"))
	}
	executionSucceeded := valid && operationExecutionSucceeded(operations, outcomes, r.Config.DryRun)
	if r.Config.DryRun {
		r.Metrics.RecordApply(r.metricTargetName(), metrics.ApplyRejected)
	} else if hasPlanningConflict(operations) && applyErr == nil {
		r.Metrics.RecordApply(r.metricTargetName(), metrics.ApplyRejected)
	} else if errors.Is(applyErr, context.Canceled) || errors.Is(applyErr, context.DeadlineExceeded) {
		r.Metrics.RecordApply(r.metricTargetName(), metrics.ApplyInterrupted)
	} else if applyErr != nil || !executionSucceeded {
		r.Metrics.RecordApply(r.metricTargetName(), metrics.ApplyFailed)
	} else {
		r.Metrics.RecordApply(r.metricTargetName(), metrics.ApplySucceeded)
	}
	if r.ChangePlanStore != nil && changePlanName != "" {
		phase := v1alpha1.ChangePlanSucceeded
		if applyErr != nil || !executionSucceeded {
			phase = v1alpha1.ChangePlanFailed
		}
		if r.Config.DryRun && valid && applyErr == nil && executionSucceeded {
			phase = v1alpha1.ChangePlanSucceeded
		}
		if errors.Is(applyErr, context.Canceled) || errors.Is(applyErr, context.DeadlineExceeded) {
			phase = v1alpha1.ChangePlanInterrupted
		}
		if _, statusErr := r.ChangePlanStore.UpdatePhase(ctx, r.ChangePlanNamespace, changePlanName, phase, outcomes); statusErr != nil && applyErr == nil {
			return statusErr
		} else if statusErr == nil {
			r.Metrics.SetCurrentPlanPhase(r.metricTargetName(), phase)
		}
	}
	return applyErr
}

func metricSource(value string) metrics.Source {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case source.SourceService:
		return metrics.SourceService
	case source.SourceIngress:
		return metrics.SourceIngress
	case source.SourceGateway:
		return metrics.SourceGateway
	default:
		return metrics.SourceUnknown
	}
}

func validateOperationOutcomes(operations []plan.Operation, outcomes []plan.OperationOutcome, dryRun bool) ([]plan.OperationOutcome, bool) {
	expected := make(map[string]plan.Operation, len(operations))
	for _, operation := range operations {
		expected[plan.SanitizeOperation(operation).ID] = operation
	}
	seen := make(map[string]struct{}, len(outcomes))
	valid := len(expected) == len(operations)
	for _, outcome := range outcomes {
		operation, exists := expected[outcome.OperationID]
		_, duplicate := seen[outcome.OperationID]
		if !exists || duplicate || (outcome.Result != plan.ApplySucceeded && outcome.Result != plan.ApplyFailed && outcome.Result != plan.ApplyBlocked) {
			valid = false
			continue
		}
		if dryRun && outcome.Result == plan.ApplySucceeded {
			valid = false
		}
		if operation.Type == plan.OperationConflict && (outcome.Result != plan.ApplyBlocked || outcome.Reason != "planning-conflict") {
			valid = false
		}
		seen[outcome.OperationID] = struct{}{}
	}
	for id := range expected {
		if _, ok := seen[id]; !ok {
			valid = false
		}
	}
	return append([]plan.OperationOutcome(nil), outcomes...), valid
}

func operationExecutionSucceeded(operations []plan.Operation, outcomes []plan.OperationOutcome, dryRun bool) bool {
	byID := make(map[string]plan.OperationOutcome, len(outcomes))
	for _, outcome := range outcomes {
		byID[outcome.OperationID] = outcome
	}
	for _, operation := range operations {
		outcome := byID[plan.SanitizeOperation(operation).ID]
		if dryRun && outcome.Result == plan.ApplyBlocked && outcome.Reason == "dry-run" {
			continue
		}
		if outcome.Result != plan.ApplySucceeded {
			return false
		}
	}
	return true
}

func hasPlanningConflict(operations []plan.Operation) bool {
	for _, operation := range operations {
		if operation.Type == plan.OperationConflict {
			return true
		}
	}
	return false
}

func (r Runner) targetName() string {
	if value := strings.TrimSpace(r.TargetName); value != "" {
		return value
	}
	return "default"
}

func (r Runner) metricTargetName() string {
	name := strings.TrimSpace(r.TargetIdentity.Name)
	if name == "" {
		name = r.targetName()
	}
	if namespace := strings.TrimSpace(r.TargetIdentity.Namespace); namespace != "" {
		return namespace + "/" + name
	}
	return name
}

func oneShotDocument(cfg config.Config, discovery source.Result, providerRevision string, operations []plan.Operation) plan.Document {
	sourceNames := append([]string(nil), cfg.Sources...)
	sort.Strings(sourceNames)
	sourcePreconditions := make([]plan.DiscoverySourcePrecondition, 0, len(sourceNames))
	for _, sourceName := range sourceNames {
		sourcePreconditions = append(sourcePreconditions, plan.DiscoverySourcePrecondition{
			Kind: sourceName, Complete: discovery.SourceComplete(sourceName),
		})
	}
	document := plan.NewDocument(
		plan.TargetIdentity{Name: "default", VDOM: cfg.FortiGate.VDOM, Zone: cfg.FortiGate.Zone},
		plan.Preconditions{
			Provider: plan.ProviderPrecondition{Revision: providerRevision, Stable: true, Complete: true},
			Discovery: plan.DiscoveryPrecondition{
				Generation: endpointSetGeneration(discovery.Endpoints),
				Complete:   !discovery.HasIncompleteSources(),
				Sources:    sourcePreconditions,
			},
			Policy: plan.PolicyPrecondition{Complete: true},
		},
		operations,
	)
	document.SafetyDecisions = []plan.SafetyDecision{
		{Code: plan.SafetyDecisionProviderSnapshotStable, Allowed: true},
		{Code: plan.SafetyDecisionDiscoveryComplete, Allowed: !discovery.HasIncompleteSources()},
	}
	return document
}

func endpointSetGeneration(endpoints []dns.Endpoint) int64 {
	values := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		endpoint = endpoint.Normalize()
		value, _ := json.Marshal(struct {
			Key, OwnerID, APIVersion, Kind, Namespace, Name, UID string
			TTL                                                  int64
			Disabled                                             bool
		}{
			Key: endpoint.Key(), OwnerID: endpoint.OwnerID,
			APIVersion: endpoint.Source.APIVersion, Kind: endpoint.Source.Kind,
			Namespace: endpoint.Source.Namespace, Name: endpoint.Source.Name, UID: endpoint.Source.UID,
			TTL: endpoint.TTL, Disabled: endpoint.Disabled,
		})
		values = append(values, string(value))
	}
	sort.Strings(values)
	digest := sha256.Sum256([]byte(strings.Join(values, "\n")))
	return int64(binary.BigEndian.Uint64(digest[:8]) & uint64(^uint64(0)>>1))
}

// restrictedOwnershipConflict identifies a DNS owner name whose existing rows
// cannot be safely adopted while source or namespace discovery is restricted.
// FortiGate exposes no persisted source identity, so any target, type, TTL,
// status, or cardinality mismatch must fail closed instead of becoming an
// update, replacement, create, or cleanup.
type restrictedOwnershipConflict struct {
	desired dns.Endpoint
	current dns.Endpoint
}

// prepareExclusiveOwnership marks current rows as controller-owned for planner
// input. Complete, unrestricted exclusive-zone discovery adopts every row. In
// restricted mode it adopts only rows that exactly match the planner's
// normalized desired record, and records a name-level conflict unless the full
// current and desired sets for that DNS owner name are identical. A name with no
// current rows is not conflicted, so genuinely missing names can still be
// created.
func prepareExclusiveOwnership(current, desired []dns.Endpoint, ownerID string, restricted bool) map[string]restrictedOwnershipConflict {
	if !restricted {
		for i := range current {
			current[i].OwnerID = ownerID
		}
		return nil
	}

	// Match planner.BuildWithCleanupScope's last-desired-value-per-key behavior so
	// ownership cannot be granted against one duplicate desired row and then used
	// to update toward another row with the same normalized key.
	desiredByKey := make(map[string]dns.Endpoint, len(desired))
	for _, endpoint := range desired {
		endpoint = endpoint.Normalize()
		desiredByKey[endpoint.Key()] = endpoint
	}
	desiredByGroup := map[string][]dns.Endpoint{}
	for _, endpoint := range desiredByKey {
		group := endpoint.MutationGroupKey()
		desiredByGroup[group] = append(desiredByGroup[group], endpoint)
	}

	currentByGroup := map[string][]dns.Endpoint{}
	for i := range current {
		// The supported FortiGate schema carries no ownership metadata. Never trust
		// a caller-populated value in restricted mode; re-establish ownership only
		// through an exact desired-state match below.
		current[i].OwnerID = ""
		normalized := current[i].Normalize()
		group := normalized.MutationGroupKey()
		currentByGroup[group] = append(currentByGroup[group], normalized)
		if desiredEndpoint, ok := desiredByKey[normalized.Key()]; ok && normalized.EqualRecord(desiredEndpoint) {
			current[i].OwnerID = ownerID
		}
	}

	conflicts := map[string]restrictedOwnershipConflict{}
	for group, desiredEndpoints := range desiredByGroup {
		currentEndpoints := currentByGroup[group]
		if len(currentEndpoints) == 0 || restrictedRecordSetsEqual(currentEndpoints, desiredEndpoints, desiredByKey) {
			continue
		}
		sortEndpointsForConflict(desiredEndpoints)
		sortEndpointsForConflict(currentEndpoints)
		conflicts[group] = restrictedOwnershipConflict{
			desired: desiredEndpoints[0],
			current: currentEndpoints[0],
		}
	}
	return conflicts
}

func restrictedRecordSetsEqual(current, desired []dns.Endpoint, desiredByKey map[string]dns.Endpoint) bool {
	if len(current) != len(desired) {
		return false
	}
	seen := make(map[string]struct{}, len(current))
	for _, currentEndpoint := range current {
		key := currentEndpoint.Key()
		desiredEndpoint, ok := desiredByKey[key]
		if !ok || !currentEndpoint.EqualRecord(desiredEndpoint) {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func sortEndpointsForConflict(endpoints []dns.Endpoint) {
	sort.Slice(endpoints, func(i, j int) bool {
		if endpoints[i].Key() != endpoints[j].Key() {
			return endpoints[i].Key() < endpoints[j].Key()
		}
		return endpoints[i].ProviderID < endpoints[j].ProviderID
	})
}

// enforceRestrictedOwnershipConflicts replaces every planner result for a
// mismatched restricted name with one deterministic conflict. This wider guard
// is required because A and AAAA records can normally coexist, while restricted
// exclusive mode may create only a genuinely absent DNS owner name.
func enforceRestrictedOwnershipConflicts(operations []plan.Operation, conflicts map[string]restrictedOwnershipConflict) []plan.Operation {
	if len(conflicts) == 0 {
		return operations
	}

	kept := make([]plan.Operation, 0, len(operations)+len(conflicts))
	for _, operation := range operations {
		endpoint := operation.Desired
		if endpoint.DNSName == "" {
			endpoint = operation.Current
		}
		if _, conflicted := conflicts[endpoint.MutationGroupKey()]; conflicted {
			continue
		}
		kept = append(kept, operation)
	}

	groups := make([]string, 0, len(conflicts))
	for group := range conflicts {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	conflictOperations := make([]plan.Operation, 0, len(groups))
	for _, group := range groups {
		conflict := conflicts[group]
		conflictOperations = append(conflictOperations, plan.Operation{
			Type:    plan.OperationConflict,
			Desired: conflict.desired,
			Current: conflict.current,
			Reason:  "restricted exclusive-zone discovery cannot prove ownership of mismatched existing records",
		})
	}
	return append(conflictOperations, kept...)
}

// cleanupRefusal describes a mass-cleanup guard trip: how many cleanup
// operations were dropped from the cycle and why.
type cleanupRefusal struct {
	reason string
	count  int
}

const (
	refusalEmptyDesired = "empty-desired"
	refusalCapExceeded  = "cap-exceeded"
)

// guardCleanup strips delete/deactivate operations from a cycle's plan when
// the mass-cleanup guard trips: a successful discovery that produced zero
// desired endpoints is the signature of a misconfiguration (wrong domain
// filter or namespace) rather than a legitimate teardown, and an optional
// numeric cap bounds cleanup blast radius per cycle. Creates, updates, and
// conflicts pass through untouched — they are the safe direction — and the
// next cycle re-evaluates from fresh discovery.
func guardCleanup(operations []plan.Operation, desiredCount int, cfg config.Config) ([]plan.Operation, cleanupRefusal) {
	cleanupCount := 0
	for _, operation := range operations {
		if operation.Type == plan.OperationDelete || operation.Type == plan.OperationDeactivate {
			cleanupCount++
		}
	}
	if cleanupCount == 0 {
		return operations, cleanupRefusal{}
	}
	reason := ""
	switch {
	case desiredCount == 0 && !cfg.AllowEmptyDesiredCleanup:
		reason = refusalEmptyDesired
	case cfg.MaxCleanupPerCycle > 0 && cleanupCount > cfg.MaxCleanupPerCycle:
		reason = refusalCapExceeded
	default:
		return operations, cleanupRefusal{}
	}
	kept := operations[:0:0]
	for _, operation := range operations {
		if operation.Type == plan.OperationDelete || operation.Type == plan.OperationDeactivate {
			continue
		}
		kept = append(kept, operation)
	}
	return kept, cleanupRefusal{reason: reason, count: cleanupCount}
}

func cleanupAllowed(endpoint dns.Endpoint, opts source.Options, exclusiveZoneOwnership bool) bool {
	if !opts.DomainAllowed(endpoint.DNSName) {
		return false
	}
	if exclusiveZoneOwnership {
		return true
	}
	if len(opts.Namespaces) > 0 {
		if endpoint.Source.Namespace == "" || !opts.NamespaceAllowed(endpoint.Source.Namespace) {
			return false
		}
	}
	if sourcesAreRestrictive(opts.Sources) {
		sourceName := cleanupSourceName(endpoint.Source.Kind)
		if sourceName == "" || !opts.SourceEnabled(sourceName) {
			return false
		}
	}
	return true
}

func sourcesAreRestrictive(sources []string) bool {
	if len(sources) == 0 {
		return false
	}
	enabled := map[string]struct{}{}
	for _, sourceName := range sources {
		enabled[strings.ToLower(sourceName)] = struct{}{}
	}
	_, service := enabled[source.SourceService]
	_, ingress := enabled[source.SourceIngress]
	_, gateway := enabled[source.SourceGateway]
	return !(service && ingress && gateway)
}

func cleanupSourceName(kind string) string {
	switch strings.ToLower(kind) {
	case "service":
		return source.SourceService
	case "ingress":
		return source.SourceIngress
	case "gateway", "httproute":
		return source.SourceGateway
	default:
		return ""
	}
}

func (r Runner) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}
