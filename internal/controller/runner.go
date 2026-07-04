package controller

import (
	"context"
	"log/slog"
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
		return err
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
	operations := plan.BuildWithCleanupScope(
		discovery.Endpoints,
		current,
		r.Config.OwnerID,
		plan.CleanupPolicy(r.Config.CleanupPolicy),
		func(endpoint dns.Endpoint) bool {
			return cleanupAllowed(endpoint, opts)
		},
	)
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

func cleanupAllowed(endpoint dns.Endpoint, opts source.Options) bool {
	if !opts.DomainAllowed(endpoint.DNSName) {
		return false
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
