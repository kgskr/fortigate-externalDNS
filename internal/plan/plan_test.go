package plan

import (
	"strings"
	"testing"

	"github.com/gilsu/fortigate-external-dns/internal/dns"
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

func endpoint(name, recordType string, targets []string, owner string) dns.Endpoint {
	return ttlEndpoint(name, recordType, targets, owner, 300)
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
