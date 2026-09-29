package controller

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	"github.com/kgskr/fortigate-external-dns/internal/source"
)

// ctxCheckingDynamic fails every status write issued on a dead context, like a
// real API server round trip would, so tests can prove which context the
// controller used for terminal phase writes.
type ctxCheckingDynamic struct{ dynamic.Interface }

func (c ctxCheckingDynamic) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return ctxCheckingResource{c.Interface.Resource(gvr)}
}

type ctxCheckingResource struct {
	dynamic.NamespaceableResourceInterface
}

func (r ctxCheckingResource) Namespace(ns string) dynamic.ResourceInterface {
	return ctxCheckingNamespaced{r.NamespaceableResourceInterface.Namespace(ns)}
}

type ctxCheckingNamespaced struct{ dynamic.ResourceInterface }

func (n ctxCheckingNamespaced) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return n.ResourceInterface.UpdateStatus(ctx, obj, opts)
}

// applyHookDNSClient runs a hook inside Apply, modeling a provider call that is
// interrupted mid-flight.
type applyHookDNSClient struct {
	recordingDNSClient
	hook func(ctx context.Context) error
}

func (c *applyHookDNSClient) Apply(ctx context.Context, operations []plan.Operation, dryRun bool) error {
	c.operations = append([]plan.Operation(nil), operations...)
	return c.hook(ctx)
}

func newPhaseStore(t *testing.T) (*plan.ChangePlanStore, dynamic.Interface) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := ctxCheckingDynamic{dynamicfake.NewSimpleDynamicClient(scheme)}
	store, err := plan.NewChangePlanStore(client)
	if err != nil {
		t.Fatal(err)
	}
	return store, client
}

func listPlans(t *testing.T, client dynamic.Interface) []v1alpha1.FortiGateDNSChangePlan {
	t.Helper()
	list, err := client.Resource(v1alpha1.ChangePlanGVR).Namespace("system").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []v1alpha1.FortiGateDNSChangePlan
	for i := range list.Items {
		var typed v1alpha1.FortiGateDNSChangePlan
		if err := v1alpha1.FromUnstructured(&list.Items[i], &typed); err != nil {
			t.Fatal(err)
		}
		out = append(out, typed)
	}
	return out
}

func storeRunner(t *testing.T, client DNSClient, store *plan.ChangePlanStore, approval bool) Runner {
	runner := planTestRunner(restrictedOwnershipService("web", "203.0.113.10"), client)
	runner.TargetName = "edge"
	runner.TargetIdentity = plan.TargetIdentity{Namespace: "system", Name: "edge", UID: "target-uid", Generation: 3, VDOM: "root", Zone: "example.com"}
	runner.ChangePlanStore = store
	runner.ChangePlanNamespace = "system"
	runner.ApprovalRequired = approval
	return runner
}

func TestApprovalRequiredWithZeroOperationsPersistsNothing(t *testing.T) {
	store, client := newPhaseStore(t)
	dnsClient := &recordingDNSClient{revision: "rev-1", records: []dns.Endpoint{restrictedCurrentEndpoint("web.example.com", "A", "203.0.113.10", 300, false)}}
	runner := storeRunner(t, dnsClient, store, true)
	for i := 0; i < 3; i++ {
		if err := runner.RunOnce(context.Background()); err != nil {
			t.Fatalf("quiet cycle %d must succeed, got %v", i, err)
		}
	}
	if plans := listPlans(t, client); len(plans) != 0 {
		t.Fatalf("no ChangePlan may be created for a quiet cycle, got %d", len(plans))
	}
	if len(dnsClient.operations) != 0 {
		t.Fatalf("unexpected provider operations %#v", dnsClient.operations)
	}
}

