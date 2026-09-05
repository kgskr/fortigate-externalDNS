package controller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	clocktesting "k8s.io/utils/clock/testing"
	gatewayfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

func TestPlatformRuntimeWiresInformerEventsPeriodicAuditAndTargetDelete(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(time.Unix(0, 0))
	target := newAPITarget("dns-system", "edge", []string{"apps"})
	clients, kubeClient, dynamicClient := newPlatformTestClients(t, target)
	executor := newRecordingExecutor()
	runtime := newTestPlatformRuntime(t, clients, fakeClock, executor)
	wantKinds := map[platformqueue.EventKind]bool{
		platformqueue.EventService: true, platformqueue.EventIngress: true, platformqueue.EventGateway: true,
		platformqueue.EventHTTPRoute: true, platformqueue.EventEndpointSlice: true, platformqueue.EventTarget: true,
		platformqueue.EventPolicy: true, platformqueue.EventOwnership: true, platformqueue.EventChangePlan: true,
	}
	for _, binding := range runtime.factories.bindings {
		delete(wantKinds, binding.kind)
	}
	if len(wantKinds) != 0 {
		t.Fatalf("missing informer bindings: %v", wantKinds)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runPlatformRuntime(runtime, ctx)

	if key := receiveKey(t, executor.applied); key.String() != "dns-system/edge" {
		t.Fatalf("initial target audit key = %s", key)
	}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", Annotations: map[string]string{"external-dns.alpha.kubernetes.io/hostname": "api.example.com"}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	if _, err := kubeClient.CoreV1().Services("apps").Create(ctx, service, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if key := receiveKey(t, executor.applied); key.String() != "dns-system/edge" {
		t.Fatalf("Service event target = %s", key)
	}
	waitUntil(t, func() bool { return fakeClock.Waiters() >= 2 })
	fakeClock.Step(time.Minute)
	if key := receiveKey(t, executor.applied); key.String() != "dns-system/edge" {
		t.Fatalf("periodic audit target = %s", key)
	}

	if err := dynamicClient.Resource(api.TargetGVR).Namespace("dns-system").Delete(ctx, "edge", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	key, err := platformqueue.NewTargetKey("dns-system", "edge")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return !runtime.mapper.HasTarget(key) && runtime.queue.IsTargetForgotten(key) })
	if runtime.queue.EnqueuePeriodic(key) {
		t.Fatal("deleted target still accepted periodic work")
	}

	cancel()
	if err := receiveRunResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformRuntimeBuildsOnlyAuthorizedActiveInformerCaches(t *testing.T) {
	clients, kubeClient, _ := newPlatformTestClients(t, newAPITarget("dns-system", "edge", []string{"apps"}))
	clients.Gateway = nil
	runtime, err := NewPlatformRuntime(clients, PlatformRuntimeConfig{
		Namespace: "dns-system", Sources: []string{"service"}, SourceNamespaces: []string{"apps"},
		PeriodicInterval: time.Minute,
	}, newRecordingExecutor())
	if err != nil {
		t.Fatal(err)
	}
	want := map[platformqueue.EventKind]int{platformqueue.EventService: 1, platformqueue.EventTarget: 1}
	for _, binding := range runtime.factories.bindings {
		want[binding.kind]--
		if want[binding.kind] < 0 {
			t.Fatalf("unexpected mandatory informer cache %q", binding.kind)
		}
	}
	for kind, remaining := range want {
		if remaining != 0 {
			t.Fatalf("active informer cache %q count = %d", kind, 1-remaining)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runPlatformRuntime(runtime, ctx)
	receiveKey(t, runtime.executor.(*recordingExecutor).applied)
	for _, action := range kubeClient.Actions() {
		if resource := action.GetResource().Resource; resource != "services" {
			t.Fatalf("disabled or credential resource requested by informer: %s", resource)
		}
		if namespace := action.GetNamespace(); namespace != "apps" {
			t.Fatalf("source informer request namespace = %q", namespace)
		}
	}
	cancel()
	if err := receiveRunResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformRuntimeExitsSoInformerScopeCanBeRebuilt(t *testing.T) {
	clients, _, _ := newPlatformTestClients(t, newAPITarget("dns-system", "edge", []string{"apps"}))
	executor := newRecordingExecutor()
	executor.audit = func(context.Context, platformqueue.TargetKey) (TargetAudit, error) {
		return TargetAudit{}, ErrPlatformInformerScopeChanged
	}
	runtime := newTestPlatformRuntime(t, clients, clocktesting.NewFakeClock(time.Unix(0, 0)), executor)
	err := receiveRunResult(t, runPlatformRuntime(runtime, context.Background()))
	if !errors.Is(err, ErrPlatformInformerScopeChanged) {
		t.Fatalf("scope change error = %v", err)
	}

	updatedClients, kubeClient, _ := newPlatformTestClients(t, newAPITarget("dns-system", "edge", []string{"team-b"}))
	updatedExecutor := newRecordingExecutor()
	updated, err := NewPlatformRuntime(updatedClients, PlatformRuntimeConfig{
		Namespace: "dns-system", Sources: []string{"ingress"}, SourceNamespaces: []string{"team-b"}, PeriodicInterval: time.Minute,
	}, updatedExecutor)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runPlatformRuntime(updated, ctx)
	receiveKey(t, updatedExecutor.applied)
	for _, action := range kubeClient.Actions() {
		if action.GetResource().Resource != "ingresses" || action.GetNamespace() != "team-b" {
			t.Fatalf("rebuilt informer request = %s namespace %q", action.GetResource().Resource, action.GetNamespace())
		}
	}
	cancel()
	if err := receiveRunResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformRuntimeLeadershipLossCancelsInFlightApply(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(time.Unix(0, 0))
	clients, _, _ := newPlatformTestClients(t, newAPITarget("dns-system", "edge", nil))
	started := make(chan struct{})
	exited := make(chan error, 1)
	var applies atomic.Int32
	executor := &recordingExecutor{
		audit: func(context.Context, platformqueue.TargetKey) (TargetAudit, error) {
			return TargetAudit{CleanupCapable: true, DiscoveryComplete: true, ProviderSnapshotStable: true}, nil
		},
		apply: func(ctx context.Context, _ platformqueue.TargetKey, _ TargetAudit) error {
			if applies.Add(1) == 1 {
				close(started)
			}
			<-ctx.Done()
			exited <- ctx.Err()
			return ctx.Err()
		},
	}
	runtime := newTestPlatformRuntime(t, clients, fakeClock, executor)
	ctx, cancel := context.WithCancel(context.Background())
	done := runPlatformRuntime(runtime, ctx)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider apply did not start")
	}
	cancel()
	if err := receiveRunResult(t, done); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("apply cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight apply did not observe leadership cancellation")
	}
	if applies.Load() != 1 {
		t.Fatalf("provider apply calls after leadership loss = %d", applies.Load())
	}
}

func TestPlatformRuntimeCleanupGateBlocksIncompleteAudit(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(time.Unix(0, 0))
	clients, _, _ := newPlatformTestClients(t, newAPITarget("dns-system", "edge", nil))
	audited := make(chan struct{}, 1)
	var applies atomic.Int32
	executor := &recordingExecutor{
		audit: func(context.Context, platformqueue.TargetKey) (TargetAudit, error) {
			audited <- struct{}{}
			return TargetAudit{CleanupCapable: true, DiscoveryComplete: false, ProviderSnapshotStable: true}, nil
		},
		apply: func(context.Context, platformqueue.TargetKey, TargetAudit) error {
			applies.Add(1)
			return nil
		},
	}
	runtime := newTestPlatformRuntime(t, clients, fakeClock, executor)
	ctx, cancel := context.WithCancel(context.Background())
	done := runPlatformRuntime(runtime, ctx)
	select {
	case <-audited:
	case <-time.After(3 * time.Second):
		t.Fatal("target audit did not run")
	}
	if applies.Load() != 0 {
		t.Fatal("incomplete full audit reached provider mutation")
	}
	cancel()
	if err := receiveRunResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformRuntimeDoesNotMutateBeforeTargetCacheSync(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(time.Unix(0, 0))
	clients, _, dynamicClient := newPlatformTestClients(t, newAPITarget("dns-system", "edge", nil))
	listAttempted := make(chan struct{}, 1)
	dynamicClient.PrependReactor("list", "fortigatednstargets", func(ktesting.Action) (bool, runtime.Object, error) {
		select {
		case listAttempted <- struct{}{}:
		default:
		}
		return true, nil, errors.New("target API unavailable")
	})
	executor := newRecordingExecutor()
	runtime := newTestPlatformRuntime(t, clients, fakeClock, executor)
	ctx, cancel := context.WithCancel(context.Background())
	done := runPlatformRuntime(runtime, ctx)
	select {
	case <-listAttempted:
	case <-time.After(3 * time.Second):
		t.Fatal("target informer did not attempt initial list")
	}
	cancel()
	if err := receiveRunResult(t, done); err != nil {
		t.Fatal(err)
	}
	if executor.applyCalls.Load() != 0 {
		t.Fatalf("provider mutation ran before cache synchronization: %d", executor.applyCalls.Load())
	}
}

type recordingExecutor struct {
	audit      func(context.Context, platformqueue.TargetKey) (TargetAudit, error)
	apply      func(context.Context, platformqueue.TargetKey, TargetAudit) error
	applied    chan platformqueue.TargetKey
	applyCalls atomic.Int32
}

func newRecordingExecutor() *recordingExecutor {
	result := &recordingExecutor{applied: make(chan platformqueue.TargetKey, 32)}
	result.audit = func(context.Context, platformqueue.TargetKey) (TargetAudit, error) {
		return TargetAudit{CleanupCapable: true, DiscoveryComplete: true, ProviderSnapshotStable: true}, nil
	}
	result.apply = func(_ context.Context, key platformqueue.TargetKey, _ TargetAudit) error {
		result.applied <- key
		return nil
	}
	return result
}

func (e *recordingExecutor) Audit(ctx context.Context, key platformqueue.TargetKey) (TargetAudit, error) {
	return e.audit(ctx, key)
}

func (e *recordingExecutor) Apply(ctx context.Context, key platformqueue.TargetKey, audit TargetAudit) error {
	e.applyCalls.Add(1)
	return e.apply(ctx, key, audit)
}

func newAPITarget(namespace, name string, namespaces []string) *api.FortiGateDNSTarget {
	optional := false
	return &api.FortiGateDNSTarget{
		TypeMeta:   metav1.TypeMeta{APIVersion: api.SchemeGroupVersion.String(), Kind: "FortiGateDNSTarget"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: api.FortiGateDNSTargetSpec{
			URL: "https://fortigate.example.com", Zone: "example.com",
			APITokenSecretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "fortigate-token"}, Key: "token", Optional: &optional,
			},
			OwnershipMode: api.OwnershipModeExclusive, ControllerID: "controller-a",
			Sources: []string{"service", "ingress", "gateway"}, Namespaces: append([]string(nil), namespaces...),
			CleanupPolicy: api.CleanupPolicyDelete,
		},
	}
}

func newPlatformTestClients(t *testing.T, targets ...*api.FortiGateDNSTarget) (PlatformClients, *kubefake.Clientset, *fake.FakeDynamicClient) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := make([]runtime.Object, 0, len(targets))
	for _, target := range targets {
		objects = append(objects, target)
	}
	dynamicClient := fake.NewSimpleDynamicClient(scheme, objects...)
	kubeClient := kubefake.NewSimpleClientset()
	return PlatformClients{
		Kubernetes: kubeClient, Gateway: gatewayfake.NewSimpleClientset(), Dynamic: dynamicClient,
	}, kubeClient, dynamicClient
}

func newTestPlatformRuntime(t *testing.T, clients PlatformClients, fakeClock *clocktesting.FakeClock, executor TargetExecutor) *PlatformRuntime {
	t.Helper()
	runtime, err := NewPlatformRuntime(clients, PlatformRuntimeConfig{
		Namespace: "dns-system", Sources: []string{"service", "ingress", "gateway"}, SourceNamespaces: []string{"apps"},
		Headless: true, Policy: true, Ownership: true, PlanApproval: true,
		PeriodicInterval: time.Minute, Workers: 2, Clock: fakeClock,
		Queue: platformqueue.Config{
			DisableDebounce: true, RetryBase: time.Second, RetryMax: time.Minute,
			MaxRetries: 3, Jitter: 0.01, Clock: fakeClock,
		},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func runPlatformRuntime(runtime *PlatformRuntime, ctx context.Context) <-chan error {
	result := make(chan error, 1)
	go func() { result <- runtime.Run(ctx) }()
	return result
}

func receiveKey(t *testing.T, values <-chan platformqueue.TargetKey) platformqueue.TargetKey {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for target apply")
		return platformqueue.TargetKey{}
	}
}

func receiveRunResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("platform runtime did not stop")
		return nil
	}
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
