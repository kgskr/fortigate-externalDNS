package fortigate

import (
	"context"
	"net/http"
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/plan"
)

func TestMutationRejectsInvalidSuccessResponse(t *testing.T) {
	for _, operationType := range []string{plan.OperationCreate, plan.OperationUpdate, plan.OperationDelete} {
		for _, body := range []string{
			`<html>upstream unavailable</html>`, `{"status":"error"`,
			`{"status":200}`, `{"http_status":"200"}`, `{"error":"0"}`,
			`[]`, `null`, `true`, `"success"`,
		} {
			t.Run(operationType+"/"+body, func(t *testing.T) {
				client := newTestClient(t)
				requests := 0
				client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
					requests++
					return response(http.StatusOK, body), nil
				})
				op := plan.Operation{Type: operationType, Desired: endpoint("web.example.com", "A", "203.0.113.10"), Current: currentEndpoint("7")}
				outcomes, err := client.ApplyWithResults(context.Background(), []plan.Operation{op}, false)
				if err == nil || len(outcomes) != 1 || outcomes[0].Result != plan.ApplyFailed {
					t.Fatalf("invalid success response must fail: err=%v outcomes=%#v", err, outcomes)
				}
				if requests != 1 {
					t.Fatalf("invalid envelope is not transient: got %d requests", requests)
				}
			})
		}
	}
}

func TestMalformedUpdateBlocksDependentCleanup(t *testing.T) {
	client := newTestClient(t)
	var requests []string
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Method)
		if r.Method == http.MethodPut {
			return response(http.StatusOK, `{"status":"error"`), nil
		}
		return response(http.StatusNoContent, ""), nil
	})
	current := endpoint("web.example.com", "A", "203.0.113.7")
	current.ProviderID = "7"
	duplicate := current
	duplicate.ProviderID = "8"
	independent := endpoint("other.example.com", "A", "203.0.113.9")
	independent.ProviderID = "9"
	operations := []plan.Operation{
		{Type: plan.OperationUpdate, Desired: endpoint("web.example.com", "A", "203.0.113.10"), Current: current},
		{Type: plan.OperationDelete, Current: duplicate},
		{Type: plan.OperationDelete, Current: independent},
	}
	outcomes, err := client.ApplyWithResults(context.Background(), operations, false)
	if err == nil || len(outcomes) != 3 || outcomes[0].Result != plan.ApplyFailed || outcomes[1].Result != plan.ApplyBlocked || outcomes[1].Reason != "prerequisite-failed" || outcomes[2].Result != plan.ApplySucceeded {
		t.Fatalf("failed prerequisite must block only dependent cleanup: err=%v outcomes=%#v", err, outcomes)
	}
	if len(requests) != 2 || requests[0] != http.MethodPut || requests[1] != http.MethodDelete {
		t.Fatalf("unexpected requests: %v", requests)
	}
}

func TestResponseValidationPreservesEmptyMutationAndBareListRead(t *testing.T) {
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return response(http.StatusOK, `["first","second"]`), nil
		}
		return response(http.StatusNoContent, ""), nil
	})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, err := client.newRequest(context.Background(), method, client.recordsPath("7"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.doJSON(req, nil); err != nil {
			t.Fatalf("empty 204 %s must remain supported: %v", method, err)
		}
	}
	req, err := client.newRequest(context.Background(), http.MethodGet, client.recordsPath(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	if err := client.doJSON(req, &values); err != nil || len(values) != 2 {
		t.Fatalf("bare-list GET must remain supported: values=%v err=%v", values, err)
	}
}
