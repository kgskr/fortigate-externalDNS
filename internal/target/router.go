package target

import (
	"errors"
	"sort"
	"strings"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

var ErrAmbiguousRoute = errors.New("desired endpoint has ambiguous write routing")

type RoutingResult struct {
	TargetOrder []string
	ByTarget    map[string][]dns.Endpoint
	Unrouted    []dns.Endpoint
}

// RouteEndpoints deterministically maps normalized endpoints to every eligible
// target. Multiple write targets are accepted only under the already validated
// explicit non-destructive overlap acknowledgement.
func RouteEndpoints(definitions []Definition, endpoints []dns.Endpoint) (RoutingResult, error) {
	definitions = cloneDefinitions(definitions)
	if err := ValidateAll(definitions); err != nil {
		return RoutingResult{}, err
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Key() < definitions[j].Key() })
	result := RoutingResult{ByTarget: map[string][]dns.Endpoint{}}
	seen := map[string]map[string]struct{}{}

	for _, endpoint := range endpoints {
		endpoint = endpoint.Normalize()
		eligible := make([]Definition, 0, 1)
		for _, definition := range definitions {
			if targetAcceptsEndpoint(definition, endpoint) {
				eligible = append(eligible, definition)
			}
		}
		if len(eligible) == 0 {
			result.Unrouted = append(result.Unrouted, endpoint)
			continue
		}
		for left := range eligible {
			for right := left + 1; right < len(eligible); right++ {
				if eligible[left].DryRun || eligible[right].DryRun || nonDestructiveOverlapAllowed(eligible[left], eligible[right]) {
					continue
				}
				return RoutingResult{}, ErrAmbiguousRoute
			}
		}
		for _, definition := range eligible {
			key := definition.Key()
			routed := endpoint
			routed.Zone = definition.Zone
			routed = routed.Normalize()
			if seen[key] == nil {
				seen[key] = map[string]struct{}{}
			}
			if _, duplicate := seen[key][routed.Key()]; duplicate {
				continue
			}
			seen[key][routed.Key()] = struct{}{}
			result.ByTarget[key] = append(result.ByTarget[key], routed)
		}
	}

	for key, routed := range result.ByTarget {
		result.TargetOrder = append(result.TargetOrder, key)
		sort.Slice(routed, func(i, j int) bool { return routed[i].Key() < routed[j].Key() })
		result.ByTarget[key] = routed
	}
	sort.Strings(result.TargetOrder)
	sort.Slice(result.Unrouted, func(i, j int) bool { return result.Unrouted[i].Key() < result.Unrouted[j].Key() })
	return result, nil
}

func (m *RuntimeManager) Route(endpoints []dns.Endpoint) (RoutingResult, error) {
	return RouteEndpoints(m.Definitions(), endpoints)
}

func targetAcceptsEndpoint(definition Definition, endpoint dns.Endpoint) bool {
	if endpoint.DNSName == "" {
		return false
	}
	acceptedDomain := false
	for _, suffix := range domainScopes(definition) {
		if suffixContains(suffix, endpoint.DNSName) {
			acceptedDomain = true
			break
		}
	}
	if !acceptedDomain {
		return false
	}
	if len(definition.Namespaces) > 0 && !containsNormalized(definition.Namespaces, endpoint.Source.Namespace) {
		return false
	}
	if len(definition.Sources) > 0 && !sourceSelected(definition.Sources, endpoint.Source.Kind) {
		return false
	}
	return true
}

func sourceSelected(configured []string, sourceKind string) bool {
	sourceKind = strings.ToLower(strings.TrimSpace(sourceKind))
	for _, source := range configured {
		source = strings.ToLower(strings.TrimSpace(source))
		if source == sourceKind || (source == "gateway" && (sourceKind == "gateway" || sourceKind == "httproute")) {
			return true
		}
	}
	return false
}

func containsNormalized(values []string, value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range values {
		if strings.ToLower(strings.TrimSpace(candidate)) == value {
			return true
		}
	}
	return false
}
