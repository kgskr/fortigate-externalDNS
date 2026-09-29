package controller

import (
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
)

func ownershipRow(name, recordType, target, providerID string) dns.Endpoint {
	return dns.Endpoint{DNSName: name, RecordType: recordType, Targets: []string{target}, TTL: 300, Zone: "example.com", ProviderID: providerID}
}

func TestPrepareExclusiveOwnershipAdoptsOnlyAddressAndCNAMERows(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		current := []dns.Endpoint{
			ownershipRow("a.example.com", "A", "203.0.113.1", "1"),
			ownershipRow("aaaa.example.com", "AAAA", "2001:db8::1", "2"),
			ownershipRow("c.example.com", "CNAME", "t.example.net", "3"),
			ownershipRow("example.com", "NS", "ns1.example.com", "4"),
			ownershipRow("example.com", "MX", "mail.example.com", "5"),
			ownershipRow("ptr.example.com", "PTR", "x.example.com", "6"),
			ownershipRow("txt.example.com", "TXT", "v=spf1", "7"),
			ownershipRow("srv.example.com", "SRV", "s.example.com", "8"),
		}
		desired := append([]dns.Endpoint(nil), current[:3]...)
		prepareExclusiveOwnership(current, desired, "owner", restricted)
		for _, row := range current {
			adopted := row.OwnerID == "owner"
			want := row.RecordType == "A" || row.RecordType == "AAAA" || row.RecordType == "CNAME"
			if adopted != want {
				t.Fatalf("restricted=%v %s row adopted=%v want %v", restricted, row.RecordType, adopted, want)
			}
		}
	}
}

func TestUnadoptedTypesAreNeverCleanedUpInExclusiveMode(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		current := []dns.Endpoint{
			ownershipRow("example.com", "NS", "ns1.example.com", "4"),
			ownershipRow("txt.example.com", "TXT", "v=spf1", "7"),
			ownershipRow("stale.example.com", "A", "203.0.113.9", "9"),
		}
		prepareExclusiveOwnership(current, nil, "owner", restricted)
		operations := plan.Build(nil, current, "owner", plan.CleanupDelete)
		if restricted {
			// Restricted mode adopts only exact desired matches, so nothing at all is planned.
			if len(operations) != 0 {
				t.Fatalf("restricted: unexpected operations %#v", operations)
			}
			continue
		}
		if len(operations) != 1 || operations[0].Type != plan.OperationDelete || operations[0].Current.RecordType != "A" {
			t.Fatalf("only the stale A row may be cleaned up, got %#v", operations)
		}
	}
}

func TestDesiredCNAMEConflictsWithUnadoptedMXRow(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		current := []dns.Endpoint{ownershipRow("mail.example.com", "MX", "mx.example.com", "5")}
		desired := []dns.Endpoint{{DNSName: "mail.example.com", RecordType: "CNAME", Targets: []string{"t.example.net"}, TTL: 300, Zone: "example.com", OwnerID: "owner"}}
		conflicts := prepareExclusiveOwnership(current, desired, "owner", restricted)
		if len(conflicts) != 0 {
			t.Fatalf("restricted=%v: MX row must not produce a restricted name conflict, got %#v", restricted, conflicts)
		}
		operations := plan.Build(desired, current, "owner", plan.CleanupDelete)
		if len(operations) != 1 || operations[0].Type != plan.OperationConflict {
			t.Fatalf("restricted=%v: desired CNAME beside an MX row must be a conflict, got %#v", restricted, operations)
		}
	}
}
