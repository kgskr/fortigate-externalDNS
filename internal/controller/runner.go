package controller

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	"github.com/kgskr/fortigate-external-dns/internal/source"
)

type DNSClient interface {
	ListRecords(ctx context.Context) ([]dns.Endpoint, error)
	Apply(ctx context.Context, operations []plan.Operation, dryRun bool) error
}

type Runner struct {
	Config    config.Config
	Kube      source.KubernetesClients
	DNSClient DNSClient
	Logger    *slog.Logger
	Metrics   *metrics.Metrics
	// Heartbeat, when set, is marked after every completed reconcile attempt so
	// the liveness probe can detect a wedged loop.
	Heartbeat *Heartbeat
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
	r.Heartbeat.MarkAttempt()
	return err
}

func (r Runner) reconcile(ctx context.Context) error {
	if r.Config.ReconcileTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Config.ReconcileTimeout)
		defer cancel()
	}

	opts := source.Options{
		Sources:                 r.Config.Sources,
		Namespaces:              r.Config.Namespaces,
		GatewayTargetNamespaces: r.Config.GatewayTargetNamespaces,
		DomainFilters:           r.Config.DomainFilters,
		DefaultTTL:              r.Config.DefaultTTL,
		Zone:                    r.Config.FortiGate.Zone,
		OwnerID:                 r.Config.OwnerID,
	}
	discovery, err := source.Discover(ctx, r.Kube, opts)
	if err != nil {
		return err
	}
	for _, event := range discovery.Events {
		logger := r.logger()
		if event.Level == source.EventInfo {
			logger.Info("source event", "resource", event.Resource.String(), "hostname", event.Hostname, "message", event.Message)
		} else {
			logger.Warn("source event", "resource", event.Resource.String(), "hostname", event.Hostname, "message", event.Message)
		}
	}

	current, err := r.DNSClient.ListRecords(ctx)
	if err != nil {
		return err
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
	for _, operation := range operations {
		r.Metrics.RecordOperation(operation.Type, "planned")
	}
	return r.DNSClient.Apply(ctx, operations, r.Config.DryRun)
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
