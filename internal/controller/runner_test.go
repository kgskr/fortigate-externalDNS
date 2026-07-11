package controller

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"

	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	"github.com/kgskr/fortigate-external-dns/internal/source"
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
			if got := cleanupAllowed(tc.endpoint, opts, false); got != tc.allowed {
				t.Fatalf("cleanupAllowed() = %v, want %v", got, tc.allowed)
			}
		})
	}
}

func TestExclusiveZoneCleanupScopeUsesOnlyDomainFilters(t *testing.T) {
	opts := source.Options{
		Sources:       []string{source.SourceService},
		Namespaces:    []string{"apps"},
		DomainFilters: []string{"example.com"},
	}
	inside := dns.Endpoint{
		DNSName: "owned.example.com",
		Source:  dns.SourceRef{Kind: "Unknown", Namespace: "other"},
	}
	if !cleanupAllowed(inside, opts, true) {
		t.Fatal("exclusive zone cleanup must not depend on unavailable per-record source metadata")
	}
	outside := inside
	outside.DNSName = "outside.example.net"
	if cleanupAllowed(outside, opts, true) {
		t.Fatal("exclusive zone cleanup must remain bounded by domain filters")
	}
}

func TestRunRetriesAfterInitialReconcileFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := &failOnceDNSClient{cancel: cancel}
	runner := Runner{
		Config: config.Config{
			Interval:      time.Millisecond,
			Sources:       []string{source.SourceService},
			DefaultTTL:    300,
			OwnerID:       "cluster-a",
			CleanupPolicy: "keep",
			FortiGate:     config.FortiGateConfig{Zone: "example.com"},
		},
		Kube: source.KubernetesClients{
			Core:    fake.NewSimpleClientset(),
			Gateway: gatewayfake.NewSimpleClientset(),
		},
		DNSClient: client,
		Logger:    slog.Default(),
	}

	err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runner should continue after the first error and stop on cancellation, got %v", err)
	}
	if client.listCalls != 2 {
		t.Fatalf("expected the initial failure to be retried once, got %d list calls", client.listCalls)
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

func TestGuardCleanupRefusesEmptyDesired(t *testing.T) {
	operations := []plan.Operation{
		{Type: plan.OperationDelete, Current: dns.Endpoint{DNSName: "a.example.com"}},
		{Type: plan.OperationDeactivate, Current: dns.Endpoint{DNSName: "b.example.com"}},
	}
	kept, refusal := guardCleanup(operations, 0, config.Config{})
	if len(kept) != 0 {
		t.Fatalf("empty-desired cleanup must be refused, kept %#v", kept)
	}
	if refusal.reason != refusalEmptyDesired || refusal.count != 2 {
		t.Fatalf("unexpected refusal: %#v", refusal)
	}
}

func TestGuardCleanupOverrideAllowsEmptyDesired(t *testing.T) {
	operations := []plan.Operation{{Type: plan.OperationDelete, Current: dns.Endpoint{DNSName: "a.example.com"}}}
	kept, refusal := guardCleanup(operations, 0, config.Config{AllowEmptyDesiredCleanup: true})
	if len(kept) != 1 || refusal.count != 0 {
		t.Fatalf("override must let empty-desired cleanup proceed, kept %#v refusal %#v", kept, refusal)
	}
}

func TestGuardCleanupCapRefusesOnlyCleanup(t *testing.T) {
	operations := []plan.Operation{
		{Type: plan.OperationCreate, Desired: dns.Endpoint{DNSName: "new.example.com"}},
		{Type: plan.OperationUpdate, Desired: dns.Endpoint{DNSName: "upd.example.com"}},
		{Type: plan.OperationDelete, Current: dns.Endpoint{DNSName: "a.example.com"}},
		{Type: plan.OperationDelete, Current: dns.Endpoint{DNSName: "b.example.com"}},
	}
	kept, refusal := guardCleanup(operations, 5, config.Config{MaxCleanupPerCycle: 1})
	if refusal.reason != refusalCapExceeded || refusal.count != 2 {
		t.Fatalf("cap of 1 with 2 planned cleanups must refuse, got %#v", refusal)
	}
	if len(kept) != 2 || kept[0].Type != plan.OperationCreate || kept[1].Type != plan.OperationUpdate {
		t.Fatalf("creates and updates must survive a refused cycle, kept %#v", kept)
	}
}

func TestGuardCleanupWithinCapProceeds(t *testing.T) {
	operations := []plan.Operation{
		{Type: plan.OperationDelete, Current: dns.Endpoint{DNSName: "a.example.com"}},
	}
	kept, refusal := guardCleanup(operations, 5, config.Config{MaxCleanupPerCycle: 3})
	if len(kept) != 1 || refusal.count != 0 {
		t.Fatalf("cleanup within the cap must proceed, kept %#v refusal %#v", kept, refusal)
	}
}

// TestRunOnceRefusesEmptyDesiredCleanup exercises the guard through a full
// reconcile: discovery succeeds with zero endpoints while an owned record
// exists, so the cycle's delete is refused; once a matching Service exists
// again (recovery), the same current state produces no refusal.
func TestRunOnceRefusesEmptyDesiredCleanup(t *testing.T) {
	owned := dns.Endpoint{
		DNSName:    "web.example.com",
		RecordType: "A",
		Targets:    []string{"203.0.113.10"},
		TTL:        300,
		Zone:       "example.com",
		OwnerID:    "cluster-a",
		ProviderID: "7",
		Source:     dns.SourceRef{Kind: "Service", Namespace: "apps", Name: "web"},
	}.Normalize()
	client := &recordingDNSClient{records: []dns.Endpoint{owned}}
	runner := Runner{
		Config: config.Config{
			Interval:      time.Second,
			Sources:       []string{source.SourceService},
			Namespaces:    []string{"apps"},
			DomainFilters: []string{"example.com"},
			DefaultTTL:    300,
			OwnerID:       "cluster-a",
			CleanupPolicy: "delete",
			FortiGate:     config.FortiGateConfig{Zone: "example.com"},
		},
		Kube: source.KubernetesClients{
			Core:    fake.NewSimpleClientset(),
			Gateway: gatewayfake.NewSimpleClientset(),
		},
		DNSClient: client,
		Logger:    slog.Default(),
	}

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.operations) != 0 {
		t.Fatalf("empty-desired cycle must apply no cleanup, got %#v", client.operations)
	}

	// Recovery: the source object exists again so desired is non-empty; a
	// genuinely stale extra owned record is now cleaned up normally.
	stale := owned
	stale.DNSName = "old.example.com"
	stale.ProviderID = "8"
	client.records = []dns.Endpoint{owned, stale}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "web",
			Namespace:   "apps",
			Annotations: map[string]string{source.AnnotationHostname: "web.example.com"},
		},
		Spec:   corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}}}},
	}
	runner.Kube = source.KubernetesClients{
		Core:    fake.NewSimpleClientset(service),
		Gateway: gatewayfake.NewSimpleClientset(),
	}
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.operations) != 1 || client.operations[0].Type != plan.OperationDelete || client.operations[0].Current.DNSName != "old.example.com" {
		t.Fatalf("recovered cycle must clean up the stale record normally, got %#v", client.operations)
	}
}

