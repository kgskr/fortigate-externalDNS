package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/controller"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	"github.com/kgskr/fortigate-external-dns/internal/source"
	statuswriter "github.com/kgskr/fortigate-external-dns/internal/status"
	"github.com/kgskr/fortigate-external-dns/internal/target"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
)

// crTarget builds a FortiGateDNSTarget CR as the dynamic client returns it.
func crTarget(t *testing.T, name, zone string, filters []string, mutate func(*v1alpha1.FortiGateDNSTarget)) *unstructured.Unstructured {
	t.Helper()
	object := &v1alpha1.FortiGateDNSTarget{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: "FortiGateDNSTarget"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: name, UID: types.UID("uid-" + name), Generation: 3},
		Spec: v1alpha1.FortiGateDNSTargetSpec{
			URL: "https://fortigate.example.com", VDOM: "root", Zone: zone,
			APITokenSecretRef: corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name + "-token"}, Key: "api-token"},
			OwnershipMode:     v1alpha1.OwnershipModeExclusive, ControllerID: "controller-a", Sources: []string{"service"},
			DomainFilters: filters, CleanupPolicy: v1alpha1.CleanupPolicyDelete,
		},
	}
	if mutate != nil {
		mutate(object)
	}
	converted, err := v1alpha1.ToUnstructured(object)
	if err != nil {
		t.Fatal(err)
	}
	return converted
}

func tokenSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: name + "-token", UID: types.UID("secret-" + name), ResourceVersion: "1"},
		Data:       map[string][]byte{"api-token": []byte("token-" + name)},
	}
}

func isolationConfig() config.Config {
	cfg := integrationConfig()
	cfg.TargetMode = true
	cfg.PlatformNamespace = "dns-system"
	return cfg
}

func metricsText(m *metrics.Metrics) string {
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	return recorder.Body.String()
}

func assertTargetReadiness(t *testing.T, recorder *metrics.Metrics, key string, ready bool) {
	t.Helper()
	want := 0
	if ready {
		want = 1
	}
	line := fmt.Sprintf("fortigate_external_dns_target_ready{target=%q} %d\n", key, want)
	if body := metricsText(recorder); !strings.Contains(body, line) {
		t.Fatalf("missing readiness metric %q in %s", line, body)
	}
}

var lastSuccessPattern = regexp.MustCompile(`last_successful_reconcile_timestamp_seconds (\d+)`)

func lastSuccessSeconds(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	match := lastSuccessPattern.FindStringSubmatch(metricsText(m))
	if match == nil {
		t.Fatal("last successful reconcile metric missing")
	}
	return match[1]
}

func statusReadyReason(t *testing.T, clients source.KubernetesClients, name string) (metav1.ConditionStatus, string) {
	t.Helper()
	object, err := clients.Dynamic.Resource(v1alpha1.StatusGVR).Namespace("dns-system").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("status for %s: %v", name, err)
	}
	var status v1alpha1.FortiGateDNSStatus
	if err := v1alpha1.FromUnstructured(object, &status); err != nil {
		t.Fatal(err)
	}
	for _, condition := range status.Status.Conditions {
		if condition.Type == string(statuswriter.ConditionReady) {
			return condition.Status, condition.Reason
		}
	}
	t.Fatalf("status for %s has no Ready condition", name)
	return "", ""
}

// failingClientFactory fails client construction for chosen target keys.
type failingClientFactory struct {
	inner target.ClientFactory
	fail  map[string]bool
}

func (f failingClientFactory) NewClient(ctx context.Context, definition target.Definition, material *target.CredentialMaterial) (target.ProviderClient, error) {
	if f.fail[definition.Key()] {
		return nil, errors.New("CA bundle contains no PEM certificates")
	}
	return f.inner.NewClient(ctx, definition, material)
}

