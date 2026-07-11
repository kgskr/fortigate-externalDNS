package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	"github.com/kgskr/fortigate-external-dns/internal/ownership"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	"github.com/kgskr/fortigate-external-dns/internal/source"
	statuswriter "github.com/kgskr/fortigate-external-dns/internal/status"
	"github.com/kgskr/fortigate-external-dns/internal/target"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPlatformLegacyCompatibilityUsesExclusiveSingleTarget(t *testing.T) {
	service := integrationService("apps", "legacy", "legacy.example.com", "192.0.2.10")
	clients := integrationKubernetes(t, []runtime.Object{service}, nil)
	cfg := integrationConfig()
	cfg.FortiGate = config.FortiGateConfig{
		BaseURL: "https://fortigate.example.com", APIToken: "legacy-token", VDOM: "root", Zone: "example.com", ExclusiveZoneOwnership: true,
	}
	cfg.OwnerID = "legacy-controller"
	definitions, err := target.BuildDefinitions(cfg, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	factory := newIntegrationClientFactory()
	resources := newIntegrationResourceFactory(t, clients.Dynamic)
	manager, err := target.NewRuntimeManager(nil, factory, resources, metrics.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, syncErr := manager.Sync(context.Background(), definitions); syncErr != nil || len(result.Failures) != 0 {
		t.Fatalf("Sync() result=%#v err=%v", result, syncErr)
	}
	targetRuntime, ok := manager.Runtime(target.DefaultName)
	if !ok {
		t.Fatal("legacy default runtime was not constructed")
	}
	if err := runTargetAudit(context.Background(), cfg, clients, targetRuntime, metrics.New(), discardLogger()); err != nil {
		t.Fatalf("runTargetAudit() error = %v", err)
	}
	provider := factory.provider(target.DefaultName)
	if records := provider.snapshotRecords(); len(records) != 1 || records[0].DNSName != "legacy.example.com" || records[0].Zone != "example.com" {
		t.Fatalf("legacy provider records = %#v", records)
	}
	claims, err := resources.repository(target.DefaultName).List(context.Background())
	if err != nil || len(claims) != 0 {
		t.Fatalf("exclusive legacy mode created ownership claims: %#v err=%v", claims, err)
	}
}

func TestPlatformTwoTargetsIsolateFailureZoneVDOMAndStatus(t *testing.T) {
	objects := []runtime.Object{
		integrationService("apps", "left", "api.left.example.com", "192.0.2.11"),
		integrationService("apps", "right", "api.right.example.net", "192.0.2.12"),
	}
	definitions := []target.Definition{
		integrationDefinition("left", "left.example.com", "left-vdom", v1alpha1.OwnershipModeExclusive),
		integrationDefinition("right", "right.example.net", "right-vdom", v1alpha1.OwnershipModeExclusive),
	}
	clients := integrationKubernetes(t, append(objects, secretsForDefinitions(definitions)...), nil)
	factory := newIntegrationClientFactory()
	factory.failList[definitions[0].Key()] = true
	manager := integrationManager(t, clients, definitions, factory)
	results := manager.RunAll(context.Background(), func(ctx context.Context, runtime *target.Runtime) error {
		return runTargetAudit(ctx, integrationConfig(), clients, runtime, metrics.New(), discardLogger())
	})
	if results[definitions[0].Key()].Succeeded || !results[definitions[1].Key()].Succeeded {
		t.Fatalf("isolated target results = %#v", results)
	}
	rightProvider := factory.provider(definitions[1].Key())
	if records := rightProvider.snapshotRecords(); len(records) != 1 || records[0].Zone != "right.example.net" {
		t.Fatalf("healthy target records = %#v", records)
	}
	if captured := factory.definition(definitions[1].Key()); captured.VDOM != "right-vdom" || captured.Zone != "right.example.net" {
		t.Fatalf("factory target scope = %#v", captured)
	}
	statuses, err := clients.Dynamic.Resource(v1alpha1.StatusGVR).Namespace("dns-system").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(statuses.Items) != 2 {
		t.Fatalf("per-target status objects = %d err=%v", len(statuses.Items), err)
	}
}

func TestPlatformSecretAndCARotationRebuildsOnlyAffectedRuntime(t *testing.T) {
	left := integrationDefinition("left", "left.example.com", "left-vdom", v1alpha1.OwnershipModeExclusive)
	right := integrationDefinition("right", "right.example.net", "right-vdom", v1alpha1.OwnershipModeExclusive)
	left.CARef = &v1alpha1.LocalKeyReference{Kind: "ConfigMap", Name: "left-ca", Key: "ca.crt"}
	definitions := []target.Definition{left, right}
	objects := secretsForDefinitions(definitions)
	objects = append(objects, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: left.Namespace, Name: "left-ca", UID: types.UID("left-ca-uid"), ResourceVersion: "1"},
		Data:       map[string]string{"ca.crt": "ca-one"},
	})
	clients := integrationKubernetes(t, objects, nil)
	factory := newIntegrationClientFactory()
	resources := newIntegrationResourceFactory(t, clients.Dynamic)
	resolver, _ := target.NewResolver(clients.Core.CoreV1())
	var enqueueMu sync.Mutex
	var enqueued []string
	manager, _ := target.NewRuntimeManager(resolver, factory, resources, metrics.New(), func(key string) {
		enqueueMu.Lock()
		enqueued = append(enqueued, key)
		enqueueMu.Unlock()
	})
	if _, err := manager.Sync(context.Background(), definitions); err != nil {
		t.Fatal(err)
	}

	secret, _ := clients.Core.CoreV1().Secrets(left.Namespace).Get(context.Background(), left.APITokenSecretRef.Name, metav1.GetOptions{})
	secret.Data[left.APITokenSecretRef.Key] = []byte("left-token-rotated")
	secret.ResourceVersion = "2"
	if _, err := clients.Core.CoreV1().Secrets(left.Namespace).Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Sync(context.Background(), definitions); err != nil {
		t.Fatal(err)
	}
	configMap, _ := clients.Core.CoreV1().ConfigMaps(left.Namespace).Get(context.Background(), "left-ca", metav1.GetOptions{})
	configMap.Data["ca.crt"] = "ca-two"
	configMap.ResourceVersion = "2"
	if _, err := clients.Core.CoreV1().ConfigMaps(left.Namespace).Update(context.Background(), configMap, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Sync(context.Background(), definitions); err != nil {
		t.Fatal(err)
	}
	if factory.callsFor(left.Key()) != 3 || factory.callsFor(right.Key()) != 1 {
		t.Fatalf("client rebuild counts left=%d right=%d", factory.callsFor(left.Key()), factory.callsFor(right.Key()))
	}
	if targets := manager.CredentialEvent("ConfigMap", left.Namespace, "left-ca"); len(targets) != 1 || targets[0] != left.Key() {
		t.Fatalf("CA event targets = %v", targets)
	}
	enqueueMu.Lock()
	defer enqueueMu.Unlock()
	if len(enqueued) < 5 {
		t.Fatalf("rotation events did not enqueue affected target: %v", enqueued)
	}
}

func TestPlatformPolicyDenialCannotBypassEmptyCleanupGuard(t *testing.T) {
	definition := integrationDefinition("edge", "example.com", "root", v1alpha1.OwnershipModeExclusive)
	service := integrationService("apps", "api", "api.example.com", "192.0.2.20")
	deny := &v1alpha1.FortiGateDNSPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: "FortiGateDNSPolicy"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "deny-all"},
		Spec:       v1alpha1.FortiGateDNSPolicySpec{Deny: true},
	}
	clients := integrationKubernetes(t, append([]runtime.Object{service}, secretsForDefinitions([]target.Definition{definition})...), []runtime.Object{deny})
	factory := newIntegrationClientFactory()
	manager := integrationManager(t, clients, []target.Definition{definition}, factory)
	provider := factory.provider(definition.Key())
	provider.records = []dns.Endpoint{{DNSName: "api.example.com", RecordType: dns.RecordA, Targets: []string{"192.0.2.20"}, TTL: 300, Zone: "example.com", ProviderID: "1"}}
	cfg := integrationConfig()
	cfg.PolicyEnforcement = true
	cfg.AllowEmptyDesiredCleanup = false
	targetRuntime, _ := manager.Runtime(definition.Key())
	if err := runTargetAudit(context.Background(), cfg, clients, targetRuntime, metrics.New(), discardLogger()); err != nil {
		t.Fatalf("denied audit error = %v", err)
	}
	if len(provider.snapshotRecords()) != 1 || provider.mutationCount() != 0 {
		t.Fatal("policy denial bypassed the cumulative empty-desired cleanup guard")
	}
	if err := clients.Dynamic.Resource(v1alpha1.PolicyGVR).Namespace("apps").Delete(context.Background(), "deny-all", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := runTargetAudit(context.Background(), cfg, clients, targetRuntime, metrics.New(), discardLogger()); err != nil {
		t.Fatalf("policy removal audit error = %v", err)
	}
	if len(provider.snapshotRecords()) != 1 || provider.mutationCount() != 0 {
		t.Fatal("policy removal caused destructive drift")
	}
}