func TestApprovalRequiredWithConflictOnlyPersistsNothing(t *testing.T) {
	store, client := newPhaseStore(t)
	other := restrictedCurrentEndpoint("web.example.com", "A", "198.51.100.5", 300, false)
	other.OwnerID = "someone-else"
	dnsClient := &recordingDNSClient{revision: "rev-1", records: []dns.Endpoint{other}}
	runner := storeRunner(t, dnsClient, store, true)
	runner.Config.FortiGate.ExclusiveZoneOwnership = false
	audit, err := runner.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if audit.ConflictCount != 1 || len(audit.Operations) != 1 {
		t.Fatalf("expected a conflict-only plan, got %#v", audit.Operations)
	}
	if err := runner.ApplyPrepared(context.Background(), audit); err != nil {
		t.Fatalf("conflict-only cycle must not report an approval error, got %v", err)
	}
	if plans := listPlans(t, client); len(plans) != 0 {
		t.Fatalf("no ChangePlan may be created for a conflict-only cycle, got %d", len(plans))
	}
	// Conflicts still reach the provider so it can count them and shared
	// ownership can converge an interrupted rebind; no mutation may be passed.
	if len(dnsClient.operations) != 1 || dnsClient.operations[0].Type != plan.OperationConflict {
		t.Fatalf("only the conflict may reach the provider, got %#v", dnsClient.operations)
	}
}

func TestRealChangeStillRequiresExactApproval(t *testing.T) {
	store, client := newPhaseStore(t)
	dnsClient := &recordingDNSClient{revision: "rev-1"}
	runner := storeRunner(t, dnsClient, store, true)
	err := runner.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "approval is missing") {
		t.Fatalf("real change without approval must fail closed, got %v", err)
	}
	if plans := listPlans(t, client); len(plans) != 1 {
		t.Fatalf("expected one pending plan, got %d", len(plans))
	}
	if len(dnsClient.operations) != 0 {
		t.Fatalf("provider apply ran before approval: %#v", dnsClient.operations)
	}
}

func TestNonActionableCycleInvalidatesPreviousApproval(t *testing.T) {
	for _, phase := range []v1alpha1.ChangePlanPhase{v1alpha1.ChangePlanPendingApproval, v1alpha1.ChangePlanApproved} {
		for _, transition := range []string{"source-deleted", "already-converged", "conflict-only"} {
			t.Run(string(phase)+"/"+transition, func(t *testing.T) {
				ctx := context.Background()
				store, client := newPhaseStore(t)
				dnsClient := &recordingDNSClient{revision: "rev-1"}
				runner := storeRunner(t, dnsClient, store, true)
				if err := runner.RunOnce(ctx); err == nil {
					t.Fatal("initial change must require approval")
				}
				plans := listPlans(t, client)
				if len(plans) != 1 {
					t.Fatalf("expected one pending plan, got %d", len(plans))
				}
				previousName := plans[0].Name
				if _, err := store.UpdatePhase(ctx, "system", previousName, phase, nil); err != nil {
					t.Fatal(err)
				}
				switch transition {
				case "source-deleted":
					if err := runner.Kube.Core.CoreV1().Services("apps").Delete(ctx, "web", metav1.DeleteOptions{}); err != nil {
						t.Fatal(err)
					}
				case "already-converged":
					dnsClient.records = []dns.Endpoint{restrictedCurrentEndpoint("web.example.com", "A", "203.0.113.10", 300, false)}
				case "conflict-only":
					runner.Config.FortiGate.ExclusiveZoneOwnership = false
					dnsClient.records = []dns.Endpoint{restrictedCurrentEndpoint("web.example.com", "A", "198.51.100.5", 300, false)}
					dnsClient.records[0].OwnerID = "someone-else"
				}
				for cycle := 0; cycle < 2; cycle++ {
					if err := runner.RunOnce(ctx); err != nil {
						t.Fatalf("non-actionable cycle must succeed: %v", err)
					}
				}
				plans = listPlans(t, client)
				if len(plans) != 1 || plans[0].Name != previousName || plans[0].Status.Phase != v1alpha1.ChangePlanStale {
					t.Fatalf("must stale the old plan without creating another: %#v", plans)
				}
				if hasActionableOperation(dnsClient.operations) {
					t.Fatalf("non-actionable cycle sent mutations: %#v", dnsClient.operations)
				}
			})
		}
	}
}

