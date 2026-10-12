package source

import (
	"strconv"
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestHTTPRouteMixedParentFreshnessSuppressesCleanup(t *testing.T) {
	route, gateways := routeAcrossGateways([]string{"a.example.com", "b.example.com"}, "203.0.113.50", gatewayv1.IPAddressType)
	opts := testOptions()
	current := EndpointsFromHTTPRoute(route, gateways, opts).Endpoints
	if len(current) != 2 {
		t.Fatalf("initial endpoints = %v", current)
	}
	for i := range current {
		current[i].ProviderID = strconv.Itoa(i + 1)
	}
	route.Generation = 2
	route.Status.Parents[0].Conditions = conds(metav1.ConditionTrue, metav1.ConditionTrue, 2)
	for _, phase := range []string{"stale", "missing", "recovered", "rejected"} {
		t.Run(phase, func(t *testing.T) {
			candidate := route.DeepCopy()
			switch phase {
			case "missing":
				candidate.Status.Parents = candidate.Status.Parents[:1]
			case "recovered":
				candidate.Status.Parents[1].Conditions = conds(metav1.ConditionTrue, metav1.ConditionTrue, 2)
			case "rejected":
				candidate.Status.Parents[1].Conditions = conds(metav1.ConditionFalse, metav1.ConditionTrue, 2)
			}
			result := EndpointsFromHTTPRoute(candidate, gateways, opts)
			incomplete := phase == "stale" || phase == "missing"
			if result.HasIncompleteSources() != incomplete {
				t.Fatalf("incomplete = %v, want %v", result.HasIncompleteSources(), incomplete)
			}
			// Keep a healthy sibling desired so the empty-desired guard cannot
			// mask a missing discovery-completeness guard in the planner.
			sibling := EndpointsFromService(loadBalancerService("sibling", "apps", "sibling.example.com", "203.0.113.60"), opts).Endpoints
			desired := append(sibling, result.Endpoints...)
			operations := plan.BuildWithCleanupScope(desired, current, opts.OwnerID, plan.CleanupDelete, func(dns.Endpoint) bool { return !result.HasIncompleteSources() })
			deletes := 0
			for _, operation := range operations {
				if operation.Type == plan.OperationDelete {
					deletes++
					if phase != "rejected" || operation.Current.DNSName != "b.example.com" {
						t.Fatalf("unexpected cleanup: %s", operation)
					}
				}
			}
			if phase == "rejected" && deletes != 1 {
				t.Fatalf("authoritative rejection must allow cleanup, got %v", operations)
			}
		})
	}
}

func TestHTTPRouteRequiresBothCurrentConditionsForReferencedParents(t *testing.T) {
	parent := gatewayv1.ParentReference{Name: "public"}
	for _, mode := range []string{"stale-accepted", "stale-resolved", "missing-resolved", "unknown-accepted", "unknown-resolved", "unrelated-status", "removed-parent-status"} {
		t.Run(mode, func(t *testing.T) {
			route := routeWithStatus(2, []string{"a.example.com"}, parent, conds(metav1.ConditionTrue, metav1.ConditionTrue, 2))
			conditions := route.Status.Parents[0].Conditions
			switch mode {
			case "stale-accepted":
				conditions[0].ObservedGeneration = 1
			case "stale-resolved":
				conditions[1].ObservedGeneration = 1
			case "missing-resolved":
				route.Status.Parents[0].Conditions = conditions[:1]
			case "unknown-accepted":
				conditions[0].Status = metav1.ConditionUnknown
			case "unknown-resolved":
				conditions[1].Status = metav1.ConditionUnknown
			case "unrelated-status":
				route.Status.Parents[0].ParentRef.Name = "other"
			case "removed-parent-status":
				route.Status.Parents = append(route.Status.Parents, gatewayv1.RouteParentStatus{ParentRef: gatewayv1.ParentReference{Name: "removed"}})
			}
			result := EndpointsFromHTTPRoute(route, gatewayWith(listener("http", "", 80)), testOptions())
			if want := mode != "removed-parent-status"; result.HasIncompleteSources() != want {
				t.Fatalf("incomplete = %v, want %v", result.HasIncompleteSources(), want)
			}
		})
	}
}

func TestStaleOutOfScopeHTTPRouteDoesNotBlockZoneCleanup(t *testing.T) {
	parent := gatewayv1.ParentReference{Name: "public"}
	for _, testCase := range []struct {
		name       string
		host       string
		incomplete bool
	}{
		{name: "other zone", host: "outside.other.com"},
		{name: "zone apex", host: "example.com"},
		{name: "in zone", host: "app.example.com", incomplete: true},
		{name: "wildcard other zone", host: "*.other.com"},
		{name: "wildcard in zone", host: "*.example.com", incomplete: true},
		{name: "wildcard parent zone", host: "*.com", incomplete: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			route := routeWithStatus(2, []string{testCase.host}, parent, conds(metav1.ConditionTrue, metav1.ConditionTrue, 1))
			result := EndpointsFromHTTPRoute(route, gatewayWith(listener("http", "", 80)), testOptions())
			if got := result.HasIncompleteSources(); got != testCase.incomplete {
				t.Fatalf("incomplete = %v, want %v", got, testCase.incomplete)
			}
		})
	}
}
