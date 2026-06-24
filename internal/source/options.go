package source

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gilsu/fortigate-external-dns/internal/dns"
)

const (
	SourceService = "service"
	SourceIngress = "ingress"
	SourceGateway = "gateway"

	AnnotationHostname      = "external-dns.kubernetes.io/hostname"
	AnnotationHostnameAlpha = "external-dns.alpha.kubernetes.io/hostname"
	AnnotationTTL           = "external-dns.kubernetes.io/ttl"
	AnnotationTTLAlpha      = "external-dns.alpha.kubernetes.io/ttl"
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

type Event struct {
	Severity  string
	Resource  dns.SourceRef
	Message   string
	Hostname  string
	Namespace string
}

func (r *Result) AddEvent(severity string, ref dns.SourceRef, hostname string, message string) {
	r.Events = append(r.Events, Event{
		Severity:  severity,
		Resource:  ref,
		Message:   message,
		Hostname:  dns.NormalizeDNSName(hostname),
		Namespace: ref.Namespace,
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
		if err != nil || ttl <= 0 {
			return 0, fmt.Errorf("invalid TTL annotation %s=%q", key, value)
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
	if host == "" || !opts.DomainAllowed(host) {
		return
	}
	if len(targets) == 0 {
		result.AddEvent("warning", ref, host, "resource has a hostname but no publishable target")
		return
	}
	result.Endpoints = append(result.Endpoints, dns.BuildEndpoints(host, targets, ttl, opts.Zone, opts.OwnerID, ref)...)
}
