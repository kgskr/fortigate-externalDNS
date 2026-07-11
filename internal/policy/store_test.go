package policy

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func TestDynamicProviderListsOnlyConfiguredNamespacesDeterministically(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := []runtime.Object{
		policyObject("team-b", "z-policy", v1alpha1.FortiGateDNSPolicySpec{Deny: true}),
		policyObject("team-a", "b-policy", v1alpha1.FortiGateDNSPolicySpec{AllowedHostnameSuffixes: []string{"example.com"}}),
		policyObject("team-a", "a-policy", v1alpha1.FortiGateDNSPolicySpec{TTL: &v1alpha1.TTLRange{Minimum: 60, Maximum: 600}}),
	}
	provider, err := NewDynamicProvider(dynamicfake.NewSimpleDynamicClient(scheme, objects...))
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := provider.Evaluator(context.Background(), []string{"team-a", "team-a", " "}, Bounds{})
	if err != nil {
		t.Fatal(err)
	}
	result := evaluator.Evaluate([]Candidate{
		{Endpoint: dns.Endpoint{DNSName: "web.example.com", RecordType: dns.RecordA, Targets: []string{"203.0.113.10"}, TTL: 300, Source: dns.SourceRef{Kind: "Service", Namespace: "team-a", Name: "web"}}},
		{Endpoint: dns.Endpoint{DNSName: "api.example.com", RecordType: dns.RecordA, Targets: []string{"203.0.113.11"}, TTL: 300, Source: dns.SourceRef{Kind: "Service", Namespace: "team-b", Name: "api"}}},
	})
	if len(result.Allowed) != 2 || len(result.Rejected) != 0 {
		t.Fatalf("configured namespace policy result = %#v", result)
	}
}

func TestDynamicProviderFailsClosedOnInvalidPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	provider, err := NewDynamicProvider(dynamicfake.NewSimpleDynamicClient(scheme,
		policyObject("apps", "invalid", v1alpha1.FortiGateDNSPolicySpec{AllowedTargetCIDRs: []string{"not-a-cidr"}}),
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Evaluator(context.Background(), []string{"apps"}, Bounds{}); err == nil {
		t.Fatal("invalid policy must make the policy snapshot incomplete")
	}
}

func policyObject(namespace, name string, spec v1alpha1.FortiGateDNSPolicySpec) *v1alpha1.FortiGateDNSPolicy {
	return &v1alpha1.FortiGateDNSPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: "FortiGateDNSPolicy"},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       spec,
	}
}
