package target

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/config"
)

func TestLegacyConfigurationSynthesizesOneDefaultTarget(t *testing.T) {
	cfg := legacyConfig()
	definitions, err := BuildDefinitions(cfg, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 {
		t.Fatalf("definitions = %#v", definitions)
	}
	target := definitions[0]
	if target.Name != DefaultName || !target.Legacy || target.URL != cfg.FortiGate.BaseURL || target.APIToken != cfg.FortiGate.APIToken || target.Zone != "example.com" {
		t.Fatalf("legacy target changed settings: %#v", target)
	}
	if target.OwnershipMode != v1alpha1.OwnershipModeExclusive || target.CleanupPolicy != v1alpha1.CleanupPolicyDelete || target.ApprovalMode != v1alpha1.ApprovalModeDisabled {
		t.Fatalf("legacy compatibility modes changed: %#v", target)
	}
}

func TestConfigurationAuthoritiesAreMutuallyExclusive(t *testing.T) {
	object := apiTarget("edge", "example.com", []string{"example.com"})
	if _, err := BuildDefinitions(legacyConfig(), true, []v1alpha1.FortiGateDNSTarget{object}); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("direct credentials plus CRD mode should fail, got %v", err)
	} else if strings.Contains(err.Error(), "unit-test-token") {
		t.Fatalf("mutual exclusion error leaked the token: %v", err)
	}
	if _, err := BuildDefinitions(config.Config{}, false, []v1alpha1.FortiGateDNSTarget{object}); err == nil {
		t.Fatal("CRD objects with disabled target mode unexpectedly accepted")
	}

	definitions, err := BuildDefinitions(config.Config{}, true, []v1alpha1.FortiGateDNSTarget{object})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || definitions[0].Legacy || definitions[0].APITokenSecretRef == nil {
		t.Fatalf("CRD target was not loaded: %#v", definitions)
	}
}

func TestWriteScopeOverlapRejectsParentChildAndExactSuffixes(t *testing.T) {
	cases := []struct{ left, right string }{
		{"example.com", "apps.example.com"},
		{"apps.example.com", "example.com"},
		{"example.com", "example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.left+"_"+tc.right, func(t *testing.T) {
			left := FromAPI(ptr(apiTarget("left", "example.com", []string{tc.left})))
			right := FromAPI(ptr(apiTarget("right", "example.com", []string{tc.right})))
			if err := ValidateAll([]Definition{left, right}); err == nil || !strings.Contains(err.Error(), "overlapping") {
				t.Fatalf("overlap unexpectedly accepted: %v", err)
			}
		})
	}
}

func TestDryRunAndAcknowledgedKeepTargetsDoNotConflict(t *testing.T) {
	left := FromAPI(ptr(apiTarget("left", "example.com", []string{"example.com"})))
	right := FromAPI(ptr(apiTarget("right", "apps.example.com", []string{"apps.example.com"})))
	right.DryRun = true
	if err := ValidateAll([]Definition{left, right}); err != nil {
		t.Fatalf("dry-run target should not create a write overlap: %v", err)
	}

	right.DryRun = false
	left.CleanupPolicy = v1alpha1.CleanupPolicyKeep
	right.CleanupPolicy = v1alpha1.CleanupPolicyKeep
	left.AllowNonDestructiveOverlap = true
	right.AllowNonDestructiveOverlap = true
	if err := ValidateAll([]Definition{left, right}); err != nil {
		t.Fatalf("acknowledged keep overlap should be allowed: %v", err)
	}
}

func TestTargetValidationFailsClosedAndSanitizesURL(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Definition)
	}{
		{"userinfo", func(d *Definition) { d.URL = "https://user:password@fortigate.example.com" }},
		{"query", func(d *Definition) { d.URL = "https://fortigate.example.com?token=secret" }},
		{"scheme", func(d *Definition) { d.URL = "ftp://fortigate.example.com" }},
		{"ownership", func(d *Definition) { d.OwnershipMode = "unknown" }},
		{"secret", func(d *Definition) { d.APITokenSecretRef = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			definition := FromAPI(ptr(apiTarget("edge", "example.com", nil)))
			tc.mutate(&definition)
			err := ValidateAll([]Definition{definition})
			if err == nil {
				t.Fatal("unsafe target unexpectedly accepted")
			}
			for _, secret := range []string{"password", "token=secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("validation error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestFromAPIDoesNotAliasMutableConfiguration(t *testing.T) {
	optional := false
	object := apiTarget("edge", "example.com", []string{"example.com"})
	object.Spec.APITokenSecretRef.Optional = &optional
	object.Spec.CARef = &v1alpha1.LocalKeyReference{Kind: "Secret", Name: "ca", Key: "ca.crt"}
	definition := FromAPI(&object)
	object.Spec.DomainFilters[0] = "changed.example.com"
	*object.Spec.APITokenSecretRef.Optional = true
	object.Spec.CARef.Name = "changed"
	if definition.DomainFilters[0] != "example.com" || *definition.APITokenSecretRef.Optional || definition.CARef.Name != "ca" {
		t.Fatalf("target definition aliases API object: %#v", definition)
	}
}

func legacyConfig() config.Config {
	return config.Config{
		DryRun:        false,
		OwnerID:       "cluster-a",
		Sources:       []string{"service", "ingress", "gateway"},
		DomainFilters: []string{"example.com"},
		CleanupPolicy: "delete",
		FortiGate: config.FortiGateConfig{
			BaseURL: "https://fortigate.example.com", APIToken: "unit-test-token", VDOM: "root", Zone: "Example.COM.", ExclusiveZoneOwnership: true,
		},
	}
}

func apiTarget(name, zone string, domains []string) v1alpha1.FortiGateDNSTarget {
	return v1alpha1.FortiGateDNSTarget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dns-system"},
		Spec: v1alpha1.FortiGateDNSTargetSpec{
			URL: "https://fortigate.example.com", Zone: zone,
			APITokenSecretRef: corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "fortigate-token"}, Key: "api-token"},
			OwnershipMode:     v1alpha1.OwnershipModeShared, ControllerID: "cluster-a",
			DomainFilters: domains, CleanupPolicy: v1alpha1.CleanupPolicyKeep,
		},
	}
}

func ptr(value v1alpha1.FortiGateDNSTarget) *v1alpha1.FortiGateDNSTarget { return &value }
