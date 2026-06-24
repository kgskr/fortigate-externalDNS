package fortigate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gilsu/fortigate-external-dns/internal/config"
	"github.com/gilsu/fortigate-external-dns/internal/dns"
	"github.com/gilsu/fortigate-external-dns/internal/plan"
)

func TestListRecordsMapsFortiGateResponse(t *testing.T) {
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer unit-test-credential" {
			t.Fatalf("missing bearer token")
		}
		if r.URL.Path != "/api/v2/cmdb/system/dns-database/example.com/dns-entry" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body bytes.Buffer
		_ = json.NewEncoder(&body).Encode(fortiResponse{Results: []fortiRecord{{
			ID:       float64(7),
			Hostname: "web.example.com",
			Type:     "A",
			IP:       "203.0.113.10",
			TTL:      300,
			Comment:  "managed-by=fortigate-external-dns owner-id=cluster-a source=Service/apps/web",
			Status:   "enable",
		}}})
		return response(http.StatusOK, body.String()), nil
	})

	records, err := client.ListRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ProviderID != "7" || records[0].OwnerID != "cluster-a" {
		t.Fatalf("unexpected records: %#v", records)
	}
}

func TestApplyDryRunDoesNotMutate(t *testing.T) {
	called := false
	var logs bytes.Buffer
	client := newTestClientWithLogger(t, slog.New(slog.NewTextHandler(&logs, nil)))
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return response(http.StatusOK, `{}`), nil
	})

	err := client.Apply(context.Background(), []plan.Operation{{
		Type:    plan.OperationCreate,
		Desired: endpoint("web.example.com", "A", "203.0.113.10"),
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("dry-run sent a mutating request")
	}
	if strings.Contains(logs.String(), "unit-test-credential") {
		t.Fatal("logs leaked API token")
	}
}

func TestApplyCreateUpdateDeleteRequests(t *testing.T) {
	var seen []string
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		return response(http.StatusOK, `{}`), nil
	})

	ops := []plan.Operation{
		{Type: plan.OperationCreate, Desired: endpoint("new.example.com", "A", "203.0.113.10")},
		{Type: plan.OperationUpdate, Desired: endpoint("old.example.com", "A", "203.0.113.11"), Current: currentEndpoint("42")},
		{Type: plan.OperationDelete, Current: currentEndpoint("43")},
	}
	if err := client.Apply(context.Background(), ops, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /api/v2/cmdb/system/dns-database/example.com/dns-entry",
		"PUT /api/v2/cmdb/system/dns-database/example.com/dns-entry/42",
		"DELETE /api/v2/cmdb/system/dns-database/example.com/dns-entry/43",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected requests:\n%s", strings.Join(seen, "\n"))
	}
}

func TestRetryOnServerError(t *testing.T) {
	attempts := 0
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return response(http.StatusInternalServerError, "temporary"), nil
		}
		return response(http.StatusOK, `{"results":[]}`), nil
	})

	if _, err := client.ListRecords(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("expected retry, got %d attempts", attempts)
	}
}

func TestApplyContinuesAfterFailedOperation(t *testing.T) {
	var posts int
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		posts++
		if posts == 1 {
			return response(http.StatusBadRequest, `{"status":"error"}`), nil
		}
		return response(http.StatusOK, `{}`), nil
	})

	ops := []plan.Operation{
		{Type: plan.OperationCreate, Desired: endpoint("bad.example.com", "A", "203.0.113.1")},
		{Type: plan.OperationCreate, Desired: endpoint("good.example.com", "A", "203.0.113.2")},
	}
	err := client.Apply(context.Background(), ops, false)
	if err == nil {
		t.Fatal("expected an aggregated error for the failed operation")
	}
	if posts != 2 {
		t.Fatalf("expected the second independent operation to still run, got %d POSTs", posts)
	}
	if !strings.Contains(err.Error(), "bad.example.com") {
		t.Fatalf("aggregated error should name the failed operation: %v", err)
	}
}

func TestListRecordsRejectsErrorEnvelopeOn200(t *testing.T) {
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"status":"error","http_status":424,"results":[]}`), nil
	})

	records, err := client.ListRecords(context.Background())
	if err == nil {
		t.Fatal("expected an error for a 200 response carrying an error envelope")
	}
	if records != nil {
		t.Fatalf("error envelope must not be treated as an empty result set: %#v", records)
	}
}

func TestApplyReplaceUsesProviderIDPut(t *testing.T) {
	var seen []string
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		return response(http.StatusOK, `{"status":"success"}`), nil
	})

	op := plan.Operation{
		Type:    plan.OperationReplace,
		Desired: endpoint("old.example.com", "A", "203.0.113.99"),
		Current: currentEndpoint("9"),
	}
	if err := client.Apply(context.Background(), []plan.Operation{op}, false); err != nil {
		t.Fatal(err)
	}
	want := "PUT /api/v2/cmdb/system/dns-database/example.com/dns-entry/9"
	if len(seen) != 1 || seen[0] != want {
		t.Fatalf("replace should PUT to the provider ID, got %v", seen)
	}
}

func TestApplyFailsWithoutProviderID(t *testing.T) {
	called := false
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return response(http.StatusOK, `{}`), nil
	})

	stale := endpoint("old.example.com", "A", "203.0.113.10") // no ProviderID
	err := client.Apply(context.Background(), []plan.Operation{{Type: plan.OperationDelete, Current: stale}}, false)
	if err == nil {
		t.Fatal("expected a missing-provider-ID error")
	}
	if called {
		t.Fatal("must not issue a hostname-based request when the provider ID is missing")
	}
}

func TestRetryRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(http.StatusInternalServerError, "boom"), nil
	})

	_, err := client.ListRecords(ctx)
	if err == nil {
		t.Fatal("expected an error when the context is canceled during retry backoff")
	}
}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	return newTestClientWithLogger(t, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
}

func newTestClientWithLogger(t *testing.T, logger *slog.Logger) *Client {
	t.Helper()
	client, err := NewClient(config.FortiGateConfig{
		BaseURL:  "https://fortigate.example.com",
		APIToken: "unit-test-credential",
		VDOM:     "root",
		Zone:     "example.com",
		Timeout:  time.Second,
		Retries:  1,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func endpoint(name, recordType, target string) dns.Endpoint {
	return dns.Endpoint{
		DNSName:    name,
		RecordType: recordType,
		Targets:    []string{target},
		TTL:        300,
		Zone:       "example.com",
		OwnerID:    "cluster-a",
		Source:     dns.SourceRef{Kind: "Service", Namespace: "apps", Name: "web"},
	}.Normalize()
}

func currentEndpoint(id string) dns.Endpoint {
	endpoint := endpoint("old.example.com", "A", "203.0.113.10")
	endpoint.ProviderID = id
	return endpoint
}
