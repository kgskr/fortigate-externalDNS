package dns

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

const (
	RecordA     = "A"
	RecordAAAA  = "AAAA"
	RecordCNAME = "CNAME"
)

type SourceRef struct {
	Kind      string
	Namespace string
	Name      string
}

func (s SourceRef) String() string {
	if s.Namespace == "" {
		return fmt.Sprintf("%s/%s", s.Kind, s.Name)
	}
	return fmt.Sprintf("%s/%s/%s", s.Kind, s.Namespace, s.Name)
}

type Endpoint struct {
	DNSName    string
	RecordType string
	Targets    []string
	TTL        int64
	Zone       string
	OwnerID    string
	Source     SourceRef
	ProviderID string
	Disabled   bool
}

// Key is the per-entry identity: a single FortiGate dns-entry is one target, so
// the target value participates in identity. Two A targets for the same name are
// distinct entries with distinct keys.
func (e Endpoint) Key() string {
	e = e.Normalize()
	parts := []string{e.Zone, e.DNSName, e.RecordType}
	if len(e.Targets) > 0 {
		parts = append(parts, e.Targets[0])
	}
	return strings.Join(parts, "|")
}

// LogicalKey is the record identity independent of target value: (zone, name,
// type). It is used to detect that a target change is a replacement of an
// existing record rather than an unrelated create plus delete.
func (e Endpoint) LogicalKey() string {
	e = e.Normalize()
	return strings.Join([]string{e.Zone, e.DNSName, e.RecordType}, "|")
}

// Normalize returns a normalized copy. It never mutates the caller's Targets
// slice: the targets are copied before normalization and sorting.
func (e Endpoint) Normalize() Endpoint {
	e.DNSName = NormalizeDNSName(e.DNSName)
	e.RecordType = strings.ToUpper(strings.TrimSpace(e.RecordType))
	e.Zone = NormalizeDNSName(e.Zone)
	if len(e.Targets) > 0 {
		targets := make([]string, len(e.Targets))
		for i := range e.Targets {
			targets[i] = NormalizeTarget(e.Targets[i])
		}
		sort.Strings(targets)
		e.Targets = targets
	}
	return e
}

func (e Endpoint) EqualRecord(other Endpoint) bool {
	e = e.Normalize()
	other = other.Normalize()
	if e.DNSName != other.DNSName || e.RecordType != other.RecordType || e.TTL != other.TTL || e.Disabled != other.Disabled {
		return false
	}
	if len(e.Targets) != len(other.Targets) {
		return false
	}
	for i := range e.Targets {
		if e.Targets[i] != other.Targets[i] {
			return false
		}
	}
	return true
}

func RecordKey(zone, name, recordType string) string {
	return strings.Join([]string{
		NormalizeDNSName(zone),
		NormalizeDNSName(name),
		strings.ToUpper(strings.TrimSpace(recordType)),
	}, "|")
}

func NormalizeDNSName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

func NormalizeTarget(target string) string {
	target = strings.TrimSpace(target)
	if ip := net.ParseIP(target); ip != nil {
		return ip.String()
	}
	return NormalizeDNSName(target)
}

func RecordTypeForTarget(target string) string {
	ip := net.ParseIP(strings.TrimSpace(target))
	if ip == nil {
		return RecordCNAME
	}
	if ip.To4() != nil {
		return RecordA
	}
	return RecordAAAA
}

func BuildEndpoints(hostname string, targets []string, ttl int64, zone string, ownerID string, source SourceRef) []Endpoint {
	seen := map[string]struct{}{}
	var endpoints []Endpoint
	for _, target := range targets {
		target = NormalizeTarget(target)
		if target == "" {
			continue
		}
		recordType := RecordTypeForTarget(target)
		key := RecordKey(zone, hostname, recordType) + "|" + target
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		endpoints = append(endpoints, Endpoint{
			DNSName:    NormalizeDNSName(hostname),
			RecordType: recordType,
			Targets:    []string{target},
			TTL:        ttl,
			Zone:       zone,
			OwnerID:    ownerID,
			Source:     source,
		}.Normalize())
	}
	sort.Slice(endpoints, func(i, j int) bool {
		return endpoints[i].Key() < endpoints[j].Key()
	})
	return endpoints
}