func TestPlatformCRDApprovalMissingMismatchAndMatch(t *testing.T) {
	definition := integrationDefinition("approved", "approved.example.com", "root", v1alpha1.OwnershipModeExclusive)
	definition.ApprovalMode = v1alpha1.ApprovalModeRequired
	service := integrationService("apps", "approved", "api.approved.example.com", "192.0.2.30")
	clients := integrationKubernetes(t, append([]runtime.Object{service}, secretsForDefinitions([]target.Definition{definition})...), nil)
	factory := newIntegrationClientFactory()
	manager := integrationManager(t, clients, []target.Definition{definition}, factory)
	approvalRuntime, _ := manager.Runtime(definition.Key())
	cfg := integrationConfig()

	err := runTargetAudit(context.Background(), cfg, clients, approvalRuntime, metrics.New(), discardLogger())
	if err == nil || !strings.Contains(err.Error(), "approval is missing") {
		t.Fatalf("missing approval error = %v", err)
	}
	provider := factory.provider(definition.Key())
	if provider.mutationCount() != 0 {
		t.Fatal("missing approval allowed provider mutation")
	}
	plans := listChangePlans(t, clients.Dynamic, definition.Namespace)
	if len(plans) != 1 {
		t.Fatalf("pending plans = %#v", plans)
	}
	updatePlanApproval(t, clients.Dynamic, &plans[0], "wrong-hash")
	err = runTargetAudit(context.Background(), cfg, clients, approvalRuntime, metrics.New(), discardLogger())
	if err == nil || !strings.Contains(err.Error(), "does not match") || provider.mutationCount() != 0 {
		t.Fatalf("mismatched approval error=%v mutations=%d", err, provider.mutationCount())
	}
	plans = listChangePlans(t, clients.Dynamic, definition.Namespace)
	updatePlanApproval(t, clients.Dynamic, &plans[0], plans[0].Spec.PlanHash)
	if err := runTargetAudit(context.Background(), cfg, clients, approvalRuntime, metrics.New(), discardLogger()); err != nil {
		t.Fatalf("matching approval error = %v", err)
	}
	if provider.mutationCount() != 1 || len(provider.snapshotRecords()) != 1 {
		t.Fatalf("matching approval mutations=%d records=%#v", provider.mutationCount(), provider.snapshotRecords())
	}
}

