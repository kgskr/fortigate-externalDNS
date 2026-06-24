package dns

import (
	"testing"
)

func TestNormalizeDoesNotMutateCallerTargets(t *testing.T) {
	original := []string{"2.2.2.2", "1.1.1.1"}
	endpoint := Endpoint{
		DNSName:    "App.Example.com.",
		RecordType: "a",
		Targets:    original,
		Zone:       "Example.com",
	}

	endpoint.Normalize()

	if original[0] != "2.2.2.2" || original[1] != "1.1.1.1" {
		t.Fatalf("Normalize mutated caller slice: %v", original)
	}
}

func TestNormalizeReturnsCanonicalCopy(t *testing.T) {
	endpoint := Endpoint{
		DNSName:    "App.Example.com.",
		RecordType: "a",
		Targets:    []string{"2.2.2.2", "1.1.1.1"},
		Zone:       "Example.com.",
	}

	got := endpoint.Normalize()

	if got.DNSName != "app.example.com" {
		t.Errorf("DNSName = %q, want app.example.com", got.DNSName)
	}
	if got.RecordType != "A" {
		t.Errorf("RecordType = %q, want A", got.RecordType)
	}
	if got.Zone != "example.com" {
		t.Errorf("Zone = %q, want example.com", got.Zone)
	}
	if got.Targets[0] != "1.1.1.1" || got.Targets[1] != "2.2.2.2" {
		t.Errorf("Targets = %v, want sorted [1.1.1.1 2.2.2.2]", got.Targets)
	}
}

func TestKeyDiffersByTargetButLogicalKeyDoesNot(t *testing.T) {
	base := Endpoint{DNSName: "app.example.com", RecordType: "A", Zone: "example.com"}
	a := base
	a.Targets = []string{"1.1.1.1"}
	b := base
	b.Targets = []string{"2.2.2.2"}

	if a.Key() == b.Key() {
		t.Errorf("expected distinct Key for different targets, both %q", a.Key())
	}
	if a.LogicalKey() != b.LogicalKey() {
		t.Errorf("expected same LogicalKey, got %q and %q", a.LogicalKey(), b.LogicalKey())
	}
}

func TestBuildEndpointsMultipleTargetsRemainDistinct(t *testing.T) {
	endpoints := BuildEndpoints("app.example.com", []string{"1.1.1.1", "2.2.2.2"}, 300, "example.com", "owner", SourceRef{Kind: "Service"})

	if len(endpoints) != 2 {
		t.Fatalf("expected 2 distinct endpoints, got %d: %+v", len(endpoints), endpoints)
	}
	if endpoints[0].Key() == endpoints[1].Key() {
		t.Errorf("expected distinct keys, both %q", endpoints[0].Key())
	}
	for _, e := range endpoints {
		if e.RecordType != RecordA {
			t.Errorf("RecordType = %q, want A", e.RecordType)
		}
	}
}

func TestBuildEndpointsDeduplicatesTargets(t *testing.T) {
	endpoints := BuildEndpoints("app.example.com", []string{"1.1.1.1", "1.1.1.1", " "}, 300, "example.com", "owner", SourceRef{})
	if len(endpoints) != 1 {
		t.Fatalf("expected 1 endpoint after dedupe, got %d", len(endpoints))
	}
}

func TestRecordTypeForTarget(t *testing.T) {
	cases := map[string]string{
		"1.1.1.1":          RecordA,
		"2001:db8::1":      RecordAAAA,
		"target.other.com": RecordCNAME,
	}
	for target, want := range cases {
		if got := RecordTypeForTarget(target); got != want {
			t.Errorf("RecordTypeForTarget(%q) = %q, want %q", target, got, want)
		}
	}
}

func TestEqualRecordComparesAllTargetsAndFields(t *testing.T) {
	a := Endpoint{DNSName: "app.example.com", RecordType: "A", Targets: []string{"1.1.1.1"}, TTL: 300, Zone: "example.com"}
	same := a
	if !a.EqualRecord(same) {
		t.Error("identical records should be equal")
	}
	diffTTL := a
	diffTTL.TTL = 60
	if a.EqualRecord(diffTTL) {
		t.Error("records with different TTL should not be equal")
	}
	diffTarget := a
	diffTarget.Targets = []string{"2.2.2.2"}
	if a.EqualRecord(diffTarget) {
		t.Error("records with different target should not be equal")
	}
	diffDisabled := a
	diffDisabled.Disabled = true
	if a.EqualRecord(diffDisabled) {
		t.Error("records with different Disabled should not be equal")
	}
}