// TestRunOnceDiscoveryErrorPlansNoCleanup asserts the pre-existing fail-closed
// behavior the guard builds on: a failed discovery aborts the cycle before any
// plan is built, so no cleanup can be derived from incomplete state.
func TestRunOnceDiscoveryErrorPlansNoCleanup(t *testing.T) {
	core := fake.NewSimpleClientset()
	core.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("api server unavailable")
	})
	client := &recordingDNSClient{records: []dns.Endpoint{{
		DNSName:    "web.example.com",
		RecordType: "A",
		Targets:    []string{"203.0.113.10"},
		Zone:       "example.com",
		OwnerID:    "cluster-a",
		ProviderID: "7",
	}}}
	runner := Runner{
		Config: config.Config{
			Interval:      time.Second,
			Sources:       []string{source.SourceService},
			DefaultTTL:    300,
			OwnerID:       "cluster-a",
			CleanupPolicy: "delete",
			FortiGate:     config.FortiGateConfig{Zone: "example.com"},
		},
		Kube: source.KubernetesClients{
			Core:    core,
			Gateway: gatewayfake.NewSimpleClientset(),
		},
		DNSClient: client,
		Logger:    slog.Default(),
	}

	if err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("a failed discovery must surface as a reconcile error")
	}
	if len(client.operations) != 0 {
		t.Fatalf("a failed discovery must not reach apply, got %#v", client.operations)
	}
}

