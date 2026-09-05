package policy

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func TestRestrictiveIntersectionAndOptIn(t *testing.T) {
	evaluator, err := NewEvaluator(Bounds{
		HostnameSuffixes: []string{"example.com"},
		TTL:              &v1alpha1.TTLRange{Minimum: 30, Maximum: 600},
	}, []NamedPolicy{
		{
			Namespace: "apps", Name: "team",
			Spec: v1alpha1.FortiGateDNSPolicySpec{
				Selector:                &metav1.LabelSelector{MatchLabels: map[string]string{"team": "edge"}},
				SourceKinds:             []string{"Service"},
				AllowedHostnameSuffixes: []string{"apps.example.com"},
				TTL:                     &v1alpha1.TTLRange{Minimum: 60, Maximum: 300},
				AllowedTargetCIDRs:      []string{"203.0.113.0/24"},
				RequireOptIn:            &v1alpha1.OptInRequirement{Annotation: "dns.example.com/publish", Value: "true"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	allowed := candidate("api.apps.example.com", "203.0.113.10", 120, "apps", "Service", "api")
	allowed.Labels = map[string]string{"team": "edge"}
	allowed.Annotations = map[string]string{"dns.example.com/publish": "true"}
	result := evaluator.Evaluate([]Candidate{allowed})
	if len(result.Allowed) != 1 || len(result.Rejected) != 0 {
		t.Fatalf("allowed candidate result = %#v", result)
	}

	cases := []struct {
		name   string
		mutate func(*Candidate)
		reason Reason
	}{
		{"hostname", func(c *Candidate) { c.Endpoint.DNSName = "api.other.example.com" }, ReasonHostnameNotAllowed},
		{"ttl", func(c *Candidate) { c.Endpoint.TTL = 30 }, ReasonTTLNotAllowed},
		{"target", func(c *Candidate) { c.Endpoint.Targets = []string{"198.51.100.1"} }, ReasonTargetNotAllowed},
		{"opt-in", func(c *Candidate) { c.Annotations = nil }, ReasonOptInRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := allowed
			value.Endpoint.Targets = append([]string(nil), allowed.Endpoint.Targets...)
			value.Labels = cloneMap(allowed.Labels)
			value.Annotations = cloneMap(allowed.Annotations)
			tc.mutate(&value)
			result := evaluator.Evaluate([]Candidate{value})
			if len(result.Rejected) != 1 || result.Rejected[0].Reason != tc.reason {
				t.Fatalf("result = %#v, want reason %s", result, tc.reason)
			}
		})
	}
}

func TestDenyPrecedence(t *testing.T) {
	evaluator, err := NewEvaluator(Bounds{}, []NamedPolicy{
		{Namespace: "apps", Name: "allow", Spec: v1alpha1.FortiGateDNSPolicySpec{AllowedHostnameSuffixes: []string{"example.com"}}},
		{Namespace: "apps", Name: "deny", Spec: v1alpha1.FortiGateDNSPolicySpec{Deny: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := evaluator.Evaluate([]Candidate{candidate("api.example.com", "203.0.113.10", 60, "apps", "Service", "api")})
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != ReasonDenied {
		t.Fatalf("deny did not win: %#v", result)
	}
}

func TestHostnameTargetConstraintRejectsOutsideSuffix(t *testing.T) {
	evaluator, err := NewEvaluator(Bounds{}, []NamedPolicy{{
		Namespace: "apps", Name: "hostname-target",
		Spec: v1alpha1.FortiGateDNSPolicySpec{AllowedTargetSuffixes: []string{"lb.example.net"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	allowed := candidate("alias.example.com", "edge.lb.example.net", 60, "apps", "Service", "allowed")
	rejected := candidate("other.example.com", "outside.example.org", 60, "apps", "Service", "rejected")
	result := evaluator.Evaluate([]Candidate{rejected, allowed})
	if len(result.Allowed) != 1 || result.Allowed[0].Endpoint.Source.Name != "allowed" {
		t.Fatalf("allowed hostname targets = %#v", result.Allowed)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != ReasonTargetNotAllowed {
		t.Fatalf("rejected hostname targets = %#v", result.Rejected)
	}
}

func TestOuterBoundsCannotBeWidened(t *testing.T) {
	evaluator, err := NewEvaluator(Bounds{
		SourceKinds:      []string{"Service"},
		HostnameSuffixes: []string{"internal.example.com"},
	}, []NamedPolicy{{
		Namespace: "apps", Name: "wider",
		Spec: v1alpha1.FortiGateDNSPolicySpec{
			SourceKinds:             []string{"Ingress"},
			AllowedHostnameSuffixes: []string{"example.com"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	candidates := []Candidate{
		candidate("public.example.com", "203.0.113.10", 60, "apps", "Service", "public"),
		candidate("api.internal.example.com", "203.0.113.11", 60, "apps", "Ingress", "api"),
	}
	result := evaluator.Evaluate(candidates)
	if len(result.Allowed) != 0 || len(result.Rejected) != 2 {
		t.Fatalf("outer bounds were widened: %#v", result)
	}
}

func TestQuotaSelectionIsDeterministic(t *testing.T) {
	evaluator, err := NewEvaluator(Bounds{}, []NamedPolicy{{
		Namespace: "apps", Name: "quota",
		Spec: v1alpha1.FortiGateDNSPolicySpec{MaxRecordsPerNamespace: 2, MaxRecordsPerTarget: 2},
	}})
	if err != nil {
		t.Fatal(err)
	}
	values := []Candidate{
		candidate("c.example.com", "203.0.113.3", 60, "apps", "Service", "c"),
		candidate("a.example.com", "203.0.113.1", 60, "apps", "Service", "a"),
		candidate("b.example.com", "203.0.113.2", 60, "apps", "Service", "b"),
	}
	forward := evaluator.Evaluate(values)
	reverse := evaluator.Evaluate([]Candidate{values[2], values[1], values[0]})
	if !reflect.DeepEqual(candidateKeys(forward.Allowed), candidateKeys(reverse.Allowed)) ||
		!reflect.DeepEqual(rejectionKeys(forward.Rejected), rejectionKeys(reverse.Rejected)) {
		t.Fatalf("quota depends on input order:\nforward=%#v\nreverse=%#v", forward, reverse)
	}
	if got := candidateKeys(forward.Allowed); !reflect.DeepEqual(got, []string{"a.example.com", "b.example.com"}) {
		t.Fatalf("allowed = %#v", got)
	}
	if len(forward.Rejected) != 1 || forward.Rejected[0].Reason != ReasonNamespaceQuotaExceeded {
		t.Fatalf("rejected = %#v", forward.Rejected)
	}
}

func TestInvalidPolicyStateReturnsAnError(t *testing.T) {
	tests := []NamedPolicy{
		{Namespace: "apps", Name: "cidr", Spec: v1alpha1.FortiGateDNSPolicySpec{AllowedTargetCIDRs: []string{"not-a-cidr"}}},
		{Namespace: "apps", Name: "ttl", Spec: v1alpha1.FortiGateDNSPolicySpec{TTL: &v1alpha1.TTLRange{Minimum: 300, Maximum: 60}}},
		{Namespace: "apps", Name: "selector", Spec: v1alpha1.FortiGateDNSPolicySpec{Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "team", Operator: "Invalid"}}}}},
	}
	for _, policy := range tests {
		t.Run(policy.Name, func(t *testing.T) {
			if _, err := NewEvaluator(Bounds{}, []NamedPolicy{policy}); err == nil {
				t.Fatal("invalid policy unexpectedly compiled")
			}
		})
	}
}

func TestNilEvaluatorPreservesCompatibility(t *testing.T) {
	values := []Candidate{candidate("api.example.com", "203.0.113.10", 60, "apps", "Service", "api")}
	result := (*Evaluator)(nil).Evaluate(values)
	if !reflect.DeepEqual(result.Allowed, values) || len(result.Rejected) != 0 {
		t.Fatalf("nil evaluator result = %#v", result)
	}
}

func TestGatewaySourceAllowsGatewayAndHTTPRouteObjects(t *testing.T) {
	evaluator, err := NewEvaluator(Bounds{SourceKinds: []string{"gateway"}}, []NamedPolicy{{
		Namespace: "apps", Name: "gateway", Spec: v1alpha1.FortiGateDNSPolicySpec{SourceKinds: []string{"gateway"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result := evaluator.Evaluate([]Candidate{
		candidate("gateway.example.com", "203.0.113.10", 60, "apps", "Gateway", "public"),
		candidate("route.example.com", "203.0.113.10", 60, "apps", "HTTPRoute", "api"),
	})
	if len(result.Allowed) != 2 || len(result.Rejected) != 0 {
		t.Fatalf("gateway source result = %#v", result)
	}
}

func TestGatewayPolicyKindDoesNotSelectHTTPRoutes(t *testing.T) {
	evaluator, err := NewEvaluator(Bounds{SourceKinds: []string{"gateway"}}, []NamedPolicy{{
		Namespace: "apps", Name: "deny-gateways", Spec: v1alpha1.FortiGateDNSPolicySpec{SourceKinds: []string{"Gateway"}, Deny: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result := evaluator.Evaluate([]Candidate{
		candidate("gateway.example.com", "192.0.2.1", 60, "apps", "Gateway", "edge"),
		candidate("route.example.com", "192.0.2.1", 60, "apps", "HTTPRoute", "route"),
	})
	if len(result.Allowed) != 1 || result.Allowed[0].Endpoint.Source.Kind != "HTTPRoute" || len(result.Rejected) != 1 || result.Rejected[0].Candidate.Endpoint.Source.Kind != "Gateway" {
		t.Fatalf("policy CR kind selector changed meaning: %#v", result)
	}
}

func candidate(hostname, target string, ttl int64, namespace, kind, name string) Candidate {
	return Candidate{
		TargetName: "edge",
		Endpoint: dns.Endpoint{
			DNSName: hostname, RecordType: dns.RecordTypeForTarget(target), Targets: []string{target}, TTL: ttl, Zone: "example.com",
			Source: dns.SourceRef{Namespace: namespace, Kind: kind, Name: name},
		},
	}
}

func cloneMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func candidateKeys(values []Candidate) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.Endpoint.DNSName
	}
	return result
}

func rejectionKeys(values []Rejection) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.Candidate.Endpoint.DNSName + ":" + string(value.Reason)
	}
	return result
}