func isolationManager(t *testing.T, clients source.KubernetesClients, factory target.ClientFactory, logger *slog.Logger, recorder *metrics.Metrics) *target.RuntimeManager {
	t.Helper()
	resolver, err := target.NewResolver(clients.Core.CoreV1())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := target.NewRuntimeManager(resolver, factory, newIntegrationResourceFactory(t, clients.Dynamic), recorder, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetLogger(logger)
	return manager
}

func TestLoadTargetDefinitionsIsolatesInvalidAndConflictingTargets(t *testing.T) {
	clients := integrationKubernetes(t, nil, []runtime.Object{
		crTarget(t, "good", "good.example.com", []string{"good.example.com"}, nil),
		crTarget(t, "bad-url", "bad.example.com", nil, func(o *v1alpha1.FortiGateDNSTarget) { o.Spec.URL = "http://cleartext" }),
		crTarget(t, "foreign-filter", "example.com", []string{"other.org"}, nil),
		crTarget(t, "left", "shared.example.com", []string{"shared.example.com"}, nil),
		crTarget(t, "right", "shared.example.com", []string{"api.shared.example.com"}, nil),
	})
	load, err := loadTargetDefinitions(context.Background(), isolationConfig(), clients)
	if err != nil {
		t.Fatalf("invalid targets must not fail the load: %v", err)
	}
	if len(load.definitions) != 1 || load.definitions[0].Name != "good" {
		t.Fatalf("definitions = %#v", load.definitions)
	}
	want := map[string]target.FailureReason{
		"dns-system/bad-url": target.FailureInvalid, "dns-system/foreign-filter": target.FailureInvalid,
		"dns-system/left": target.FailureConflict, "dns-system/right": target.FailureConflict,
	}
	if len(load.invalid) != len(want) {
		t.Fatalf("invalid = %#v", load.invalid)
	}
	for key, reason := range want {
		if load.invalid[key].Reason != reason {
			t.Fatalf("%s reason = %q, want %q", key, load.invalid[key].Reason, reason)
		}
	}
}

func TestLoadTargetDefinitionsListErrorIsReturned(t *testing.T) {
	clients := integrationKubernetes(t, nil, nil)
	fakeDynamic := clients.Dynamic.(interface {
		PrependReactor(string, string, k8stesting.ReactionFunc)
	})
	fakeDynamic.PrependReactor("list", "fortigatednstargets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	if _, err := loadTargetDefinitions(context.Background(), isolationConfig(), clients); err == nil {
		t.Fatal("LIST failure must be reported to the caller")
	}
}

func TestSyncTargetsKeepsHealthySiblingsAndWritesFailureStatus(t *testing.T) {
	clients := integrationKubernetes(t, []runtime.Object{tokenSecret("good"), tokenSecret("bad-client"), tokenSecret("bad-url")}, []runtime.Object{
		crTarget(t, "good", "good.example.com", []string{"good.example.com"}, nil),
		crTarget(t, "bad-url", "bad.example.com", nil, func(o *v1alpha1.FortiGateDNSTarget) { o.Spec.URL = "http://cleartext" }),
		crTarget(t, "bad-client", "client.example.com", nil, nil),
		crTarget(t, "no-secret", "nosecret.example.com", nil, nil),
	})
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	recorder := metrics.New()
	factory := failingClientFactory{inner: newIntegrationClientFactory(), fail: map[string]bool{"dns-system/bad-client": true}}
	manager := isolationManager(t, clients, factory, logger, recorder)
	cfg := isolationConfig()
	load, err := loadTargetDefinitions(context.Background(), cfg, clients)
	if err != nil {
		t.Fatal(err)
	}

	result, err := syncTargets(context.Background(), cfg, clients, manager, recorder, logger, load, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Ready) != 1 || result.Ready[0] != "dns-system/good" {
		t.Fatalf("ready = %#v", result.Ready)
	}
	wantFailures := map[string]target.FailureReason{
		"dns-system/bad-url": target.FailureInvalid, "dns-system/bad-client": target.FailureClient, "dns-system/no-secret": target.FailureCredentials,
	}
	for key, reason := range wantFailures {
		if result.Failures[key] != reason {
			t.Fatalf("%s failure = %q, want %q", key, result.Failures[key], reason)
		}
	}
	if result.CredentialReasons["dns-system/no-secret"] != target.CredentialSecretUnavailable {
		t.Fatalf("credential sub-reason = %#v", result.CredentialReasons)
	}
	// Status is written for failures even though no runtime (and no status
	// store) exists for them.
	for name, want := range map[string]string{
		"bad-url": string(statuswriter.ReasonInvalidConfiguration), "bad-client": string(statuswriter.ReasonInvalidConfiguration),
		"no-secret": string(statuswriter.ReasonCredentialsUnavailable),
	} {
		if status, reason := statusReadyReason(t, clients, name); status != metav1.ConditionFalse || reason != want {
			t.Fatalf("%s Ready = %s/%s, want False/%s", name, status, reason, want)
		}
	}
	if !strings.Contains(logs.String(), "CA bundle contains no PEM certificates") || !strings.Contains(logs.String(), "credentialReason=secret-unavailable") {
		t.Fatalf("setup diagnostics missing from log: %s", logs.String())
	}
	if strings.Contains(logs.String(), "token-good") {
		t.Fatalf("log leaked a token: %s", logs.String())
	}
	if text := metricsText(recorder); !strings.Contains(text, `target="dns-system/bad-url"} 0`) || !strings.Contains(text, `fortigate_external_dns_target_ready{target="dns-system/good"} 0`) {
		t.Fatalf("target readiness metrics missing: %s", text)
	}
}

func TestSyncTargetsConflictMarksBothSidesAndKeepsSibling(t *testing.T) {
	clients := integrationKubernetes(t, []runtime.Object{tokenSecret("other")}, []runtime.Object{
		crTarget(t, "left", "shared.example.com", []string{"shared.example.com"}, nil),
		crTarget(t, "right", "shared.example.com", []string{"api.shared.example.com"}, nil),
		crTarget(t, "other", "other.example.net", nil, nil),
	})
	logger := discardLogger()
	recorder := metrics.New()
	manager := isolationManager(t, clients, newIntegrationClientFactory(), logger, recorder)
	load, _ := loadTargetDefinitions(context.Background(), isolationConfig(), clients)
	result, err := syncTargets(context.Background(), isolationConfig(), clients, manager, recorder, logger, load, "")
	if err != nil || len(result.Ready) != 1 || result.Ready[0] != "dns-system/other" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, name := range []string{"left", "right"} {
		if status, reason := statusReadyReason(t, clients, name); status != metav1.ConditionFalse || reason != string(statuswriter.ReasonOwnershipConflict) {
			t.Fatalf("%s Ready = %s/%s", name, status, reason)
		}
	}
}

func eventExecutorFor(t *testing.T, clients source.KubernetesClients, cfg config.Config, manager *target.RuntimeManager, recorder *metrics.Metrics, heartbeat *controller.Heartbeat) eventTargetExecutor {
	t.Helper()
	load, err := loadTargetDefinitions(context.Background(), cfg, clients)
	if err != nil {
		t.Fatal(err)
	}
	return eventTargetExecutor{
		cfg: cfg, clients: clients, manager: manager, recorder: recorder, logger: discardLogger(), heartbeat: heartbeat,
		scope: platformEventScope(cfg, load.definitions),
	}
}

func heartbeatMarked(heartbeat *controller.Heartbeat) bool {
	// Activated a while ago; a fresh attempt makes a tiny window healthy again.
	return heartbeat.Healthy(2 * time.Second)
}

func newStaleHeartbeat(t *testing.T) *controller.Heartbeat {
	t.Helper()
	heartbeat := controller.NewHeartbeat()
	heartbeat.SetActive(true)
	time.Sleep(60 * time.Millisecond)
	if heartbeat.Healthy(30 * time.Millisecond) {
		t.Fatal("test heartbeat should start stale")
	}
	return heartbeat
}

func TestEventAuditFailsOnlyForInvalidKeyAndAlwaysMarksAttempt(t *testing.T) {
	clients := integrationKubernetes(t, []runtime.Object{tokenSecret("good"), tokenSecret("broken")}, []runtime.Object{
		crTarget(t, "good", "good.example.com", []string{"good.example.com"}, nil),
		crTarget(t, "invalid", "bad.example.com", nil, func(o *v1alpha1.FortiGateDNSTarget) { o.Spec.URL = "http://cleartext" }),
		crTarget(t, "broken", "broken.example.com", nil, nil),
	})
	cfg := isolationConfig()
	recorder := metrics.New()
	factory := newIntegrationClientFactory()
	factory.failList["dns-system/broken"] = true
	manager := isolationManager(t, clients, factory, discardLogger(), recorder)
	heartbeat := newStaleHeartbeat(t)
	executor := eventExecutorFor(t, clients, cfg, manager, recorder, heartbeat)
	key := func(name string) platformqueue.TargetKey {
		k, err := platformqueue.NewTargetKey("dns-system", name)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}

	before := lastSuccessSeconds(t, recorder)
	if before != "0" {
		t.Fatalf("no reconcile has happened yet, last success = %s", before)
	}
	// Invalid key: fails with its fixed reason, and still counts as an attempt.
	_, err := executor.Audit(context.Background(), key("invalid"))
	if reason, ok := targetErrorReason(err); !ok || reason != target.FailureInvalid {
		t.Fatalf("invalid target audit error = %v", err)
	}
	if !heartbeatMarked(heartbeat) {
		t.Fatal("failed audit did not mark a heartbeat attempt")
	}

	// Unreachable provider: audit errors, attempt still marked.
	heartbeat = newStaleHeartbeat(t)
	executor.heartbeat = heartbeat
	if _, err := executor.Audit(context.Background(), key("broken")); err == nil {
		t.Fatal("provider outage should fail the audit")
	}
	assertTargetReadiness(t, recorder, "dns-system/broken", false)
	if !heartbeatMarked(heartbeat) {
		t.Fatal("provider failure did not mark a heartbeat attempt")
	}

	// Healthy sibling is unaffected by the invalid and broken ones.
	heartbeat = newStaleHeartbeat(t)
	executor.heartbeat = heartbeat
	audit, err := executor.Audit(context.Background(), key("good"))
	if err != nil {
		t.Fatalf("healthy target audit failed because of a sibling: %v", err)
	}
	if err := executor.Apply(context.Background(), key("good"), audit); err != nil {
		t.Fatal(err)
	}
	assertTargetReadiness(t, recorder, "dns-system/good", true)
	if !heartbeatMarked(heartbeat) {
		t.Fatal("successful reconcile did not mark a heartbeat attempt")
	}
	// The reconcile metric now records the success (it stayed 0 forever before).
	if after := lastSuccessSeconds(t, recorder); after == "0" {
		t.Fatal("event-mode reconcile did not update last_successful_reconcile_timestamp_seconds")
	}
	if text := metricsText(recorder); !strings.Contains(text, "reconcile_errors_total 2") {
		t.Fatalf("failed audits were not recorded as reconcile errors: %s", text)
	}
}

func TestPollingCycleRecordsReconcileMetricPerTarget(t *testing.T) {
	clients := integrationKubernetes(t, []runtime.Object{tokenSecret("good"), tokenSecret("broken")}, []runtime.Object{
		crTarget(t, "good", "good.example.com", []string{"good.example.com"}, nil),
		crTarget(t, "broken", "broken.example.com", nil, nil),
		crTarget(t, "invalid", "bad.example.com", nil, func(o *v1alpha1.FortiGateDNSTarget) { o.Spec.URL = "http://cleartext" }),
	})
	cfg := isolationConfig()
	recorder := metrics.New()
	factory := newIntegrationClientFactory()
	factory.failList["dns-system/broken"] = true
	manager := isolationManager(t, clients, factory, discardLogger(), recorder)

	err := runTargetCycle(context.Background(), cfg, clients, manager, recorder, discardLogger())
	if err == nil {
		t.Fatal("a cycle with failing targets must report failure for --once")
	}
	if lastSuccessSeconds(t, recorder) == "0" {
		t.Fatal("healthy target's audit did not update last_successful_reconcile_timestamp_seconds")
	}
	if !strings.Contains(metricsText(recorder), "reconcile_total 2") {
		t.Fatalf("expected one reconcile per runnable target: %s", metricsText(recorder))
	}
}

func TestPollingModeSurvivesListErrorsAndKeepsHeartbeat(t *testing.T) {
	clients := integrationKubernetes(t, nil, nil)
	fakeDynamic := clients.Dynamic.(interface {
		PrependReactor(string, string, k8stesting.ReactionFunc)
	})
	fakeDynamic.PrependReactor("list", "fortigatednstargets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	cfg := isolationConfig()
	cfg.Resync = 10 * time.Millisecond
	heartbeat := newStaleHeartbeat(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runTargetMode(ctx, cfg, clients, metrics.New(), discardLogger(), heartbeat) }()

	deadline := time.Now().Add(2 * time.Second)
	for !heartbeat.Healthy(30 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("failed cycles did not mark heartbeat attempts")
		}
		select {
		case err := <-done:
			t.Fatalf("polling mode exited on a transient list error: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("runTargetMode() = %v, want context canceled", err)
	}
}

func TestPollingOnceReturnsListError(t *testing.T) {
	clients := integrationKubernetes(t, nil, nil)
	fakeDynamic := clients.Dynamic.(interface {
		PrependReactor(string, string, k8stesting.ReactionFunc)
	})
	fakeDynamic.PrependReactor("list", "fortigatednstargets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	cfg := isolationConfig()
	cfg.Once = true
	if err := runTargetMode(context.Background(), cfg, clients, metrics.New(), discardLogger(), controller.NewHeartbeat()); err == nil {
		t.Fatal("--once must report the list failure")
	}
}

func TestRunResyncHeartbeatMarksEveryTickWithZeroTargets(t *testing.T) {
	heartbeat := newStaleHeartbeat(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probes := make(chan struct{}, 8)
	go runResyncHeartbeat(ctx, 10*time.Millisecond, heartbeat, func() bool {
		probes <- struct{}{}
		return true
	})
	select {
	case <-probes:
	case <-time.After(2 * time.Second):
		t.Fatal("resync tick never fired")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !heartbeat.Healthy(30 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("resync tick did not mark a heartbeat attempt")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// With targets present, audits mark attempts; the tick must not, or a wedged
// worker would never fail liveness.
func TestRunResyncHeartbeatDoesNotMaskWorkerWhenTargetsExist(t *testing.T) {
	heartbeat := newStaleHeartbeat(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probes := make(chan struct{}, 64)
	go runResyncHeartbeat(ctx, 5*time.Millisecond, heartbeat, func() bool {
		probes <- struct{}{}
		return false
	})
	for range 3 {
		select {
		case <-probes:
		case <-time.After(2 * time.Second):
			t.Fatal("resync tick never fired")
		}
	}
	if heartbeat.Healthy(time.Millisecond) {
		t.Fatal("tick marked a heartbeat attempt while targets exist")
	}
}

func TestTargetTimeoutIsPerRequestOnlyAndRunnerUsesReconcileTimeout(t *testing.T) {
	definition := integrationDefinition("edge", "example.com", "root", v1alpha1.OwnershipModeExclusive)
	definition.Timeout = 3 * time.Second
	clients := integrationKubernetes(t, nil, nil)
	manager, err := target.NewRuntimeManager(nil, newIntegrationClientFactory(), newIntegrationResourceFactory(t, clients.Dynamic), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	definition.Legacy = true
	definition.APIToken = "token"
	definition.APITokenSecretRef = nil
	if _, err := manager.Sync(context.Background(), []target.Definition{definition}); err != nil {
		t.Fatal(err)
	}
	runtimeForTarget, _ := manager.Runtime(definition.Key())
	cfg := isolationConfig()
	cfg.ReconcileTimeout = 90 * time.Second
	runner, err := buildTargetRunner(cfg, clients, runtimeForTarget, metrics.New(), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if runner.Config.ReconcileTimeout != 90*time.Second {
		t.Fatalf("runner ReconcileTimeout = %s, want the global 90s (spec.timeout is per-request only)", runner.Config.ReconcileTimeout)
	}
	if runtimeForTarget.Definition.Timeout != 3*time.Second {
		t.Fatalf("per-request timeout was lost: %s", runtimeForTarget.Definition.Timeout)
	}
}

func TestStatusWriteFailureIsLoggedWithFixedReasonAndDoesNotFail(t *testing.T) {
	clients := integrationKubernetes(t, nil, nil)
	fakeDynamic := clients.Dynamic.(interface {
		PrependReactor(string, string, k8stesting.ReactionFunc)
	})
	forbidden := func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: cannot write fortigatednsstatuses")
	}
	fakeDynamic.PrependReactor("create", "fortigatednsstatuses", forbidden)
	fakeDynamic.PrependReactor("update", "fortigatednsstatuses", forbidden)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	writeSetupFailureStatus(context.Background(), clients, 10, logger, "dns-system", "edge", 1, target.FailureCredentials)

	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "reason=status-write-failed") || !strings.Contains(logs.String(), "dns-system/edge") {
		t.Fatalf("status write failure was not logged: %q", logs.String())
	}
}

func TestTargetConditionsReflectActualState(t *testing.T) {
	healthy := controller.ReconcileAudit{DiscoveryComplete: true, PolicyComplete: true, ProviderSnapshotStable: true, ProviderRevision: "r1"}
	conflicted := healthy
	conflicted.ConflictCount = 2
	conflicted.Operations = []plan.Operation{{Type: plan.OperationConflict}}
	pending := healthy
	pending.Operations = []plan.Operation{{Type: plan.OperationCreate}}

	tests := []struct {
		name                            string
		approval                        bool
		audit                           *controller.ReconcileAudit
		err                             error
		ready, ownership, plan          metav1.ConditionStatus
		readyReason, ownerReason, pReas statuswriter.Reason
	}{
		{"healthy", false, &healthy, nil, metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionTrue,
			statuswriter.ReasonReady, statuswriter.ReasonOwnershipHealthy, statuswriter.ReasonPlanApproved},
		{"conflict count", false, &conflicted, nil, metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionTrue,
			statuswriter.ReasonReady, statuswriter.ReasonOwnershipConflict, statuswriter.ReasonPlanApproved},
		{"provider outage before audit", false, nil, errors.New("provider down"), metav1.ConditionFalse, metav1.ConditionUnknown, metav1.ConditionUnknown,
			statuswriter.ReasonProviderUnavailable, statuswriter.ReasonUnknown, statuswriter.ReasonUnknown},
		{"ownership error", false, nil, target.Fail(target.FailureOwnership), metav1.ConditionFalse, metav1.ConditionFalse, metav1.ConditionUnknown,
			statuswriter.ReasonOwnershipConflict, statuswriter.ReasonOwnershipConflict, statuswriter.ReasonUnknown},
		{"apply failure without approval", false, &pending, errors.New("apply failed"), metav1.ConditionFalse, metav1.ConditionTrue, metav1.ConditionTrue,
			statuswriter.ReasonApplyFailed, statuswriter.ReasonOwnershipHealthy, statuswriter.ReasonPlanApproved},
		{"apply failure with approval required", true, &pending, errors.New("provider request failed"), metav1.ConditionFalse, metav1.ConditionTrue, metav1.ConditionFalse,
			statuswriter.ReasonApplyFailed, statuswriter.ReasonOwnershipHealthy, statuswriter.ReasonPendingApproval},
		{"approval pending", true, &pending, plan.ErrApprovalRequired, metav1.ConditionFalse, metav1.ConditionTrue, metav1.ConditionFalse,
			statuswriter.ReasonPendingApproval, statuswriter.ReasonOwnershipHealthy, statuswriter.ReasonPendingApproval},
		{"approval required, nothing to change", true, &healthy, nil, metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionTrue,
			statuswriter.ReasonReady, statuswriter.ReasonOwnershipHealthy, statuswriter.ReasonPlanApproved},
		{"approval setup failure", true, nil, target.Fail(target.FailureApproval), metav1.ConditionFalse, metav1.ConditionUnknown, metav1.ConditionFalse,
			statuswriter.ReasonPendingApproval, statuswriter.ReasonUnknown, statuswriter.ReasonPendingApproval},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conditions := targetConditions(4, tc.approval, tc.audit, tc.err)
			check := func(kind statuswriter.ConditionType, status metav1.ConditionStatus, reason statuswriter.Reason) {
				got := conditions[kind]
				if got.Status != status || got.Reason != reason || got.ObservedGeneration != 4 {
					t.Fatalf("%s = %s/%s (gen %d), want %s/%s", kind, got.Status, got.Reason, got.ObservedGeneration, status, reason)
				}
			}
			check(statuswriter.ConditionReady, tc.ready, tc.readyReason)
			check(statuswriter.ConditionOwnershipHealthy, tc.ownership, tc.ownerReason)
			check(statuswriter.ConditionPlanApproved, tc.plan, tc.pReas)
		})
	}
}

func TestPolicyStatusIsIndependentOfSourceDiscovery(t *testing.T) {
	for _, complete := range []bool{true, false} {
		audit := &controller.ReconcileAudit{DiscoveryComplete: complete, PolicyComplete: true}
		conditions := targetConditions(4, false, audit, nil)
		if got := conditions[statuswriter.ConditionPolicyAccepted]; got.Status != metav1.ConditionTrue || got.Reason != statuswriter.ReasonPolicyAccepted {
			t.Fatalf("source completeness %v changed policy status: %#v", complete, got)
		}
	}
}

func TestChangePlanApprovalErrorsReportPendingApproval(t *testing.T) {
	clients := integrationKubernetes(t, nil, nil)
	store, err := plan.NewChangePlanStore(clients.Dynamic)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"missing", "mismatched", "expired", "stale", "terminal"} {
		t.Run(scenario, func(t *testing.T) {
			object := &v1alpha1.FortiGateDNSChangePlan{}
			object.Spec.PlanHash = "sha256:current"
			object.Status.Phase = v1alpha1.ChangePlanPendingApproval
			object.Annotations = map[string]string{v1alpha1.ApprovalHashAnnotation: object.Spec.PlanHash}
			switch scenario {
			case "missing":
				object.Annotations = nil
			case "mismatched":
				object.Annotations[v1alpha1.ApprovalHashAnnotation] = "sha256:old"
			case "expired":
				expired := metav1.NewTime(time.Unix(1, 0))
				object.Spec.ExpiresAt = &expired
			case "stale":
				object.Status.Phase = v1alpha1.ChangePlanStale
			case "terminal":
				object.Status.Phase = v1alpha1.ChangePlanSucceeded
			}
			approvalErr := store.RequireExactApproval(object)
			if !errors.Is(approvalErr, plan.ErrApprovalRequired) {
				t.Fatalf("expected classified approval rejection, got %v", approvalErr)
			}
			audit := &controller.ReconcileAudit{DiscoveryComplete: true, PolicyComplete: true, Operations: []plan.Operation{{Type: plan.OperationCreate}}}
			conditions := targetConditions(4, true, audit, fmt.Errorf("reconcile target: %w", approvalErr))
			for _, kind := range []statuswriter.ConditionType{statuswriter.ConditionReady, statuswriter.ConditionPlanApproved} {
				got := conditions[kind]
				if got.Status != metav1.ConditionFalse || got.Reason != statuswriter.ReasonPendingApproval || got.ObservedGeneration != 4 {
					t.Fatalf("%s must report pending approval: %#v", kind, got)
				}
			}
		})
	}
}