func TestRunOnceSuppressesAllCleanupWhenAnySourceIsIncomplete(t *testing.T) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "web",
			Namespace:   "apps",
			Annotations: map[string]string{source.AnnotationHostname: "web.example.com"},
		},
		Spec:   corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}}}},
	}
	gatewayClient := gatewayfake.NewSimpleClientset()
	gatewayClient.PrependReactor("list", "httproutes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: gatewayv1.GroupName, Resource: "httproutes"}, "")
	})
	client := &recordingDNSClient{records: []dns.Endpoint{{
		DNSName:    "stale.example.com",
		RecordType: "A",
		Targets:    []string{"203.0.113.99"},
		Zone:       "example.com",
		ProviderID: "7",
		Source:     dns.SourceRef{Kind: "Gateway", Namespace: "apps", Name: "public"},
	}}}
	runner := Runner{
		Config: config.Config{
			Interval:      time.Second,
			Sources:       []string{source.SourceService, source.SourceIngress, source.SourceGateway},
			DomainFilters: []string{"example.com"},
			DefaultTTL:    300,
			OwnerID:       "cluster-a",
			CleanupPolicy: "delete",
			FortiGate: config.FortiGateConfig{
				Zone:                   "example.com",
				ExclusiveZoneOwnership: true,
			},
		},
		Kube: source.KubernetesClients{
			Core:    fake.NewSimpleClientset(service),
			Gateway: gatewayClient,
		},
		DNSClient: client,
		Logger:    slog.Default(),
	}

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.operations) != 1 || client.operations[0].Type != plan.OperationCreate || client.operations[0].Desired.DNSName != "web.example.com" {
		t.Fatalf("partial discovery must preserve creates while suppressing every cleanup operation, got %#v", client.operations)
	}
}

func TestRunOnceExclusiveZoneTreatsListedRecordsAsOwned(t *testing.T) {
	client := &recordingDNSClient{records: []dns.Endpoint{{
		DNSName:    "stale.example.com",
		RecordType: "A",
		Targets:    []string{"203.0.113.99"},
		Zone:       "example.com",
		ProviderID: "7",
	}}}
	runner := Runner{
		Config: config.Config{
			Interval:                 time.Second,
			Sources:                  []string{source.SourceService, source.SourceIngress, source.SourceGateway},
			DomainFilters:            []string{"example.com"},
			DefaultTTL:               300,
			OwnerID:                  "cluster-a",
			CleanupPolicy:            "delete",
			AllowEmptyDesiredCleanup: true,
			FortiGate: config.FortiGateConfig{
				Zone:                   "example.com",
				ExclusiveZoneOwnership: true,
			},
		},
		Kube: source.KubernetesClients{
			Core:    fake.NewSimpleClientset(),
			Gateway: gatewayfake.NewSimpleClientset(),
		},
		DNSClient: client,
		Logger:    slog.Default(),
	}

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.operations) != 1 || client.operations[0].Type != plan.OperationDelete {
		t.Fatalf("exclusive zone mode must treat listed records as controller-owned, got %#v", client.operations)
	}
}