func TestPlatformSharedCreateUpdateDeleteAndDryRunClaimSafety(t *testing.T) {
	definition := integrationDefinition("shared", "shared.example.com", "root", v1alpha1.OwnershipModeShared)
	service := integrationService("apps", "shared", "api.shared.example.com", "192.0.2.40")
	clients := integrationKubernetes(t, append([]runtime.Object{service}, secretsForDefinitions([]target.Definition{definition})...), nil)
	factory := newIntegrationClientFactory()
	resources := newIntegrationResourceFactory(t, clients.Dynamic)
	manager := integrationManagerWithResources(t, clients, []target.Definition{definition}, factory, resources)
	targetRuntime, _ := manager.Runtime(definition.Key())
	cfg := integrationConfig()
	cfg.AllowEmptyDesiredCleanup = true
	if err := runTargetAudit(context.Background(), cfg, clients, targetRuntime, metrics.New(), discardLogger()); err != nil {
		t.Fatalf("shared create error = %v", err)
	}
	provider := factory.provider(definition.Key())
	claims, _ := resources.repository(definition.Key()).List(context.Background())
	if len(provider.snapshotRecords()) != 1 || len(claims) != 1 || claims[0].Status.Phase != v1alpha1.OwnershipPhaseConfirmed {
		t.Fatalf("shared create records=%#v claims=%#v", provider.snapshotRecords(), claims)
	}
	targetRuntime.Definition.DefaultTTL = 600
	if err := runTargetAudit(context.Background(), cfg, clients, targetRuntime, metrics.New(), discardLogger()); err != nil {
		t.Fatalf("shared update error = %v", err)
	}
	if got := provider.snapshotRecords(); len(got) != 1 || got[0].TTL != 600 {
		t.Fatalf("shared update did not converge: %#v", got)
	}
	if err := clients.Core.CoreV1().Services("apps").Delete(context.Background(), "shared", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := runTargetAudit(context.Background(), cfg, clients, targetRuntime, metrics.New(), discardLogger()); err != nil {
		t.Fatalf("shared delete error = %v", err)
	}
	if len(provider.snapshotRecords()) != 0 {
		t.Fatalf("shared delete retained provider rows: %#v", provider.snapshotRecords())
	}

	dryDefinition := integrationDefinition("dry-shared", "dry.example.net", "root", v1alpha1.OwnershipModeShared)
	dryDefinition.DryRun = true
	dryService := integrationService("apps", "dry", "api.dry.example.net", "192.0.2.41")
	dryClients := integrationKubernetes(t, append([]runtime.Object{dryService}, secretsForDefinitions([]target.Definition{dryDefinition})...), nil)
	dryFactory := newIntegrationClientFactory()
	dryResources := newIntegrationResourceFactory(t, dryClients.Dynamic)
	dryManager := integrationManagerWithResources(t, dryClients, []target.Definition{dryDefinition}, dryFactory, dryResources)
	dryRuntime, _ := dryManager.Runtime(dryDefinition.Key())
	if err := runTargetAudit(context.Background(), integrationConfig(), dryClients, dryRuntime, metrics.New(), discardLogger()); err != nil {
		t.Fatalf("shared dry-run error = %v", err)
	}
	dryClaims, _ := dryResources.repository(dryDefinition.Key()).List(context.Background())
	if dryFactory.provider(dryDefinition.Key()).mutationCount() != 0 || len(dryClaims) != 0 {
		t.Fatalf("shared dry-run mutations=%d claims=%#v", dryFactory.provider(dryDefinition.Key()).mutationCount(), dryClaims)
	}
}

func TestPlatformDryRunAcrossExclusiveAndSharedMultiTarget(t *testing.T) {
	exclusive := integrationDefinition("dry-exclusive", "exclusive.example.com", "root", v1alpha1.OwnershipModeExclusive)
	shared := integrationDefinition("dry-shared", "shared.example.net", "root", v1alpha1.OwnershipModeShared)
	exclusive.DryRun = true
	shared.DryRun = true
	definitions := []target.Definition{exclusive, shared}
	objects := []runtime.Object{
		integrationService("apps", "exclusive", "api.exclusive.example.com", "192.0.2.51"),
		integrationService("apps", "shared", "api.shared.example.net", "192.0.2.52"),
	}
	objects = append(objects, secretsForDefinitions(definitions)...)
	clients := integrationKubernetes(t, objects, nil)
	factory := newIntegrationClientFactory()
	resources := newIntegrationResourceFactory(t, clients.Dynamic)
	manager := integrationManagerWithResources(t, clients, definitions, factory, resources)
	results := manager.RunAll(context.Background(), func(ctx context.Context, runtime *target.Runtime) error {
		return runTargetAudit(ctx, integrationConfig(), clients, runtime, metrics.New(), discardLogger())
	})
	for _, definition := range definitions {
		if !results[definition.Key()].Succeeded || factory.provider(definition.Key()).mutationCount() != 0 {
			t.Fatalf("dry target %q result=%#v mutations=%d", definition.Key(), results[definition.Key()], factory.provider(definition.Key()).mutationCount())
		}
	}
	claims, _ := resources.repository(shared.Key()).List(context.Background())
	if len(claims) != 0 {
		t.Fatalf("multi-target dry-run created claims: %#v", claims)
	}
}

func TestPlatformLeadershipCancellationStopsConcurrentTargetAudits(t *testing.T) {
	definitions := []target.Definition{
		integrationDefinition("left", "left.example.com", "left", v1alpha1.OwnershipModeExclusive),
		integrationDefinition("right", "right.example.net", "right", v1alpha1.OwnershipModeExclusive),
	}
	clients := integrationKubernetes(t, secretsForDefinitions(definitions), nil)
	factory := newIntegrationClientFactory()
	manager := integrationManager(t, clients, definitions, factory)
	for _, definition := range definitions {
		provider := factory.provider(definition.Key())
		provider.blockList = make(chan struct{})
		provider.listStarted = make(chan struct{}, 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan map[string]target.RunResult, 1)
	go func() {
		done <- manager.RunAll(ctx, func(runCtx context.Context, runtime *target.Runtime) error {
			return runTargetAudit(runCtx, integrationConfig(), clients, runtime, metrics.New(), discardLogger())
		})
	}()
	for _, definition := range definitions {
		select {
		case <-factory.provider(definition.Key()).listStarted:
		case <-time.After(2 * time.Second):
			t.Fatalf("target %q did not start provider audit", definition.Key())
		}
	}
	cancel()
	results := <-done
	for _, definition := range definitions {
		if results[definition.Key()].Reason != target.FailureCancelled || factory.provider(definition.Key()).mutationCount() != 0 {
			t.Fatalf("canceled target %q result=%#v mutations=%d", definition.Key(), results[definition.Key()], factory.provider(definition.Key()).mutationCount())
		}
	}
}

func TestPlatformConcurrentTargetEventClaimApprovalRotationAndStatus(t *testing.T) {
	definition := integrationDefinition("race-shared", "race.example.com", "root", v1alpha1.OwnershipModeShared)
	definition.ApprovalMode = v1alpha1.ApprovalModeRequired
	service := integrationService("apps", "race", "api.race.example.com", "192.0.2.70")
	clients := integrationKubernetes(t, append([]runtime.Object{service}, secretsForDefinitions([]target.Definition{definition})...), nil)
	factory := newIntegrationClientFactory()
	resources := newIntegrationResourceFactory(t, clients.Dynamic)
	resolver, _ := target.NewResolver(clients.Core.CoreV1())
	manager, _ := target.NewRuntimeManager(resolver, factory, resources, metrics.New(), func(string) {})
	if result, err := manager.Sync(context.Background(), []target.Definition{definition}); err != nil || len(result.Failures) != 0 {
		t.Fatalf("initial Sync() result=%#v err=%v", result, err)
	}
	targetRuntime, _ := manager.Runtime(definition.Key())
	_ = runTargetAudit(context.Background(), integrationConfig(), clients, targetRuntime, metrics.New(), discardLogger())
	plans := listChangePlans(t, clients.Dynamic, definition.Namespace)
	if len(plans) != 1 {
		t.Fatalf("pending race plan = %#v", plans)
	}
	updatePlanApproval(t, clients.Dynamic, &plans[0], plans[0].Spec.PlanHash)

	var wait sync.WaitGroup
	wait.Add(4)
	go func() {
		defer wait.Done()
		_ = manager.RunAll(context.Background(), func(ctx context.Context, runtime *target.Runtime) error {
			return runTargetAudit(ctx, integrationConfig(), clients, runtime, metrics.New(), discardLogger())
		})
	}()
	go func() {
		defer wait.Done()
		secret, err := clients.Core.CoreV1().Secrets(definition.Namespace).Get(context.Background(), definition.APITokenSecretRef.Name, metav1.GetOptions{})
		if err != nil {
			return
		}
		secret.Data[definition.APITokenSecretRef.Key] = []byte("rotated-race-token")
		secret.ResourceVersion = "2"
		_, _ = clients.Core.CoreV1().Secrets(definition.Namespace).Update(context.Background(), secret, metav1.UpdateOptions{})
		_, _ = manager.Sync(context.Background(), []target.Definition{definition})
	}()
	go func() {
		defer wait.Done()
		for range 20 {
			manager.CredentialEvent("Secret", definition.Namespace, definition.APITokenSecretRef.Name)
		}
	}()
	go func() {
		defer wait.Done()
		_ = manager.RunAll(context.Background(), func(ctx context.Context, runtime *target.Runtime) error {
			return runTargetAudit(ctx, integrationConfig(), clients, runtime, metrics.New(), discardLogger())
		})
	}()
	wait.Wait()

	if records := factory.provider(definition.Key()).snapshotRecords(); len(records) > 1 {
		t.Fatalf("concurrent approval/rotation created duplicate provider rows: %#v", records)
	}
	claims, err := resources.repository(definition.Key()).List(context.Background())
	if err != nil || len(claims) > 1 {
		t.Fatalf("concurrent claim state = %#v err=%v", claims, err)
	}
	statuses, err := clients.Dynamic.Resource(v1alpha1.StatusGVR).Namespace(definition.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(statuses.Items) > 1 {
		t.Fatalf("concurrent status writes = %d err=%v", len(statuses.Items), err)
	}
}

func integrationConfig() config.Config {
	return config.Config{
		DryRun: false, Sources: []string{"service"}, DefaultTTL: 300, OwnerID: "controller-a", CleanupPolicy: "delete",
		Interval: time.Minute, ReconcileTimeout: 5 * time.Second, StatusRetention: 10, Resync: time.Minute,
	}
}

func integrationDefinition(name, zone, vdom string, mode v1alpha1.OwnershipMode) target.Definition {
	return target.Definition{
		Namespace: "dns-system", Name: name, UID: "uid-" + name, Generation: 1,
		URL: "https://fortigate.example.com", VDOM: vdom, Zone: zone,
		APITokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name + "-token"}, Key: "api-token"},
		OwnershipMode:     mode, ControllerID: "controller-a", Sources: []string{"service"},
		DomainFilters: []string{zone}, CleanupPolicy: v1alpha1.CleanupPolicyDelete, ApprovalMode: v1alpha1.ApprovalModeDisabled,
		DefaultTTL: 300, Timeout: 5 * time.Second, Retries: 1,
	}
}

func integrationService(namespace, name, hostname, address string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: map[string]string{source.AnnotationHostname: hostname}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status:     corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: address}}}},
	}
}

