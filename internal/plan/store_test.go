package plan

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
)

func TestChangePlanStoreRequiresExactCurrentHashApproval(t *testing.T) {
	store := newTestChangePlanStore(t)
	document := testDocument([]SanitizedOperation{SanitizeOperation(Operation{Type: OperationCreate, Desired: testEndpoint("api.example.com", "203.0.113.10"), Reason: "record missing"})})
	object, err := store.PersistCurrent(context.Background(), "system", document, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if object.Status.Phase != v1alpha1.ChangePlanPendingApproval {
		t.Fatalf("new plan phase = %q", object.Status.Phase)
	}
	if err := store.RequireExactApproval(object); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing approval error = %v", err)
	}
	object.Annotations = map[string]string{v1alpha1.ApprovalHashAnnotation: strings.ToUpper(object.Spec.PlanHash)}
	if err := store.RequireExactApproval(object); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched approval error = %v", err)
	}
	object.Annotations[v1alpha1.ApprovalHashAnnotation] = object.Spec.PlanHash
	if err := store.RequireExactApproval(object); err != nil {
		t.Fatalf("exact approval rejected: %v", err)
	}
}

func TestChangePlanStoreSupersedesAndPrunesOnlyTerminalPlans(t *testing.T) {
	store := newTestChangePlanStore(t)
	firstDocument := testDocument([]SanitizedOperation{SanitizeOperation(Operation{Type: OperationCreate, Desired: testEndpoint("a.example.com", "203.0.113.10"), Reason: "record missing"})})
	first, err := store.PersistCurrent(context.Background(), "system", firstDocument, nil, 10)
	if err != nil {
		t.Fatal(err)
	}

	secondDocument := firstDocument
	secondDocument.Preconditions.Provider.Revision = "rev-2"
	second, err := store.PersistCurrent(context.Background(), "system", secondDocument, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.client.Resource(v1alpha1.ChangePlanGVR).Namespace("system").Get(context.Background(), first.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var oldTyped v1alpha1.FortiGateDNSChangePlan
	if err := v1alpha1.FromUnstructured(old, &oldTyped); err != nil {
		t.Fatal(err)
	}
	if oldTyped.Status.Phase != v1alpha1.ChangePlanStale {
		t.Fatalf("superseded plan phase = %q", oldTyped.Status.Phase)
	}
	recurrent, err := store.PersistCurrent(context.Background(), "system", firstDocument, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if recurrent.Name == first.Name {
		t.Fatal("a recurring plan must not reuse stale approval-bearing plan identity")
	}
	secondObject, err := store.client.Resource(v1alpha1.ChangePlanGVR).Namespace("system").Get(context.Background(), second.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var secondTyped v1alpha1.FortiGateDNSChangePlan
	if err := v1alpha1.FromUnstructured(secondObject, &secondTyped); err != nil {
		t.Fatal(err)
	}
	if secondTyped.Status.Phase != v1alpha1.ChangePlanStale {
		t.Fatalf("newly superseded plan phase = %q", secondTyped.Status.Phase)
	}
	if err := store.Prune(context.Background(), "system", "edge", 0); err != nil {
		t.Fatal(err)
	}
	list, err := store.client.Resource(v1alpha1.ChangePlanGVR).Namespace("system").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].GetName() != recurrent.Name {
		t.Fatalf("retained plans = %#v, want only current pending plan", list.Items)
	}
}

func TestChangePlanStoreRecordsBoundedOutcomesAndExpiry(t *testing.T) {
	store := newTestChangePlanStore(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	store.now = func() time.Time { return now }
	expires := metav1.NewTime(now.Add(time.Minute))
	document := testDocument([]SanitizedOperation{SanitizeOperation(Operation{Type: OperationCreate, Desired: testEndpoint("api.example.com", "203.0.113.10"), Reason: "record missing"})})
	object, err := store.PersistCurrent(context.Background(), "system", document, &expires, 10)
	if err != nil {
		t.Fatal(err)
	}
	object.Annotations = map[string]string{v1alpha1.ApprovalHashAnnotation: object.Spec.PlanHash}
	store.now = func() time.Time { return expires.Add(time.Nanosecond) }
	if err := store.RequireExactApproval(object); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired plan error = %v", err)
	}
	longReason := strings.Repeat("x", 200)
	updated, err := store.UpdatePhase(context.Background(), "system", object.Name, v1alpha1.ChangePlanInterrupted, []OperationOutcome{{OperationID: document.Operations[0].ID, Result: ApplyBlocked, Reason: longReason}})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Status.Outcomes) != 1 || len(updated.Status.Outcomes[0].Reason) != 64 || updated.Status.CompletedAt == nil {
		t.Fatalf("bounded interrupted status = %#v", updated.Status)
	}
}

func newTestChangePlanStore(t *testing.T) *ChangePlanStore {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store, err := NewChangePlanStore(dynamicfake.NewSimpleDynamicClient(scheme))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
