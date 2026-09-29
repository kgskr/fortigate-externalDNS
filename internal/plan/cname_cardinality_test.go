package plan

import (
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func TestPlanRejectsMultipleDesiredCNAMETargetsForSameName(t *testing.T) {
	desired := []dns.Endpoint{
		endpoint("app.example.com", "CNAME", []string{"a.example.net"}, "owner"),
		endpoint("app.example.com", "CNAME", []string{"b.example.net"}, "owner"),
	}
	operations := Build(desired, nil, "owner", CleanupDelete)
	if len(operations) != 1 || operations[0].Type != OperationConflict {
		t.Fatalf("two CNAME targets for one name must yield one conflict, got %#v", operations)
	}
	if got := operationReason(operations[0].Reason); got != OperationReasonCNAMETypeConflict {
		t.Fatalf("conflict must map to a fixed reason code, got %q", got)
	}
}

func TestPlanRejectsSingleCNAMEEndpointWithMultipleTargets(t *testing.T) {
	desired := []dns.Endpoint{endpoint("app.example.com", "CNAME", []string{"a.example.net", "b.example.net"}, "owner")}
	current := []dns.Endpoint{providerEndpoint("app.example.com", "CNAME", []string{"a.example.net"}, "owner", "3")}
	operations := Build(desired, current, "owner", CleanupDelete)
	if len(operations) != 1 || operations[0].Type != OperationConflict {
		t.Fatalf("multi-target CNAME must conflict instead of update, got %#v", operations)
	}
}

func TestPlanSingleCNAMETargetStillCreates(t *testing.T) {
	desired := []dns.Endpoint{endpoint("app.example.com", "CNAME", []string{"a.example.net"}, "owner")}
	operations := Build(desired, nil, "owner", CleanupDelete)
	if len(operations) != 1 || operations[0].Type != OperationCreate {
		t.Fatalf("expected create, got %#v", operations)
	}
}
