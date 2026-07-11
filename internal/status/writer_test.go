package status

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const testPlanHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestWriterCreatesCompleteTargetStatusAndBoundsHistory(t *testing.T) {
	client := newFakeClient(t)
	writer, err := NewWriter(client, "dns-system", "edge", 2)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC)
	writer.now = func() time.Time { return now }

	conditions := healthyConditions(7)
	conditions[ConditionProviderReachable] = ConditionState{
		Status: metav1.ConditionTrue, Reason: ReasonProviderReachable, ObservedGeneration: 11,
		Detail: "ignored provider response body",
	}
	applyTime := now.Add(-time.Minute)
	snapshot := Snapshot{
		TargetGeneration: 7,
		ProviderRevision: "rev-1",
		Counts:           api.ReconcileCounts{Desired: 4, Current: 3, Drift: 1, Conflicts: -2},
		PlanHash:         testPlanHash,
		AuditTime:        now.Add(-2 * time.Minute),
		ApplyTime:        &applyTime,
		Conditions:       conditions,
		Audit: &Audit{
			PlanHash: testPlanHash, Phase: api.ChangePlanSucceeded,
			Timestamp: now.Add(-time.Minute), Counts: api.ReconcileCounts{Desired: 4, Current: 3, Drift: 1},
		},
	}
	if err := writer.Write(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}

	got := getStatus(t, client, "dns-system", "edge")
	if got.Spec.TargetRef.Name != "edge" || got.Spec.Retention != 2 {
		t.Fatalf("unexpected status identity: %#v", got.Spec)
	}
	if got.Status.ObservedTargetGeneration != 7 || got.Status.Counts.Conflicts != 0 {
		t.Fatalf("generation/count sanitization failed: %#v", got.Status)
	}
	if got.Status.ProviderRevision != revisionFingerprint("rev-1") || strings.Contains(got.Status.ProviderRevision, "rev-1") {
		t.Fatalf("provider revision was not fingerprinted: %q", got.Status.ProviderRevision)
	}
	if got.Status.LastPlanHash != testPlanHash || got.Status.LastAuditTime == nil || !got.Status.LastAuditTime.Time.Equal(now.Add(-2*time.Minute)) {
		t.Fatalf("plan/audit state not preserved: %#v", got.Status)
	}
	if got.Status.LastApplyTime == nil || !got.Status.LastApplyTime.Time.Equal(applyTime) {
		t.Fatalf("apply timestamp not preserved: %#v", got.Status.LastApplyTime)
	}
	if len(got.Status.Conditions) != len(conditionOrder) {
		t.Fatalf("condition count = %d, want %d", len(got.Status.Conditions), len(conditionOrder))
	}
	provider := findCondition(t, got.Status.Conditions, ConditionProviderReachable)
	if provider.ObservedGeneration != 11 || provider.Message != reasonMessages[ReasonProviderReachable] || strings.Contains(provider.Message, "ignored") {
		t.Fatalf("provider condition was not normalized: %#v", provider)
	}
	if len(got.Status.History) != 1 || got.Status.History[0].PlanHash != testPlanHash {
		t.Fatalf("unexpected audit history: %#v", got.Status.History)
	}

	transition := provider.LastTransitionTime
	for i, phase := range []api.ChangePlanPhase{api.ChangePlanFailed, api.ChangePlanInterrupted} {
		snapshot.Audit = &Audit{
			PlanHash: strings.Repeat(string(rune('b'+i)), 64), Phase: phase, Timestamp: now.Add(time.Duration(i+1) * time.Minute),
		}
		snapshot.AuditTime = time.Time{}
		writer.now = func() time.Time { return now.Add(10 * time.Minute) }
		if err := writer.Write(context.Background(), snapshot); err != nil {
			t.Fatal(err)
		}
	}
	got = getStatus(t, client, "dns-system", "edge")
	if len(got.Status.History) != 2 || got.Status.History[0].Phase != string(api.ChangePlanFailed) || got.Status.History[1].Phase != string(api.ChangePlanInterrupted) {
		t.Fatalf("history is not a bounded newest-entry ring: %#v", got.Status.History)
	}
	provider = findCondition(t, got.Status.Conditions, ConditionProviderReachable)
	if !provider.LastTransitionTime.Equal(&transition) {
		t.Fatalf("unchanged condition transition time changed: got %s want %s", provider.LastTransitionTime, transition)
	}
}

func TestWriterRetriesStatusConflict(t *testing.T) {
	client := newFakeClient(t)
	writer, err := NewWriter(client, "dns-system", "edge", DefaultRetention)
	if err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	client.PrependReactor("update", "fortigatednsstatuses", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "status" {
			return false, nil, nil
		}
		if attempts.Add(1) == 1 {
			return true, nil, apierrors.NewConflict(api.StatusGVR.GroupResource(), "edge", nil)
		}
		return false, nil, nil
	})

	if err := writer.Write(context.Background(), Snapshot{TargetGeneration: 3, Conditions: healthyConditions(3)}); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("status update attempts = %d, want 2", attempts.Load())
	}
	if got := getStatus(t, client, "dns-system", "edge"); got.Status.ObservedTargetGeneration != 3 {
		t.Fatalf("status was not written after retry: %#v", got.Status)
	}
}

