package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	"github.com/kgskr/fortigate-external-dns/internal/source"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSourceExpansionBudgetPreservesUnrelatedPublishingAndPreventsCleanup(t *testing.T) {
	ordinary := restrictedOwnershipService("ordinary", "203.0.113.10")
	ordinary.Annotations[source.AnnotationHostname] = "ordinary.example.com"
	excessive := restrictedOwnershipService("excessive", "203.0.113.20")
	excessive.Spec.Type = corev1.ServiceTypeClusterIP
	excessive.Status = corev1.ServiceStatus{}
	var hosts []string
	for i := 0; i < 64; i++ {
		hosts = append(hosts, fmt.Sprintf("host-%d.example.com", i))
		excessive.Spec.ExternalIPs = append(excessive.Spec.ExternalIPs, fmt.Sprintf("203.0.113.%d", i+1))
	}
	excessive.Annotations[source.AnnotationHostname] = strings.Join(hosts, ",")
	stale := restrictedCurrentEndpoint("old.example.com", dns.RecordA, "198.51.100.7", 300, false)
	for _, includeExcessive := range []bool{false, true} {
		t.Run(fmt.Sprintf("excessive=%t", includeExcessive), func(t *testing.T) {
			client := &recordingDNSClient{records: []dns.Endpoint{stale}}
			runner := planTestRunner(ordinary, client)
			runner.Config.Namespaces = nil
			runner.Config.Sources = []string{source.SourceService, source.SourceIngress, source.SourceGateway}
			runner.Config.CleanupPolicy = "delete"
			if includeExcessive {
				runner.Kube.Core = fake.NewSimpleClientset(ordinary, excessive)
			}
			audit, err := runner.Prepare(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if audit.DiscoveryComplete == includeExcessive {
				t.Fatalf("discovery complete=%t, excessive=%t", audit.DiscoveryComplete, includeExcessive)
			}
			creates, deletes := 0, 0
			for _, operation := range audit.Operations {
				switch operation.Type {
				case plan.OperationCreate:
					creates++
					if operation.Desired.DNSName != "ordinary.example.com" {
						t.Fatalf("partially expanded excessive source escaped: %#v", operation)
					}
				case plan.OperationDelete, plan.OperationDeactivate, plan.OperationReplace:
					deletes++
				}
			}
			if creates != 1 || (includeExcessive && deletes != 0) || (!includeExcessive && deletes != 1) {
				t.Fatalf("creates=%d deletes=%d, excessive=%t", creates, deletes, includeExcessive)
			}
		})
	}
}
