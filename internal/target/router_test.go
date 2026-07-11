package target

import (
	"strings"
	"testing"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func TestRouteEndpointsUsesNormalizedDomainsNamespacesSourcesAndTargetZones(t *testing.T) {
	definitions := independentDefinitions()
	definitions[0].Namespaces = []string{"team-a"}
	definitions[0].Sources = []string{"service"}
	definitions[1].Namespaces = []string{"team-b"}
	definitions[1].Sources = []string{"ingress"}
	endpoints := []dns.Endpoint{
		{DNSName: "API.LEFT.EXAMPLE.COM.", RecordType: dns.RecordA, Targets: []string{"192.0.2.1"}, TTL: 300, Source: dns.SourceRef{Kind: "Service", Namespace: "team-a"}},
		{DNSName: "web.right.example.net", RecordType: dns.RecordA, Targets: []string{"192.0.2.2"}, TTL: 300, Source: dns.SourceRef{Kind: "Ingress", Namespace: "team-b"}},
		{DNSName: "ignored.left.example.com", RecordType: dns.RecordA, Targets: []string{"192.0.2.3"}, TTL: 300, Source: dns.SourceRef{Kind: "Ingress", Namespace: "team-a"}},
	}
	result, err := RouteEndpoints(definitions, endpoints)
	if err != nil {
		t.Fatalf("RouteEndpoints() error = %v", err)
	}
	if len(result.TargetOrder) != 2 || result.TargetOrder[0] != definitions[0].Key() || result.TargetOrder[1] != definitions[1].Key() {
		t.Fatalf("target order = %v", result.TargetOrder)
	}
	if got := result.ByTarget[definitions[0].Key()]; len(got) != 1 || got[0].Zone != definitions[0].Zone || got[0].DNSName != "api.left.example.com" {
		t.Fatalf("left route = %#v", got)
	}
	if got := result.ByTarget[definitions[1].Key()]; len(got) != 1 || got[0].Zone != definitions[1].Zone {
		t.Fatalf("right route = %#v", got)
	}
	if len(result.Unrouted) != 1 || result.Unrouted[0].DNSName != "ignored.left.example.com" {
		t.Fatalf("unrouted = %#v", result.Unrouted)
	}
}

func TestRouteEndpointsRejectsAmbiguousWriteTargetsBeforeMutation(t *testing.T) {
	left := FromAPI(ptr(apiTarget("left", "example.com", []string{"example.com"})))
	right := FromAPI(ptr(apiTarget("right", "apps.example.com", []string{"apps.example.com"})))
	_, err := RouteEndpoints([]Definition{left, right}, []dns.Endpoint{{DNSName: "api.apps.example.com", RecordType: dns.RecordA, Targets: []string{"192.0.2.1"}, TTL: 300}})
	if err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("ambiguous write route error = %v", err)
	}
}

func TestRouteEndpointsAllowsExplicitAcknowledgedKeepFanout(t *testing.T) {
	left := FromAPI(ptr(apiTarget("left", "example.com", []string{"example.com"})))
	right := FromAPI(ptr(apiTarget("right", "apps.example.com", []string{"apps.example.com"})))
	left.CleanupPolicy = v1alpha1.CleanupPolicyKeep
	right.CleanupPolicy = v1alpha1.CleanupPolicyKeep
	left.AllowNonDestructiveOverlap = true
	right.AllowNonDestructiveOverlap = true
	result, err := RouteEndpoints([]Definition{right, left}, []dns.Endpoint{{DNSName: "api.apps.example.com", RecordType: dns.RecordA, Targets: []string{"192.0.2.1"}, TTL: 300}})
	if err != nil {
		t.Fatalf("RouteEndpoints() error = %v", err)
	}
	if len(result.TargetOrder) != 2 || len(result.ByTarget[left.Key()]) != 1 || len(result.ByTarget[right.Key()]) != 1 {
		t.Fatalf("acknowledged fanout = %#v", result)
	}
}

func TestRouteEndpointsIsDeterministicAndDeduplicated(t *testing.T) {
	definitions := independentDefinitions()
	endpoint := dns.Endpoint{DNSName: "api.left.example.com", RecordType: dns.RecordA, Targets: []string{"192.0.2.1"}, TTL: 300}
	first, err := RouteEndpoints(definitions, []dns.Endpoint{endpoint, endpoint})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RouteEndpoints([]Definition{definitions[1], definitions[0]}, []dns.Endpoint{endpoint})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ByTarget[definitions[0].Key()]) != 1 || strings.Join(first.TargetOrder, ",") != strings.Join(second.TargetOrder, ",") {
		t.Fatalf("non-deterministic routing: %#v %#v", first, second)
	}
}

func TestGatewaySourceIncludesHTTPRouteEndpoints(t *testing.T) {
	definition := independentDefinitions()[0]
	definition.Sources = []string{"gateway"}
	result, err := RouteEndpoints([]Definition{definition}, []dns.Endpoint{{
		DNSName: "route.left.example.com", RecordType: dns.RecordA, Targets: []string{"192.0.2.5"}, TTL: 300,
		Source: dns.SourceRef{Kind: "HTTPRoute", Namespace: "team-a"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ByTarget[definition.Key()]) != 1 {
		t.Fatalf("HTTPRoute was not selected by gateway source: %#v", result)
	}
}
