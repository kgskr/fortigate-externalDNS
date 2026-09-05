package fortigate

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/plan"
)

func TestApplyWithResultsPreservesMixedOutcomesAndCleanupDependency(t *testing.T) {
	client := newTestClient(t)
	var requests []string
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Method)
		if r.Method == http.MethodPost {
			return response(http.StatusBadRequest, `{"status":"error","message":"sensitive-provider-detail"}`), nil
		}
		return response(http.StatusOK, `{}`), nil
	})
	stale := endpoint("app.example.com", "A", "203.0.113.7")
	stale.ProviderID = "7"
	independent := endpoint("other.example.com", "A", "203.0.113.8")
	independent.ProviderID = "8"
	ops := []plan.Operation{
		{Type: plan.OperationCreate, Desired: endpoint("app.example.com", "CNAME", "lb.example.net")},
		{Type: plan.OperationDelete, Current: stale},
		{Type: plan.OperationDelete, Current: independent},
		{Type: plan.OperationConflict, Desired: endpoint("conflict.example.com", "A", "203.0.113.9")},
	}
	outcomes, err := client.ApplyWithResults(context.Background(), ops, false)
	if err == nil {
		t.Fatal("failed request must remain visible")
	}
	want := []plan.OperationOutcome{
		{OperationID: plan.SanitizeOperation(ops[0]).ID, Result: plan.ApplyFailed, Reason: "provider-request-failed"},
		{OperationID: plan.SanitizeOperation(ops[1]).ID, Result: plan.ApplyBlocked, Reason: "prerequisite-failed"},
		{OperationID: plan.SanitizeOperation(ops[2]).ID, Result: plan.ApplySucceeded},
		{OperationID: plan.SanitizeOperation(ops[3]).ID, Result: plan.ApplyBlocked, Reason: "planning-conflict"},
	}
	if !reflect.DeepEqual(outcomes, want) {
		t.Fatalf("outcomes = %#v, want %#v", outcomes, want)
	}
	if !reflect.DeepEqual(requests, []string{http.MethodPost, http.MethodDelete}) {
		t.Fatalf("dependent cleanup or conflict made a request: %v", requests)
	}
}

func TestApplyWithResultsCancellationPreservesCompletedWork(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		cancel()
		return response(http.StatusOK, `{}`), nil
	})
	ops := []plan.Operation{
		{Type: plan.OperationCreate, Desired: endpoint("one.example.com", "A", "203.0.113.1")},
		{Type: plan.OperationCreate, Desired: endpoint("two.example.com", "A", "203.0.113.2")},
		{Type: plan.OperationCreate, Desired: endpoint("three.example.com", "A", "203.0.113.3")},
	}
	outcomes, err := client.ApplyWithResults(ctx, ops, false)
	if !errors.Is(err, context.Canceled) || requests != 1 || len(outcomes) != len(ops) {
		t.Fatalf("requests=%d outcomes=%#v err=%v", requests, outcomes, err)
	}
	if outcomes[0].Result != plan.ApplySucceeded {
		t.Fatalf("completed request must remain successful: %#v", outcomes[0])
	}
	for i := 1; i < len(ops); i++ {
		if outcomes[i].Result != plan.ApplyBlocked || outcomes[i].Reason != "context-canceled" {
			t.Fatalf("unattempted request %d: %#v", i, outcomes[i])
		}
	}
}

func TestApplyWithResultsDryRunNeverReportsApplied(t *testing.T) {
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("dry-run must not make a provider request")
		return nil, nil
	})
	ops := []plan.Operation{{Type: plan.OperationCreate, Desired: endpoint("app.example.com", "A", "203.0.113.7")}}
	outcomes, err := client.ApplyWithResults(context.Background(), ops, true)
	if err != nil || len(outcomes) != 1 || outcomes[0].Result != plan.ApplyBlocked || outcomes[0].Reason != "dry-run" {
		t.Fatalf("dry-run outcomes=%#v err=%v", outcomes, err)
	}
}