// An earlier release persisted no-op plans, so a pending plan can carry the
// same hash as the current quiet cycle; it must be staled too, and the target
// must stop reporting a current plan phase.
func TestQuietCycleStalesSameHashPlanAndClearsPhaseMetric(t *testing.T) {
	ctx := context.Background()
	store, client := newPhaseStore(t)
	dnsClient := &recordingDNSClient{revision: "rev-1", records: []dns.Endpoint{restrictedCurrentEndpoint("web.example.com", "A", "203.0.113.10", 300, false)}}
	runner := storeRunner(t, dnsClient, store, true)
	runner.Metrics = metrics.New()
	audit, err := runner.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hasActionableOperation(audit.Operations) {
		t.Fatalf("expected a quiet cycle, got %#v", audit.Operations)
	}
	legacy, err := store.PersistCurrent(ctx, "system", audit.Document, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	runner.Metrics.SetCurrentPlanPhase(runner.metricTargetName(), v1alpha1.ChangePlanPendingApproval)
	if err := runner.ApplyPrepared(ctx, audit); err != nil {
		t.Fatalf("quiet cycle must succeed: %v", err)
	}
	plans := listPlans(t, client)
	if len(plans) != 1 || plans[0].Name != legacy.Name || plans[0].Status.Phase != v1alpha1.ChangePlanStale {
		t.Fatalf("same-hash no-op plan must be staled: %#v", plans)
	}
	for _, line := range strings.Split(scrapeRunnerMetrics(runner.Metrics), "\n") {
		if strings.Contains(line, "_plans{") && strings.HasSuffix(strings.TrimSpace(line), " 1") {
			t.Fatalf("quiet cycle still reports a current plan phase: %s", line)
		}
	}
}

func TestReconcileTimeoutMidApplyWritesInterruptedPhase(t *testing.T) {
	store, client := newPhaseStore(t)
	dnsClient := &applyHookDNSClient{recordingDNSClient: recordingDNSClient{revision: "rev-1"}}
	dnsClient.hook = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	runner := storeRunner(t, dnsClient, store, false)
	runner.Config.ReconcileTimeout = 300 * time.Millisecond
	audit, err := runner.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = runner.ApplyPrepared(context.Background(), audit)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	plans := listPlans(t, client)
	if len(plans) != 1 || plans[0].Status.Phase != v1alpha1.ChangePlanInterrupted {
		t.Fatalf("plan must reach Interrupted after the reconcile timeout, got %#v", plans)
	}
}

func TestParentCancellationMidApplyDoesNotWriteTerminalPhase(t *testing.T) {
	store, client := newPhaseStore(t)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	dnsClient := &applyHookDNSClient{recordingDNSClient: recordingDNSClient{revision: "rev-1"}}
	dnsClient.hook = func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	runner := storeRunner(t, dnsClient, store, false)
	runner.Config.ReconcileTimeout = time.Minute
	audit, err := runner.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPrepared(parent, audit); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	plans := listPlans(t, client)
	if len(plans) != 1 || plans[0].Status.Phase != v1alpha1.ChangePlanApplying {
		t.Fatalf("a canceled parent must leave the plan Applying, got %#v", plans)
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRevalidationDoesNotDoubleCountPlanLogsOrCleanupRefusals(t *testing.T) {
	store, _ := newPhaseStore(t)
	stale1 := restrictedCurrentEndpoint("old1.example.com", "A", "203.0.113.21", 300, false)
	stale2 := restrictedCurrentEndpoint("old2.example.com", "A", "203.0.113.22", 300, false)
	dnsClient := &recordingDNSClient{revision: "rev-1", records: []dns.Endpoint{stale1, stale2}}
	runner := storeRunner(t, dnsClient, store, false)
	runner.Config.CleanupPolicy = "delete"
	runner.Config.MaxCleanupPerCycle = 1
	// Unrestricted exclusive-zone discovery adopts the stale rows so cleanup is planned.
	runner.Config.Namespaces = nil
	runner.Config.Sources = []string{source.SourceService, source.SourceIngress, source.SourceGateway}
	logs := &lockedBuffer{}
	runner.Logger = slog.New(slog.NewTextHandler(logs, nil))
	runner.Metrics = metrics.New()
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(logs.String(), "reconcile plan built"); got != 1 {
		t.Fatalf("plan built log emitted %d times, want 1:\n%s", got, logs.String())
	}
	if got := strings.Count(logs.String(), "mass-cleanup guard refused"); got != 1 {
		t.Fatalf("cleanup refusal log emitted %d times, want 1", got)
	}
	body := scrapeRunnerMetrics(runner.Metrics)
	found := false
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "cleanup_refused") && strings.Contains(line, "cap-exceeded") && !strings.HasPrefix(line, "#") {
			found = true
			if !strings.HasSuffix(strings.TrimSpace(line), " 1") {
				t.Fatalf("cleanup refusal counted more than once: %s", line)
			}
		}
	}
	if !found {
		t.Fatalf("cleanup refusal metric missing:\n%s", body)
	}
}
