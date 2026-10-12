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

func TestCNAMEChangedTargetRequiresAtomicReplacement(t *testing.T) {
	desired := []dns.Endpoint{endpoint("app.example.com", dns.RecordCNAME, []string{"new.example.net"}, "owner")}
	base := providerEndpoint("app.example.com", dns.RecordCNAME, []string{"old.example.net"}, "owner", "7")
	for _, tc := range []struct {
		name         string
		cleanup      CleanupPolicy
		allowCleanup bool
		providerID   string
		duplicates   bool
		want         string
	}{
		{name: "keep", cleanup: CleanupKeep, allowCleanup: true, providerID: "7", want: OperationConflict},
		{name: "deactivate", cleanup: CleanupDeactivate, allowCleanup: true, providerID: "7", want: OperationConflict},
		{name: "cleanup suppressed", cleanup: CleanupDelete, providerID: "7", want: OperationConflict},
		{name: "missing provider ID", cleanup: CleanupDelete, allowCleanup: true, want: OperationConflict},
		{name: "multiple current rows", cleanup: CleanupDelete, allowCleanup: true, providerID: "7", duplicates: true, want: OperationConflict},
		{name: "delete atomic replacement", cleanup: CleanupDelete, allowCleanup: true, providerID: "7", want: OperationReplace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := []dns.Endpoint{base}
			current[0].ProviderID = tc.providerID
			if tc.duplicates {
				duplicate := base
				duplicate.ProviderID = "8"
				current = append(current, duplicate)
			}
			operations := BuildWithCleanupScope(desired, current, "owner", tc.cleanup, func(dns.Endpoint) bool { return tc.allowCleanup })
			if len(operations) != 1 || operations[0].Type != tc.want {
				t.Fatalf("expected %s instead of a second CNAME create: %#v", tc.want, operations)
			}
			if tc.want == OperationReplace && (operations[0].Current.ProviderID != "7" || operations[0].Desired.Targets[0] != "new.example.net") {
				t.Fatalf("replacement lost provider identity or desired target: %#v", operations)
			}
		})
	}
}

func TestCNAMECurrentDesiredTargetStillAllowsStaleCleanup(t *testing.T) {
	desired := []dns.Endpoint{endpoint("app.example.com", dns.RecordCNAME, []string{"new.example.net"}, "owner")}
	current := []dns.Endpoint{
		providerEndpoint("app.example.com", dns.RecordCNAME, []string{"new.example.net"}, "owner", "7"),
		providerEndpoint("app.example.com", dns.RecordCNAME, []string{"old.example.net"}, "owner", "8"),
	}
	operations := Build(desired, current, "owner", CleanupDelete)
	if len(operations) != 1 || operations[0].Type != OperationDelete || operations[0].Current.ProviderID != "8" {
		t.Fatalf("an already visible desired CNAME must still permit stale cleanup: %#v", operations)
	}
}