func secretsForDefinitions(definitions []target.Definition) []runtime.Object {
	objects := make([]runtime.Object, 0, len(definitions))
	for _, definition := range definitions {
		objects = append(objects, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: definition.Namespace, Name: definition.APITokenSecretRef.Name, UID: types.UID("secret-" + definition.Name), ResourceVersion: "1"},
			Data:       map[string][]byte{definition.APITokenSecretRef.Key: []byte("token-" + definition.Name)},
		})
	}
	return objects
}

func integrationKubernetes(t *testing.T, coreObjects, dynamicObjects []runtime.Object) source.KubernetesClients {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	coreClient := fake.NewSimpleClientset(coreObjects...)
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, dynamicObjects...)
	return source.KubernetesClients{Core: coreClient, Dynamic: dynamicClient, EndpointSlices: coreClient.DiscoveryV1()}
}

func integrationManager(t *testing.T, clients source.KubernetesClients, definitions []target.Definition, factory *integrationClientFactory) *target.RuntimeManager {
	t.Helper()
	return integrationManagerWithResources(t, clients, definitions, factory, newIntegrationResourceFactory(t, clients.Dynamic))
}

func integrationManagerWithResources(t *testing.T, clients source.KubernetesClients, definitions []target.Definition, factory *integrationClientFactory, resources *integrationResourceFactory) *target.RuntimeManager {
	t.Helper()
	resolver, err := target.NewResolver(clients.Core.CoreV1())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := target.NewRuntimeManager(resolver, factory, resources, metrics.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Sync(context.Background(), definitions)
	if err != nil || len(result.Failures) != 0 {
		t.Fatalf("Sync() result=%#v err=%v", result, err)
	}
	return manager
}

type integrationResourceFactory struct {
	t       *testing.T
	dynamic dynamic.Interface
	mu      sync.Mutex
	repos   map[string]*ownership.Repository
}

func newIntegrationResourceFactory(t *testing.T, client dynamic.Interface) *integrationResourceFactory {
	return &integrationResourceFactory{t: t, dynamic: client, repos: map[string]*ownership.Repository{}}
}

func (f *integrationResourceFactory) NewResources(_ context.Context, definition target.Definition) (target.StoreHandles, error) {
	planStore, err := plan.NewChangePlanStore(f.dynamic)
	if err != nil {
		return target.StoreHandles{}, err
	}
	store := &sharedMemoryStore{objects: map[string]*v1alpha1.FortiGateDNSRecordOwnership{}}
	repository, err := ownership.NewRepository(store)
	if err != nil {
		return target.StoreHandles{}, err
	}
	manager, err := ownership.NewManager(repository)
	if err != nil {
		return target.StoreHandles{}, err
	}
	namespace := definition.Namespace
	if namespace == "" {
		namespace = "dns-system"
	}
	writer, err := statuswriter.NewWriter(f.dynamic, namespace, definition.Name, 10)
	if err != nil {
		return target.StoreHandles{}, err
	}
	f.mu.Lock()
	f.repos[definition.Key()] = repository
	f.mu.Unlock()
	return target.StoreHandles{
		PlanStore: planStore, OwnershipStore: &sharedOwnershipHandles{manager: manager, repository: repository}, StatusStore: writer,
	}, nil
}

func (f *integrationResourceFactory) repository(key string) *ownership.Repository {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repos[key]
}

type integrationClientFactory struct {
	mu          sync.Mutex
	providers   map[string]*integrationProvider
	definitions map[string]target.Definition
	calls       map[string]int
	failList    map[string]bool
}

func newIntegrationClientFactory() *integrationClientFactory {
	return &integrationClientFactory{
		providers: map[string]*integrationProvider{}, definitions: map[string]target.Definition{}, calls: map[string]int{}, failList: map[string]bool{},
	}
}

func (f *integrationClientFactory) NewClient(_ context.Context, definition target.Definition, material *target.CredentialMaterial) (target.ProviderClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := definition.Key()
	f.calls[key]++
	f.definitions[key] = definition
	// Exercise both accessors without retaining the sensitive values.
	secretDigest := sha256.Sum256(append(material.APIToken(), material.CABundle()...))
	_ = hex.EncodeToString(secretDigest[:])
	provider := f.providers[key]
	if provider == nil {
		provider = &integrationProvider{definition: definition, revision: 1, failList: f.failList[key]}
		f.providers[key] = provider
	} else {
		provider.mu.Lock()
		provider.definition = definition
		provider.failList = f.failList[key]
		provider.mu.Unlock()
	}
	return provider, nil
}

func (f *integrationClientFactory) provider(key string) *integrationProvider {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.providers[key]
}

func (f *integrationClientFactory) definition(key string) target.Definition {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.definitions[key]
}

func (f *integrationClientFactory) callsFor(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[key]
}

type integrationProvider struct {
	mu          sync.Mutex
	definition  target.Definition
	revision    int
	records     []dns.Endpoint
	mutations   int
	dryRuns     int
	failList    bool
	blockList   chan struct{}
	listStarted chan struct{}
}

func (p *integrationProvider) ListRecords(ctx context.Context) ([]dns.Endpoint, error) {
	p.mu.Lock()
	fail := p.failList
	block := p.blockList
	started := p.listStarted
	p.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail {
		return nil, errors.New("provider unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return providerCopy(p.records), nil
}

func (p *integrationProvider) ListRecordsWithRevision(ctx context.Context) ([]dns.Endpoint, string, error) {
	records, err := p.ListRecords(ctx)
	if err != nil {
		return nil, "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return records, "revision-" + strconv.Itoa(p.revision), nil
}

func (p *integrationProvider) Apply(_ context.Context, operations []plan.Operation, dryRun bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if dryRun {
		p.dryRuns++
		return nil
	}
	for _, operation := range operations {
		switch operation.Type {
		case plan.OperationCreate:
			created := operation.Desired.Normalize()
			created.OwnerID = ""
			created.ProviderID = strconv.Itoa(len(p.records) + 1)
			p.records = append(p.records, created)
		case plan.OperationUpdate, plan.OperationDeactivate, plan.OperationReplace:
			for index := range p.records {
				if p.records[index].ProviderID == operation.Current.ProviderID {
					updated := operation.Desired.Normalize()
					updated.OwnerID = ""
					updated.ProviderID = operation.Current.ProviderID
					p.records[index] = updated
				}
			}
		case plan.OperationDelete:
			for index := range p.records {
				if p.records[index].ProviderID == operation.Current.ProviderID {
					p.records = append(p.records[:index], p.records[index+1:]...)
					break
				}
			}
		case plan.OperationConflict:
			continue
		default:
			continue
		}
		p.mutations++
		p.revision++
	}
	return nil
}

func (p *integrationProvider) snapshotRecords() []dns.Endpoint {
	p.mu.Lock()
	defer p.mu.Unlock()
	return providerCopy(p.records)
}

func (p *integrationProvider) mutationCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mutations
}

func providerCopy(records []dns.Endpoint) []dns.Endpoint {
	result := make([]dns.Endpoint, len(records))
	for index := range records {
		result[index] = records[index]
		result[index].Targets = append([]string(nil), records[index].Targets...)
		// FortiGate does not persist controller ownership metadata.
		result[index].OwnerID = ""
	}
	return result
}

func listChangePlans(t *testing.T, client dynamic.Interface, namespace string) []v1alpha1.FortiGateDNSChangePlan {
	t.Helper()
	list, err := client.Resource(v1alpha1.ChangePlanGVR).Namespace(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result := make([]v1alpha1.FortiGateDNSChangePlan, 0, len(list.Items))
	for index := range list.Items {
		var object v1alpha1.FortiGateDNSChangePlan
		if err := v1alpha1.FromUnstructured(&list.Items[index], &object); err != nil {
			t.Fatal(err)
		}
		result = append(result, object)
	}
	return result
}

func updatePlanApproval(t *testing.T, client dynamic.Interface, object *v1alpha1.FortiGateDNSChangePlan, approved string) {
	t.Helper()
	current, err := client.Resource(v1alpha1.ChangePlanGVR).Namespace(object.Namespace).Get(context.Background(), object.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if current.GetAnnotations() == nil {
		current.SetAnnotations(map[string]string{})
	}
	annotations := current.GetAnnotations()
	annotations[v1alpha1.ApprovalHashAnnotation] = approved
	current.SetAnnotations(annotations)
	if _, err := client.Resource(v1alpha1.ChangePlanGVR).Namespace(object.Namespace).Update(context.Background(), current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

var _ target.ProviderClient = (*integrationProvider)(nil)
var _ revisionedTargetClient = (*integrationProvider)(nil)
var _ target.ClientFactory = (*integrationClientFactory)(nil)
var _ target.ResourceFactory = (*integrationResourceFactory)(nil)
