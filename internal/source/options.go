package source

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

const (
	SourceService = "service"
	SourceIngress = "ingress"
	SourceGateway = "gateway"

	AnnotationHostname      = "external-dns.kubernetes.io/hostname"
	AnnotationHostnameAlpha = "external-dns.alpha.kubernetes.io/hostname"
	AnnotationTTL           = "external-dns.kubernetes.io/ttl"
	AnnotationTTLAlpha      = "external-dns.alpha.kubernetes.io/ttl"

	// MaxTTL bounds a per-resource TTL annotation (7 days), matching the
	// operator-facing default-TTL cap, so a tenant annotation cannot set an
	// absurd value that FortiGate would reject at apply time.
	MaxTTL = 604800
)

type Options struct {
	Sources    []string
	Namespaces []string
	// GatewayTargetNamespaces are additional namespaces consulted only to resolve
	// parent Gateway addresses for HTTPRoutes. They are a read-only lookup scope
	// and never expand which namespaces own or have their stale records cleaned up.
	GatewayTargetNamespaces []string
	DomainFilters           []string
	DefaultTTL              int64
	Zone                    string
	OwnerID                 string
}

type Result struct {
	Endpoints []dns.Endpoint
	Events    []Event
}

const (
	EventWarning = "warning"
	EventInfo    = "info"
)

type Event struct {
	Level    string
	Resource dns.SourceRef
	Message  string
	Hostname string
}

// AddEvent records a warning-level diagnostic for an actionable condition.
func (r *Result) AddEvent(ref dns.SourceRef, hostname string, message string) {
	r.addEvent(EventWarning, ref, hostname, message)
}

// AddInfoEvent records an informational diagnostic for an expected condition so
// it stays observable without being logged as a warning on every reconcile.
func (r *Result) AddInfoEvent(ref dns.SourceRef, hostname string, message string) {
	r.addEvent(EventInfo, ref, hostname, message)
}

func (r *Result) addEvent(level string, ref dns.SourceRef, hostname string, message string) {
	r.Events = append(r.Events, Event{
		Level:    level,
		Resource: ref,
		Message:  message,
		Hostname: dns.NormalizeDNSName(hostname),
	})
}

func (r *Result) Merge(other Result) {
	r.Endpoints = append(r.Endpoints, other.Endpoints...)
	r.Events = append(r.Events, other.Events...)
}

func (o Options) SourceEnabled(name string) bool {
	if len(o.Sources) == 0 {
		return true
	}
	for _, source := range o.Sources {
		if strings.EqualFold(source, name) {
			return true
		}
	}
	return false
}

func (o Options) NamespaceAllowed(namespace string) bool {
	if len(o.Namespaces) == 0 {
		return true
	}
	for _, allowed := range o.Namespaces {
		if allowed == namespace {
			return true
		}
	}
	return false
}

// GatewayTargetNamespaceAllowed reports whether a namespace is an explicitly
// configured Gateway target-lookup namespace. This is independent of
// NamespaceAllowed: it widens only Gateway address resolution, not ownership.
func (o Options) GatewayTargetNamespaceAllowed(namespace string) bool {
	for _, allowed := range o.GatewayTargetNamespaces {
		if allowed == namespace {
			return true
		}
	}
	return false
}

func (o Options) DomainAllowed(hostname string) bool {
	if len(o.DomainFilters) == 0 {
		return true
	}
	name := dns.NormalizeDNSName(hostname)
	for _, filter := range o.DomainFilters {
		filter = dns.NormalizeDNSName(filter)
		if name == filter || strings.HasSuffix(name, "."+filter) {
			return true
		}
	}
	return false
}

// HostInZone reports whether a hostname belongs to the configured FortiGate zone
// (equal to it or a subdomain). A record is always written under the zone-scoped
// dns-database path, so a hostname outside the zone would create a wrong record.
func (o Options) HostInZone(hostname string) bool {
	zone := dns.NormalizeDNSName(o.Zone)
	if zone == "" {
		return false
	}
	name := dns.NormalizeDNSName(hostname)
	return name == zone || strings.HasSuffix(name, "."+zone)
}

func HostnamesFromAnnotations(annotations map[string]string) []string {
	var out []string
	for _, key := range []string{AnnotationHostname, AnnotationHostnameAlpha} {
		value := annotations[key]
		for _, item := range strings.Split(value, ",") {
			item = dns.NormalizeDNSName(item)
			if item != "" {
				out = append(out, item)
			}
		}
	}
	return uniqueSorted(out)
}

func TTLFromAnnotations(annotations map[string]string, defaultTTL int64) (int64, error) {
	for _, key := range []string{AnnotationTTL, AnnotationTTLAlpha} {
		value := strings.TrimSpace(annotations[key])
		if value == "" {
			continue
		}
		ttl, err := strconv.ParseInt(value, 10, 64)
		if err != nil || ttl <= 0 || ttl > MaxTTL {
			return 0, fmt.Errorf("invalid TTL annotation %s=%q (must be 1..%d)", key, value, MaxTTL)
		}
		return ttl, nil
	}
	return defaultTTL, nil
}

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = dns.NormalizeDNSName(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func appendEndpointForHost(result *Result, opts Options, ref dns.SourceRef, host string, targets []string, ttl int64) {
	host = dns.NormalizeDNSName(host)
	if host == "" {
		return
	}
	if !opts.DomainAllowed(host) {
		return
	}
	if host == "*" || strings.HasPrefix(host, "*.") {
		// The FortiGate dns-database hostname field does not accept a leading
		// wildcard label; publishing it would fail or create a malformed entry.
		// Checked after the domain filter so an explicitly-excluded wildcard does
		// not generate a diagnostic.
		result.AddEvent(ref, host, "wildcard hostnames are not supported by the FortiGate dns-database; skipping")
		return
	}
	if !opts.HostInZone(host) {
		// Every record is written under the configured zone's dns-database path,
		// so a hostname outside the zone must not be published into it.
		result.AddEvent(ref, host, "hostname is outside the configured FortiGate zone; skipping")
		return
	}
	if len(targets) == 0 {
		result.AddEvent(ref, host, "resource has a hostname but no publishable target")
		return
	}
	result.Endpoints = append(result.Endpoints, dns.BuildEndpoints(host, targets, ttl, opts.Zone, opts.OwnerID, ref)...)
}