func TestRunOnceRestrictedExclusiveZoneAdoptsOnlyExactCurrentState(t *testing.T) {
	allSources := []string{source.SourceService, source.SourceIngress, source.SourceGateway}
	cases := []struct {
		name           string
		sources        []string
		namespaces     []string
		current        dns.Endpoint
		wantOperation  string
		wantOwnerAfter string
	}{
		{
			name:       "namespace restricted exact match is a no-op",
			sources:    allSources,
			namespaces: []string{"apps"},
			current: restrictedCurrentEndpoint(
				"web.example.com", dns.RecordA, "203.0.113.10", 300, false,
			),
			wantOwnerAfter: "cluster-a",
		},
		{
			name:    "source restricted target mismatch conflicts",
			sources: []string{source.SourceService},
			current: restrictedCurrentEndpoint(
				"web.example.com", dns.RecordA, "203.0.113.20", 300, false,
			),
			wantOperation: plan.OperationConflict,
		},
		{
			name:    "source restricted TTL mismatch conflicts",
			sources: []string{source.SourceService},
			current: restrictedCurrentEndpoint(
				"web.example.com", dns.RecordA, "203.0.113.10", 600, false,
			),
			wantOperation: plan.OperationConflict,
		},
		{
			name:    "source restricted status mismatch conflicts",
			sources: []string{source.SourceService},
			current: restrictedCurrentEndpoint(
				"web.example.com", dns.RecordA, "203.0.113.10", 300, true,
			),
			wantOperation: plan.OperationConflict,
		},
		{
			name:    "source restricted CNAME transition conflicts",
			sources: []string{source.SourceService},
			current: restrictedCurrentEndpoint(
				"web.example.com", dns.RecordCNAME, "lb.example.net", 300, false,
			),
			wantOperation: plan.OperationConflict,
		},
		{
			name:    "source restricted compatible record type still conflicts",
			sources: []string{source.SourceService},
			current: restrictedCurrentEndpoint(
				"web.example.com", dns.RecordAAAA, "2001:db8::10", 300, false,
			),
			wantOperation: plan.OperationConflict,
		},
		{
			name:    "genuinely missing name can be created",
			sources: []string{source.SourceService},
			current: restrictedCurrentEndpoint(
				"old.example.com", dns.RecordA, "203.0.113.99", 300, false,
			),
			wantOperation: plan.OperationCreate,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := restrictedOwnershipService("web", "203.0.113.10")
			client := &recordingDNSClient{records: []dns.Endpoint{tc.current}}
			runner := Runner{
				Config: config.Config{
					Interval:      time.Second,
					Sources:       tc.sources,
					Namespaces:    tc.namespaces,
					DomainFilters: []string{"example.com"},
					DefaultTTL:    300,
					OwnerID:       "cluster-a",
					CleanupPolicy: "keep",
					FortiGate: config.FortiGateConfig{
						Zone:                   "example.com",
						ExclusiveZoneOwnership: true,
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
			if tc.wantOperation == "" {
				if len(client.operations) != 0 {
					t.Fatalf("an exact restricted record must be adopted as a no-op, got %#v", client.operations)
				}
			} else if len(client.operations) != 1 || client.operations[0].Type != tc.wantOperation {
				t.Fatalf("operations = %#v, want one %s", client.operations, tc.wantOperation)
			}
			if tc.wantOperation == plan.OperationConflict {
				for _, operation := range client.operations {
					switch operation.Type {
					case plan.OperationCreate, plan.OperationUpdate, plan.OperationReplace, plan.OperationDelete, plan.OperationDeactivate:
						t.Fatalf("restricted mismatch emitted a mutation: %#v", client.operations)
					}
				}
			}
			if got := client.records[0].OwnerID; got != tc.wantOwnerAfter {
				t.Fatalf("current owner after restricted adoption = %q, want %q", got, tc.wantOwnerAfter)
			}
		})
	}
}

func TestRunOnceRestrictedExclusiveZoneBlocksAdditionalTargetForExistingName(t *testing.T) {
	serviceA := restrictedOwnershipService("web-a", "203.0.113.10")
	serviceB := restrictedOwnershipService("web-b", "203.0.113.20")
	client := &recordingDNSClient{records: []dns.Endpoint{
		restrictedCurrentEndpoint("web.example.com", dns.RecordA, "203.0.113.10", 300, false),
	}}
	runner := Runner{
		Config: config.Config{
			Interval:      time.Second,
			Sources:       []string{source.SourceService},
			DomainFilters: []string{"example.com"},
			DefaultTTL:    300,
			OwnerID:       "cluster-a",
			CleanupPolicy: "keep",
			FortiGate: config.FortiGateConfig{
				Zone:                   "example.com",
				ExclusiveZoneOwnership: true,
			},
		},
		Kube: source.KubernetesClients{
			Core:    fake.NewSimpleClientset(serviceA, serviceB),
			Gateway: gatewayfake.NewSimpleClientset(),
		},
		DNSClient: client,
		Logger:    slog.Default(),
	}

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.operations) != 1 || client.operations[0].Type != plan.OperationConflict {
		t.Fatalf("an existing name with a missing desired target must fail closed instead of creating, got %#v", client.operations)
	}
}

func TestRunOnceUnrestrictedExclusiveZoneStillReplacesMismatchedRecord(t *testing.T) {
	service := restrictedOwnershipService("web", "203.0.113.10")
	client := &recordingDNSClient{records: []dns.Endpoint{
		restrictedCurrentEndpoint("web.example.com", dns.RecordA, "203.0.113.20", 300, false),
	}}
	runner := Runner{
		Config: config.Config{
			Interval:      time.Second,
			Sources:       []string{source.SourceService, source.SourceIngress, source.SourceGateway},
			DomainFilters: []string{"example.com"},
			DefaultTTL:    300,
			OwnerID:       "cluster-a",
			CleanupPolicy: "delete",
			FortiGate: config.FortiGateConfig{
				Zone:                   "example.com",
				ExclusiveZoneOwnership: true,
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
	if len(client.operations) != 1 || client.operations[0].Type != plan.OperationReplace {
		t.Fatalf("unrestricted exclusive discovery must retain in-place replacement behavior, got %#v", client.operations)
	}
}

func restrictedOwnershipService(name, target string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "apps",
			Annotations: map[string]string{source.AnnotationHostname: "web.example.com"},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{
			{IP: target},
		}}},
	}
}

func restrictedCurrentEndpoint(name, recordType, target string, ttl int64, disabled bool) dns.Endpoint {
	return dns.Endpoint{
		DNSName:    name,
		RecordType: recordType,
		Targets:    []string{target},
		TTL:        ttl,
		Zone:       "example.com",
		OwnerID:    "cluster-a",
		ProviderID: "7",
		Disabled:   disabled,
	}
}

type recordingDNSClient struct {
	records    []dns.Endpoint
	operations []plan.Operation
	dryRun     bool
}

type failOnceDNSClient struct {
	listCalls int
	cancel    context.CancelFunc
}

func (c *failOnceDNSClient) ListRecords(context.Context) ([]dns.Endpoint, error) {
	c.listCalls++
	if c.listCalls == 1 {
		return nil, errors.New("transient FortiGate outage")
	}
	c.cancel()
	return nil, nil
}

func (c *failOnceDNSClient) Apply(context.Context, []plan.Operation, bool) error {
	return nil
}

func (r *recordingDNSClient) ListRecords(ctx context.Context) ([]dns.Endpoint, error) {
	return r.records, nil
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
