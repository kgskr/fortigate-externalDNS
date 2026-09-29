package fortigate

import (
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func pinRecords() []dns.Endpoint {
	return []dns.Endpoint{
		{ProviderID: "2", Zone: "example.com", DNSName: "b.example.com", RecordType: dns.RecordA, Targets: []string{"203.0.113.2"}, TTL: 300},
		{ProviderID: "1", Zone: "Example.COM.", DNSName: "A.Example.COM.", RecordType: "a", Targets: []string{"203.0.113.1"}, TTL: 60},
		{ProviderID: "3", Zone: "example.com", DNSName: "c.example.com", RecordType: dns.RecordCNAME, Targets: []string{"t.example.net"}, TTL: 30, Disabled: true},
		{ProviderID: "10", Zone: "example.com", DNSName: "d.example.com", RecordType: dns.RecordAAAA, Targets: []string{"2001:db8::1"}, TTL: 30},
	}
}

func TestPinTmp(t *testing.T) {
	r, _ := recordsRevision(pinRecords())
	e, _ := recordsRevision(nil)
	t.Log(r, e)
}