func TestWriterSanitizesAllUntrustedStatusInputs(t *testing.T) {
	client := newFakeClient(t)
	writer, err := NewWriter(client, "dns-system", "edge", 1000)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"token=super-secret-value",
		"Authorization: Bearer credentials",
		"api.attacker.example.com",
		"provider-record-id-98431",
		`{"results":[{"hostname":"full-record.example.com","ip":"203.0.113.9"}]}`,
	}
	detail := strings.Join(forbidden, " ")
	conditions := healthyConditions(1)
	conditions[ConditionReady] = ConditionState{
		Status: metav1.ConditionStatus("malicious"), Reason: Reason(detail), Detail: detail,
	}
	if err := writer.Write(context.Background(), Snapshot{
		TargetGeneration: -1,
		ProviderRevision: detail,
		Counts:           api.ReconcileCounts{Desired: -1, Current: -1, Drift: -1, Conflicts: -1},
		PlanHash:         detail,
		Conditions:       conditions,
		Audit:            &Audit{PlanHash: detail, Phase: api.ChangePlanPhase(detail)},
	}); err != nil {
		t.Fatal(err)
	}
	got := getStatus(t, client, "dns-system", "edge")
	serialized, err := json.Marshal(got.Status)
	if err != nil {
		t.Fatal(err)
	}
	body := string(serialized)
	for _, value := range forbidden {
		if strings.Contains(body, value) {
			t.Fatalf("status leaked untrusted value %q: %s", value, body)
		}
	}
	ready := findCondition(t, got.Status.Conditions, ConditionReady)
	if ready.Status != metav1.ConditionUnknown || ready.Reason != string(ReasonUnknown) || ready.Message != reasonMessages[ReasonUnknown] {
		t.Fatalf("unknown condition input was not collapsed: %#v", ready)
	}
	if got.Status.ObservedTargetGeneration != 0 || got.Status.LastPlanHash != "" || len(got.Status.History) != 0 {
		t.Fatalf("invalid bounded fields were retained: %#v", got.Status)
	}
	if got.Spec.Retention != MaxRetention {
		t.Fatalf("retention = %d, want %d", got.Spec.Retention, MaxRetention)
	}
}

func TestWriterRejectsInvalidTargetIdentity(t *testing.T) {
	client := newFakeClient(t)
	for _, target := range []string{"", "UPPER", strings.Repeat("a", 254)} {
		if _, err := NewWriter(client, "dns-system", target, 1); err == nil {
			t.Fatalf("target %q unexpectedly accepted", target)
		}
	}
}

func TestWriterConcurrentStatusWrites(t *testing.T) {
	client := newFakeClient(t)
	writer, err := NewWriter(client, "dns-system", "edge", 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(context.Background(), Snapshot{Conditions: healthyConditions(0)}); err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for generation := int64(1); generation <= 25; generation++ {
				if err := writer.Write(context.Background(), Snapshot{
					TargetGeneration: generation + int64(worker),
					ProviderRevision: "rev-concurrent",
					Counts:           api.ReconcileCounts{Desired: int32(generation)},
					Conditions:       healthyConditions(generation),
				}); err != nil {
					errorsSeen <- err
					return
				}
			}
		}(worker)
	}
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatal(err)
	}
	if got := getStatus(t, client, "dns-system", "edge"); len(got.Status.Conditions) != len(conditionOrder) {
		t.Fatalf("concurrent writes produced incomplete conditions: %#v", got.Status.Conditions)
	}
}

func newFakeClient(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return dynamicfake.NewSimpleDynamicClient(scheme)
}

func getStatus(t *testing.T, client *dynamicfake.FakeDynamicClient, namespace, name string) *api.FortiGateDNSStatus {
	t.Helper()
	object, err := client.Resource(api.StatusGVR).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var typed api.FortiGateDNSStatus
	if err := api.FromUnstructured(object, &typed); err != nil {
		t.Fatal(err)
	}
	return &typed
}

func healthyConditions(generation int64) map[ConditionType]ConditionState {
	return map[ConditionType]ConditionState{
		ConditionReady:             {Status: metav1.ConditionTrue, Reason: ReasonReady, ObservedGeneration: generation},
		ConditionDiscoveryComplete: {Status: metav1.ConditionTrue, Reason: ReasonDiscoveryComplete, ObservedGeneration: generation},
		ConditionProviderReachable: {Status: metav1.ConditionTrue, Reason: ReasonProviderReachable, ObservedGeneration: generation},
		ConditionOwnershipHealthy:  {Status: metav1.ConditionTrue, Reason: ReasonOwnershipHealthy, ObservedGeneration: generation},
		ConditionPolicyAccepted:    {Status: metav1.ConditionTrue, Reason: ReasonPolicyAccepted, ObservedGeneration: generation},
		ConditionPlanApproved:      {Status: metav1.ConditionTrue, Reason: ReasonPlanApproved, ObservedGeneration: generation},
		ConditionDriftFree:         {Status: metav1.ConditionTrue, Reason: ReasonDriftFree, ObservedGeneration: generation},
	}
}

func findCondition(t *testing.T, conditions []metav1.Condition, conditionType ConditionType) metav1.Condition {
	t.Helper()
	for _, condition := range conditions {
		if condition.Type == string(conditionType) {
			return condition
		}
	}
	t.Fatalf("condition %s not found", conditionType)
	return metav1.Condition{}
}
