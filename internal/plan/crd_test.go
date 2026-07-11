package plan

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func TestChangePlanObjectRoundTripBindsCanonicalDocumentAndSummaries(t *testing.T) {
	document := NewDocument(
		TargetIdentity{Name: "edge", Generation: 4, VDOM: "root", Zone: "example.com"},
		Preconditions{
			Provider:  ProviderPrecondition{Revision: "revision-7", Stable: true, Complete: true},
			Discovery: DiscoveryPrecondition{Generation: 11, Complete: true},
			Policy:    PolicyPrecondition{Generation: 3, Complete: true},
			Ownership: []OwnershipPrecondition{{Namespace: "dns-system", Name: "claim-a", ResourceVersion: "42", Fingerprint: strings.Repeat("a", 64), Phase: "Confirmed"}},
		},
		[]Operation{{
			Type:    OperationCreate,
			Desired: dns.Endpoint{Zone: "example.com", DNSName: "api.example.com", RecordType: dns.RecordA, Targets: []string{"203.0.113.10"}, TTL: 300},
			Reason:  "record is missing",
		}},
	)
	expires := metav1.Now()
	object, err := NewChangePlanObject("dns-system", "edge-plan", document, &expires)
	if err != nil {
		t.Fatal(err)
	}
	if object.Spec.PlanHash == "" || object.Spec.CanonicalDocument == "" || len(object.Spec.Operations) != 1 {
		t.Fatalf("incomplete persisted plan: %#v", object.Spec)
	}
	if object.Spec.OwnershipResourceVersions["dns-system/claim-a"] != "42" {
		t.Fatalf("ownership summary = %#v", object.Spec.OwnershipResourceVersions)
	}
	roundTrip, err := DocumentFromChangePlan(object)
	if err != nil {
		t.Fatal(err)
	}
	wantID, _ := document.ID()
	gotID, _ := roundTrip.ID()
	if gotID != wantID {
		t.Fatalf("round-trip ID = %s, want %s", gotID, wantID)
	}
}

func TestChangePlanObjectRejectsTamperedHashDocumentAndSummaries(t *testing.T) {
	document := NewDocument(
		TargetIdentity{Name: "edge", Zone: "example.com"},
		Preconditions{Provider: ProviderPrecondition{Revision: "rev", Stable: true, Complete: true}},
		[]Operation{{Type: OperationDelete, Current: dns.Endpoint{Zone: "example.com", DNSName: "old.example.com", RecordType: dns.RecordA, Targets: []string{"203.0.113.20"}, TTL: 300, ProviderID: "7"}, Reason: "record is stale"}},
	)
	base, err := NewChangePlanObject("dns-system", "edge-plan", document, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(){
		"hash":      func() { base.Spec.PlanHash = strings.Repeat("0", 64) },
		"document":  func() { base.Spec.CanonicalDocument += " " },
		"provider":  func() { base.Spec.ProviderRevision = "changed" },
		"operation": func() { base.Spec.Operations[0].ProviderID = "different" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			object, err := NewChangePlanObject("dns-system", "edge-plan", document, nil)
			if err != nil {
				t.Fatal(err)
			}
			base = object
			mutate()
			if _, err := DocumentFromChangePlan(base); err == nil {
				t.Fatal("tampered plan unexpectedly loaded")
			}
		})
	}
}

func TestChangePlanObjectRejectsMultiTargetProviderOperation(t *testing.T) {
	document := NewDocument(
		TargetIdentity{Name: "edge", Zone: "example.com"},
		Preconditions{},
		[]Operation{{Type: OperationCreate, Desired: dns.Endpoint{Zone: "example.com", DNSName: "api.example.com", RecordType: dns.RecordA, Targets: []string{"203.0.113.10", "203.0.113.11"}, TTL: 300}}},
	)
	if _, err := NewChangePlanObject("dns-system", "edge-plan", document, nil); err == nil {
		t.Fatal("multi-target provider operation unexpectedly persisted")
	}
}
