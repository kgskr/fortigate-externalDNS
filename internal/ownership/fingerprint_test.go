package ownership

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func TestIdentityAndFingerprintAreNormalizedAndStable(t *testing.T) {
	first := dns.Endpoint{
		Zone: "Example.COM.", DNSName: "API.Example.COM.", RecordType: "a",
		Targets: []string{"203.0.113.010"}, TTL: 300,
	}
	// net.ParseIP deliberately rejects the leading-zero spelling, so compare a
	// second input that differs only in DNS case/trailing dots and target space.
	first.Targets = []string{" 203.0.113.10 "}
	second := dns.Endpoint{
		Zone: "example.com", DNSName: "api.example.com", RecordType: "A",
		Targets: []string{"203.0.113.10"}, TTL: 300,
	}
	identityA, err := IdentityFor(" EDGE ", first)
	if err != nil {
		t.Fatal(err)
	}
	identityB, err := IdentityFor("edge", second)
	if err != nil {
		t.Fatal(err)
	}
	if identityA != identityB {
		t.Fatalf("normalized identities differ: %#v != %#v", identityA, identityB)
	}
	fingerprintA, err := Fingerprint(" EDGE ", first)
	if err != nil {
		t.Fatal(err)
	}
	fingerprintB, err := Fingerprint("edge", second)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprintA != fingerprintB || len(fingerprintA) != 64 {
		t.Fatalf("fingerprints differ or are malformed: %q %q", fingerprintA, fingerprintB)
	}
	if name := ClaimName(identityA); len(name) != 63 || !strings.HasPrefix(name, "record-") {
		t.Fatalf("claim name = %q (length %d)", name, len(name))
	}
}

func TestFingerprintCoversTTLStatusAndTarget(t *testing.T) {
	base := dns.Endpoint{
		Zone: "example.com", DNSName: "api.example.com", RecordType: dns.RecordA,
		Targets: []string{"203.0.113.10"}, TTL: 300,
	}
	values := map[string]string{}
	for name, endpoint := range map[string]dns.Endpoint{
		"base":     base,
		"ttl":      withTTL(base, 301),
		"disabled": withDisabled(base, true),
		"target":   withTarget(base, "203.0.113.11"),
	} {
		value, err := Fingerprint("edge", endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if prior, duplicate := values[value]; duplicate {
			t.Fatalf("%s and %s have the same fingerprint", prior, name)
		}
		values[value] = name
	}
}

func TestClaimMatchesEveryIdentityField(t *testing.T) {
	endpoint := dns.Endpoint{
		Zone: "example.com", DNSName: "api.example.com", RecordType: dns.RecordA,
		Targets: []string{"203.0.113.10"}, TTL: 300,
	}
	identity, err := IdentityFor("edge", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := Fingerprint("edge", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	claim := &v1alpha1.FortiGateDNSRecordOwnership{
		ObjectMeta: metav1.ObjectMeta{Name: ClaimName(identity)},
		Spec: v1alpha1.FortiGateDNSRecordOwnershipSpec{
			TargetRef: corev1.LocalObjectReference{Name: "edge"},
			Record:    RecordKey(identity), Fingerprint: fingerprint,
		},
	}
	if !ClaimMatches(claim, "edge", endpoint) {
		t.Fatal("matching claim was rejected")
	}
	claim.Spec.Record.Target = "203.0.113.99"
	if ClaimMatches(claim, "edge", endpoint) {
		t.Fatal("changed claim unexpectedly matched")
	}
}

func TestIdentityRejectsAmbiguousOrUnsupportedRecords(t *testing.T) {
	tests := []dns.Endpoint{
		{Zone: "example.com", DNSName: "api.example.com", RecordType: dns.RecordA},
		{Zone: "example.com", DNSName: "api.example.com", RecordType: dns.RecordA, Targets: []string{"1.1.1.1", "2.2.2.2"}},
		{Zone: "example.com", DNSName: "api.example.com", RecordType: "TXT", Targets: []string{"value"}},
	}
	for _, endpoint := range tests {
		if _, err := IdentityFor("edge", endpoint); err == nil {
			t.Fatalf("invalid endpoint unexpectedly accepted: %#v", endpoint)
		}
	}
	if _, err := IdentityFor("", dns.Endpoint{Zone: "example.com", DNSName: "api.example.com", RecordType: dns.RecordA, Targets: []string{"1.1.1.1"}}); err == nil {
		t.Fatal("empty target name unexpectedly accepted")
	}
}

func withTTL(endpoint dns.Endpoint, ttl int64) dns.Endpoint {
	endpoint.TTL = ttl
	return endpoint
}

func withDisabled(endpoint dns.Endpoint, disabled bool) dns.Endpoint {
	endpoint.Disabled = disabled
	return endpoint
}

func withTarget(endpoint dns.Endpoint, target string) dns.Endpoint {
	endpoint.Targets = []string{target}
	return endpoint
}
