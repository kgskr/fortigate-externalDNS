package plan

import (
	"strings"
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func TestPlanCreateUpdateDeleteAndConflict(t *testing.T) {
	desired := []dns.Endpoint{
		endpoint("new.example.com", "A", []string{"203.0.113.10"}, "owner"),
		endpoint("changed.example.com", "A", []string{"203.0.113.11"}, "owner"),
		endpoint("conflict.example.com", "A", []string{"203.0.113.12"}, "owner"),
	}
	current := []dns.Endpoint{
		ttlEndpoint("changed.example.com", "A", []string{"203.0.113.11"}, "owner", 30),
		endpoint("stale.example.com", "A", []string{"203.0.113.50"}, "owner"),
		endpoint("conflict.example.com", "A", []string{"203.0.113.12"}, "other"),
	}

	operations := Build(desired, current, "owner", CleanupDelete)
	counts := map[string]int{}
	for _, operation := range operations {
		counts[operation.Type]++
	}
	if counts[OperationCreate] != 1 || counts[OperationUpdate] != 1 || counts[OperationDelete] != 1 || counts[OperationConflict] != 1 {
		t.Fatalf("unexpected operation counts: %#v operations=%#v", counts, operations)
	}
}

func TestPlanIsIdempotent(t *testing.T) {
	desired := []dns.Endpoint{endpoint("same.example.com", "A", []string{"203.0.113.10"}, "owner")}
	current := []dns.Endpoint{endpoint("same.example.com", "A", []string{"203.0.113.10"}, "owner")}
	if got := Build(desired, current, "owner", CleanupDelete); len(got) != 0 {
		t.Fatalf("expected no operations, got %#v", got)
	}
}

func TestDeactivatePolicy(t *testing.T) {
	current := []dns.Endpoint{endpoint("stale.example.com", "A", []string{"203.0.113.50"}, "owner")}
	operations := Build(nil, current, "owner", CleanupDeactivate)
	if len(operations) != 1 || operations[0].Type != OperationDeactivate || !operations[0].Desired.Disabled {
		t.Fatalf("expected deactivate operation, got %#v", operations)
	}
}

func TestFormatDryRunPlan(t *testing.T) {
	operations := []Operation{{Type: OperationCreate, Desired: endpoint("new.example.com", "A", []string{"203.0.113.10"}, "owner"), Reason: "record is missing"}}
	if text := Format(operations); !strings.Contains(text, "create A new.example.com") {
		t.Fatalf("unexpected formatted plan: %s", text)
	}
}

func TestPlanReplacesTargetChangeInPlace(t *testing.T) {
	desired := []dns.Endpoint{endpoint("app.example.com", "A", []string{"2.2.2.2"}, "owner")}
	current := []dns.Endpoint{providerEndpoint("app.example.com", "A", []string{"1.1.1.1"}, "owner", "5")}

	operations := Build(desired, current, "owner", CleanupDelete)

	if len(operations) != 1 {
		t.Fatalf("expected a single replace operation, got %#v", operations)
	}
	op := operations[0]
	if op.Type != OperationReplace {
		t.Fatalf("expected replace, got %q", op.Type)
	}
	if op.Current.ProviderID != "5" {
		t.Errorf("replace must carry the current provider ID, got %q", op.Current.ProviderID)
	}
	if op.Desired.Targets[0] != "2.2.2.2" {
		t.Errorf("replace must target the new value, got %v", op.Desired.Targets)
	}
	for _, o := range operations {
		if o.Type == OperationCreate || o.Type == OperationDelete {
			t.Fatalf("target change must not produce unordered create/delete: %#v", operations)
		}
	}
}

func TestPlanFallsBackToCreateDeleteWithoutProviderID(t *testing.T) {
	desired := []dns.Endpoint{endpoint("app.example.com", "A", []string{"2.2.2.2"}, "owner")}
	current := []dns.Endpoint{endpoint("app.example.com", "A", []string{"1.1.1.1"}, "owner")} // no provider ID

	operations := Build(desired, current, "owner", CleanupDelete)
	counts := map[string]int{}
	for _, op := range operations {
		counts[op.Type]++
	}
	if counts[OperationReplace] != 0 {
		t.Fatalf("must not emit replace without a provider ID: %#v", operations)
	}
	if counts[OperationCreate] != 1 || counts[OperationDelete] != 1 {
		t.Fatalf("expected create+delete fallback, got %#v", counts)
	}
}

func TestPlanMultipleTargetsRemainDistinct(t *testing.T) {
	desired := []dns.Endpoint{
		endpoint("app.example.com", "A", []string{"1.1.1.1"}, "owner"),
		endpoint("app.example.com", "A", []string{"2.2.2.2"}, "owner"),
	}

	operations := Build(desired, nil, "owner", CleanupDelete)
	if len(operations) != 2 {
		t.Fatalf("expected two distinct create operations, got %#v", operations)
	}
	for _, op := range operations {
		if op.Type != OperationCreate {
			t.Fatalf("expected create, got %q", op.Type)
		}
	}
}

func TestPlanCreatesWhenUnownedRecordUsesDifferentType(t *testing.T) {
	desired := []dns.Endpoint{endpoint("app.example.com", "A", []string{"2.2.2.2"}, "owner")}
	current := []dns.Endpoint{endpoint("app.example.com", "CNAME", []string{"lb.example.net"}, "other")}

	operations := Build(desired, current, "owner", CleanupDelete)
	if len(operations) != 1 || operations[0].Type != OperationCreate {
		t.Fatalf("unowned different record type must not block A record create, got %#v", operations)
	}
}

func TestPlanNonOneToOneTargetChangeDoesNotReplace(t *testing.T) {
	desired := []dns.Endpoint{
		endpoint("app.example.com", "A", []string{"1.1.1.1"}, "owner"),
		endpoint("app.example.com", "A", []string{"2.2.2.2"}, "owner"),
	}
	current := []dns.Endpoint{providerEndpoint("app.example.com", "A", []string{"3.3.3.3"}, "owner", "7")}

	operations := Build(desired, current, "owner", CleanupDelete)
	counts := map[string]int{}
	for _, op := range operations {
		counts[op.Type]++
	}
	if counts[OperationReplace] != 0 {
		t.Fatalf("non 1:1 target change must not be a replace: %#v", operations)
	}
	if counts[OperationCreate] != 2 || counts[OperationDelete] != 1 {
		t.Fatalf("expected 2 create + 1 delete, got %#v", counts)
	}
}

func TestDeactivateIsIdempotentForAlreadyDisabledRecord(t *testing.T) {
	current := []dns.Endpoint{disabledEndpoint("stale.example.com", "A", []string{"203.0.113.50"}, "owner", "9")}
	operations := Build(nil, current, "owner", CleanupDeactivate)
	if len(operations) != 0 {
		t.Fatalf("an already-disabled stale record must not be re-deactivated, got %#v", operations)
	}
}

func TestPlanReconcilesDuplicateOwnedRows(t *testing.T) {
	desired := []dns.Endpoint{endpoint("app.example.com", "A", []string{"1.1.1.1"}, "owner")}
	current := []dns.Endpoint{
		providerEndpoint("app.example.com", "A", []string{"1.1.1.1"}, "owner", "5"),
		providerEndpoint("app.example.com", "A", []string{"1.1.1.1"}, "owner", "6"),
	}

	ops := Build(desired, current, "owner", CleanupDelete)
	counts := map[string]int{}
	deletedID := ""
	for _, op := range ops {
		counts[op.Type]++
		if op.Type == OperationDelete {
			deletedID = op.Current.ProviderID
		}
	}
	if counts[OperationDelete] != 1 || counts[OperationUpdate] != 0 || counts[OperationCreate] != 0 {
		t.Fatalf("expected exactly one delete of the duplicate row, got %#v", counts)
	}
	if deletedID != "6" {
		t.Fatalf("expected the duplicate to be removed deterministically (keep lowest id), got %q", deletedID)
	}

	for _, op := range Build(desired, current, "owner", CleanupKeep) {
		if op.Type == OperationDelete || op.Type == OperationDeactivate {
			t.Fatalf("keep policy must not remove duplicate rows: %#v", op)
		}
	}
}

func TestPlanConflictsWithUnownedLogicalSiblingDifferentTarget(t *testing.T) {
	desired := []dns.Endpoint{endpoint("app.example.com", "A", []string{"203.0.113.20"}, "owner")}
	current := []dns.Endpoint{endpoint("app.example.com", "A", []string{"203.0.113.10"}, "other")}

	operations := Build(desired, current, "owner", CleanupDelete)
	if len(operations) != 1 {
		t.Fatalf("expected a single conflict, got %#v", operations)
	}
	op := operations[0]
	if op.Type != OperationConflict {
		t.Fatalf("unowned logical sibling must conflict, got %q", op.Type)
	}
	if op.Current.Targets[0] != "203.0.113.10" || op.Desired.Targets[0] != "203.0.113.20" {
		t.Fatalf("conflict should preserve desired/current evidence, got %#v", op)
	}
}

func TestPlanConflictSuppressesOwnedStaleCleanupForLogicalRecord(t *testing.T) {
	desired := []dns.Endpoint{endpoint("app.example.com", "A", []string{"203.0.113.20"}, "owner")}
	current := []dns.Endpoint{
		providerEndpoint("app.example.com", "A", []string{"203.0.113.10"}, "owner", "5"),
		endpoint("app.example.com", "A", []string{"203.0.113.30"}, "other"),
	}

	operations := Build(desired, current, "owner", CleanupDelete)
	if len(operations) != 1 {
		t.Fatalf("logical conflict must suppress mutations for that record, got %#v", operations)
	}
	if operations[0].Type != OperationConflict {
		t.Fatalf("expected conflict, got %#v", operations)
	}
}

func TestSourceChangeEmitsUpdate(t *testing.T) {
	desired := []dns.Endpoint{sourcedEndpoint("app.example.com", "A", []string{"1.1.1.1"}, "owner", dns.SourceRef{Kind: "Ingress", Namespace: "apps", Name: "web"})}
	current := []dns.Endpoint{sourcedProviderEndpoint("app.example.com", "A", []string{"1.1.1.1"}, "owner", "5", dns.SourceRef{Kind: "Service", Namespace: "apps", Name: "web"})}

	operations := Build(desired, current, "owner", CleanupDelete)
	if len(operations) != 1 || operations[0].Type != OperationUpdate {
		t.Fatalf("a source-only change must emit a single update so the comment is rewritten, got %#v", operations)
	}
}

func endpoint(name, recordType string, targets []string, owner string) dns.Endpoint {
	return ttlEndpoint(name, recordType, targets, owner, 300)
}

func disabledEndpoint(name, recordType string, targets []string, owner, providerID string) dns.Endpoint {
	e := providerEndpoint(name, recordType, targets, owner, providerID)
	e.Disabled = true
	return e
}

func sourcedEndpoint(name, recordType string, targets []string, owner string, source dns.SourceRef) dns.Endpoint {
	e := ttlEndpoint(name, recordType, targets, owner, 300)
	e.Source = source
	return e
}

func sourcedProviderEndpoint(name, recordType string, targets []string, owner, providerID string, source dns.SourceRef) dns.Endpoint {
	e := providerEndpoint(name, recordType, targets, owner, providerID)
	e.Source = source
	return e
}

func providerEndpoint(name, recordType string, targets []string, owner, providerID string) dns.Endpoint {
	e := ttlEndpoint(name, recordType, targets, owner, 300)
	e.ProviderID = providerID
	return e
}

func ttlEndpoint(name, recordType string, targets []string, owner string, ttl int64) dns.Endpoint {
	return dns.Endpoint{
		DNSName:    name,
		RecordType: recordType,
		Targets:    targets,
		TTL:        ttl,
		Zone:       "example.com",
		OwnerID:    owner,
	}.Normalize()
}
