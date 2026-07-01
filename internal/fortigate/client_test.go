package fortigate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
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

func TestApplyRealFailureNotMaskedByCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		// The first (create) op fails with a terminal 400, then we cancel so the
		// loop short-circuits before the second op.
		cancel()
		return response(http.StatusBadRequest, `{"status":"error"}`), nil
	})

	ops := []plan.Operation{
		{Type: plan.OperationCreate, Desired: endpoint("bad.example.com", "A", "203.0.113.1")},
		{Type: plan.OperationCreate, Desired: endpoint("next.example.com", "A", "203.0.113.2")},
	}
	err := client.Apply(ctx, ops, false)
	if err == nil {
		t.Fatal("expected an error for the failed operation")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("a genuine operation failure must not be masked as context.Canceled: %v", err)
	}
	if !strings.Contains(err.Error(), "bad.example.com") {
		t.Fatalf("error should name the failed operation, got %v", err)
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

func TestCreatePostNotRetriedOnServerError(t *testing.T) {
	var posts int
	client := newTestClient(t) // Retries: 1, so a retryable method would be attempted twice
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			posts++
		}
		return response(http.StatusInternalServerError, "boom"), nil
	})

	err := client.Apply(context.Background(), []plan.Operation{{
		Type:    plan.OperationCreate,
		Desired: endpoint("web.example.com", "A", "203.0.113.10"),
	}}, false)
	if err == nil {
		t.Fatal("expected an error for the failed create")
	}
	if posts != 1 {
		t.Fatalf("create POST must not be retried (duplicate dns-entry risk), got %d POSTs", posts)
	}
}

func TestKeyedPutIsRetriedOnServerError(t *testing.T) {
	var attempts int
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return response(http.StatusInternalServerError, "temporary"), nil
		}
		return response(http.StatusOK, `{"status":"success"}`), nil
	})

	op := plan.Operation{Type: plan.OperationUpdate, Desired: endpoint("old.example.com", "A", "203.0.113.11"), Current: currentEndpoint("42")}
	if err := client.Apply(context.Background(), []plan.Operation{op}, false); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("a keyed PUT is idempotent and should be retried, got %d attempts", attempts)
	}
}

func TestManagedCommentOwnerIDWithSpaceRoundTrips(t *testing.T) {
	ep := dns.Endpoint{
		DNSName:    "web.example.com",
		RecordType: "A",
		Targets:    []string{"203.0.113.10"},
		TTL:        300,
		Zone:       "example.com",
		OwnerID:    "cluster a west",
		Source:     dns.SourceRef{Kind: "Service", Namespace: "apps", Name: "web"},
	}.Normalize()

	got := endpointToRecord(ep).toEndpoint("example.com")
	if got.OwnerID != "cluster a west" {
		t.Fatalf("owner ID with spaces did not round-trip through the managed comment: %q", got.OwnerID)
	}
	if got.Source != ep.Source {
		t.Fatalf("source did not round-trip: %#v", got.Source)
	}
}

func TestParseLegacySpaceDelimitedComment(t *testing.T) {
	fields := parseCommentFields("managed-by=fortigate-external-dns owner-id=cluster-a source=Service/apps/web")
	if fields["owner-id"] != "cluster-a" {
		t.Fatalf("legacy space-delimited owner-id must still parse, got %q", fields["owner-id"])
	}
}

func TestApplySkipsOwnershipConflictWithoutRequest(t *testing.T) {
	called := false
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return response(http.StatusOK, `{}`), nil
	})

	op := plan.Operation{
		Type:    plan.OperationConflict,
		Desired: endpoint("web.example.com", "A", "203.0.113.10"),
		Current: currentEndpoint("11"),
	}
	if err := client.Apply(context.Background(), []plan.Operation{op}, false); err != nil {
		t.Fatalf("a conflict operation should be skipped without error, got %v", err)
	}
	if called {
		t.Fatal("ownership conflict must not issue any HTTP request")
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
	}, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type recordingRecorder struct{ calls []string }

