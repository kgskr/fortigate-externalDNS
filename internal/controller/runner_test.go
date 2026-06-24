package controller

import (
	"context"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	gatewayfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"

	"github.com/gilsu/fortigate-external-dns/internal/config"
	"github.com/gilsu/fortigate-external-dns/internal/dns"
	"github.com/gilsu/fortigate-external-dns/internal/plan"
	"github.com/gilsu/fortigate-external-dns/internal/source"
)

func TestDryRunSmoke(t *testing.T) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web",
			Namespace: "apps",
			Annotations: map[string]string{
				source.AnnotationHostname: "web.example.com",
			},
		},
		Spec:   corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}}}},
	}
	client := &recordingDNSClient{}
	runner := Runner{
		Config: config.Config{
			DryRun:        true,
			Interval:      time.Second,
			Sources:       []string{source.SourceService},
			Namespaces:    []string{"apps"},
			DomainFilters: []string{"example.com"},
			DefaultTTL:    300,
			OwnerID:       "cluster-a",
			CleanupPolicy: "delete",
			FortiGate: config.FortiGateConfig{
				Zone: "example.com",
			},
		},
		Kube: source.KubernetesClients{
			Core:    fake.NewSimpleClientset(service),
			Gateway: gatewayfake.NewSimpleClientset(),
		},
		DNSClient: client,
		Logger:    slog.Default(),
	}

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.operations) != 1 || client.operations[0].Type != plan.OperationCreate {
		t.Fatalf("expected create operation, got %#v", client.operations)
	}
	if !client.dryRun {
		t.Fatal("expected dry-run apply")
	}
}

func TestCleanupScopeProtectsRecordsOutsideFilters(t *testing.T) {
	opts := source.Options{
		Sources:       []string{source.SourceService},
		Namespaces:    []string{"apps"},
		DomainFilters: []string{"example.com"},
	}
	cases := []struct {
		name     string
		endpoint dns.Endpoint
		allowed  bool
	}{
		{
			name:     "domain outside filter",
			endpoint: dns.Endpoint{DNSName: "old.example.net", Source: dns.SourceRef{Kind: "Service", Namespace: "apps", Name: "web"}},
			allowed:  false,
		},
		{
			name:     "namespace outside filter",
			endpoint: dns.Endpoint{DNSName: "old.example.com", Source: dns.SourceRef{Kind: "Service", Namespace: "other", Name: "web"}},
			allowed:  false,
		},
		{
			name:     "source outside filter",
			endpoint: dns.Endpoint{DNSName: "old.example.com", Source: dns.SourceRef{Kind: "Ingress", Namespace: "apps", Name: "web"}},
			allowed:  false,
		},
		{
			name:     "inside filter",
			endpoint: dns.Endpoint{DNSName: "old.example.com", Source: dns.SourceRef{Kind: "Service", Namespace: "apps", Name: "web"}},
			allowed:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanupAllowed(tc.endpoint, opts); got != tc.allowed {
				t.Fatalf("cleanupAllowed() = %v, want %v", got, tc.allowed)
			}
		})
	}
}

func TestReconcileTimeoutCancelsLongRunningLoop(t *testing.T) {
	runner := Runner{
		Config: config.Config{
			Interval:         time.Second,
			ReconcileTimeout: 5 * time.Millisecond,
			Sources:          []string{source.SourceService},
			DefaultTTL:       300,
			OwnerID:          "cluster-a",
			CleanupPolicy:    "delete",
			FortiGate:        config.FortiGateConfig{Zone: "example.com"},
		},
		Kube: source.KubernetesClients{
			Core:    fake.NewSimpleClientset(),
			Gateway: gatewayfake.NewSimpleClientset(),
		},
		DNSClient: &blockingDNSClient{},
		Logger:    slog.Default(),
	}

	start := time.Now()
	err := runner.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected a timeout error from the bounded reconcile")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("reconcile did not respect its timeout, took %s", elapsed)
	}
}

type recordingDNSClient struct {
	operations []plan.Operation
	dryRun     bool
}

func (r *recordingDNSClient) ListRecords(ctx context.Context) ([]dns.Endpoint, error) {
	return nil, nil
}

func (r *recordingDNSClient) Apply(ctx context.Context, operations []plan.Operation, dryRun bool) error {
	r.operations = append([]plan.Operation(nil), operations...)
	r.dryRun = dryRun
	return nil
}

// blockingDNSClient blocks ListRecords until the context is canceled, modeling a
// hung dependency that the reconcile timeout must bound.
type blockingDNSClient struct{}

func (b *blockingDNSClient) ListRecords(ctx context.Context) ([]dns.Endpoint, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingDNSClient) Apply(ctx context.Context, operations []plan.Operation, dryRun bool) error {
	return nil
}
