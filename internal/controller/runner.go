package controller

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/gilsu/fortigate-external-dns/internal/config"
	"github.com/gilsu/fortigate-external-dns/internal/dns"
	"github.com/gilsu/fortigate-external-dns/internal/plan"
	"github.com/gilsu/fortigate-external-dns/internal/source"
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
	opts := source.Options{
		Sources:       r.Config.Sources,
		Namespaces:    r.Config.Namespaces,
		DomainFilters: r.Config.DomainFilters,
		DefaultTTL:    r.Config.DefaultTTL,
		Zone:          r.Config.FortiGate.Zone,
		OwnerID:       r.Config.OwnerID,
	}
	discovery, err := source.Discover(ctx, r.Kube, opts)
	if err != nil {
		return err
	}
	for _, event := range discovery.Events {
		r.logger().Warn("source event", "resource", event.Resource.String(), "hostname", event.Hostname, "message", event.Message)
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
	r.logger().Info("reconcile plan built", "desired", len(discovery.Endpoints), "current", len(current), "operations", len(operations), "dryRun", r.Config.DryRun)
	if len(operations) > 0 {
		r.logger().Info("planned operations", "plan", plan.Format(operations))
	}
	return r.DNSClient.Apply(ctx, operations, r.Config.DryRun)
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