func (r *recordingRecorder) RecordOperation(opType, result string) {
	r.calls = append(r.calls, opType+":"+result)
}

func TestApplyRecordsOperationOutcomes(t *testing.T) {
	rec := &recordingRecorder{}
	client := newTestClient(t)
	client.recorder = rec
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost { // the create succeeds
			return response(http.StatusOK, `{"status":"success"}`), nil
		}
		return response(http.StatusBadRequest, `{"status":"error"}`), nil // the update fails
	})

	ops := []plan.Operation{
		{Type: plan.OperationCreate, Desired: endpoint("a.example.com", "A", "203.0.113.1")},
		{Type: plan.OperationUpdate, Desired: endpoint("b.example.com", "A", "203.0.113.2"), Current: currentEndpoint("5")},
		{Type: plan.OperationConflict, Desired: endpoint("c.example.com", "A", "203.0.113.3"), Current: currentEndpoint("6")},
	}
	_ = client.Apply(context.Background(), ops, false)

	// Assert the exact, ordered recordings: one per op, no spurious or doubled
	// label (e.g. an op recorded as both applied and skipped).
	got := strings.Join(rec.calls, ",")
	want := strings.Join([]string{"create:applied", "update:failed", "conflict:conflict"}, ",")
	if got != want {
		t.Fatalf("operation outcomes mismatch:\n got:  %q\n want: %q", got, want)
	}
}

func TestApplyDryRunRecordsSkipped(t *testing.T) {
	rec := &recordingRecorder{}
	client := newTestClient(t)
	client.recorder = rec
	called := false
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return response(http.StatusOK, `{}`), nil
	})

	ops := []plan.Operation{
		{Type: plan.OperationCreate, Desired: endpoint("a.example.com", "A", "203.0.113.1")},
		{Type: plan.OperationUpdate, Desired: endpoint("b.example.com", "A", "203.0.113.2"), Current: currentEndpoint("5")},
	}
	if err := client.Apply(context.Background(), ops, true); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("dry-run must not issue any request")
	}
	got := strings.Join(rec.calls, ",")
	want := strings.Join([]string{"create:skipped", "update:skipped"}, ",")
	if got != want {
		t.Fatalf("dry-run outcomes mismatch:\n got:  %q\n want: %q", got, want)
	}
}

func TestTransientEnvelopeRetriedForKeyedMethod(t *testing.T) {
	var attempts int
	client := newTestClient(t) // Retries: 1
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			// HTTP 2xx but a transient (5xx-equivalent) envelope: must be retried.
			return response(http.StatusOK, `{"status":"error","http_status":503}`), nil
		}
		return response(http.StatusOK, `{"status":"success"}`), nil
	})

	op := plan.Operation{Type: plan.OperationUpdate, Desired: endpoint("b.example.com", "A", "203.0.113.2"), Current: currentEndpoint("5")}
	if err := client.Apply(context.Background(), []plan.Operation{op}, false); err != nil {
		t.Fatalf("a recovered transient envelope should succeed, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("a transient 2xx envelope on a keyed PUT should be retried once, got %d attempts", attempts)
	}
}

func TestTransientEnvelopeNotRetriedForCreate(t *testing.T) {
	var posts int
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			posts++
		}
		return response(http.StatusOK, `{"status":"error","http_status":503}`), nil
	})

	err := client.Apply(context.Background(), []plan.Operation{{
		Type:    plan.OperationCreate,
		Desired: endpoint("a.example.com", "A", "203.0.113.1"),
	}}, false)
	if err == nil {
		t.Fatal("a create whose envelope reports failure should error")
	}
	if posts != 1 {
		t.Fatalf("a create POST must not be retried even on a transient envelope (duplicate risk), got %d POSTs", posts)
	}
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
