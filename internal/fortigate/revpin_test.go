package fortigate

import (
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

// The revision is part of every approved plan hash, so an implementation change
// must not change its bytes. These digests were produced by the original
// sort-by-marshal implementation.
func TestRecordsRevisionDigestIsStable(t *testing.T) {
	records := []dns.Endpoint{
		{ProviderID: "2", Zone: "example.com", DNSName: "b.example.com", RecordType: dns.RecordA, Targets: []string{"203.0.113.2"}, TTL: 300},
		{ProviderID: "1", Zone: "Example.COM.", DNSName: "A.Example.COM.", RecordType: "a", Targets: []string{"203.0.113.1"}, TTL: 60},
		{ProviderID: "3", Zone: "example.com", DNSName: "c.example.com", RecordType: dns.RecordCNAME, Targets: []string{"t.example.net"}, TTL: 30, Disabled: true},
		{ProviderID: "10", Zone: "example.com", DNSName: "d.example.com", RecordType: dns.RecordAAAA, Targets: []string{"2001:db8::1"}, TTL: 30},
	}
	for name, tc := range map[string]struct {
		records []dns.Endpoint
		want    string
	}{
		"mixed": {records, "sha256:499f8133c0234332eeba31e6e87b7c0a4d3823e092a5a3ae2a4bd42a908131bf"},
		"empty": {nil, "sha256:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"},
	} {
		got, err := recordsRevision(tc.records)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != tc.want {
			t.Fatalf("%s revision = %s, want %s", name, got, tc.want)
		}
	}
}
