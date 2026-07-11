package fortigate

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		_ = json.NewEncoder(&body).Encode(completeFortiResponse([]fortiRecord{{
			ID:       float64(7),
			Hostname: "web.example.com",
			Type:     "A",
			IP:       "203.0.113.10",
			TTL:      300,
			Status:   "enable",
		}}))
		return response(http.StatusOK, body.String()), nil
	})

	records, err := client.ListRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ProviderID != "7" || records[0].OwnerID != "" || records[0].Source != (dns.SourceRef{}) {
		t.Fatalf("unexpected records: %#v", records)
	}
}

func TestRecordIDAcceptsNumericQOriginKey(t *testing.T) {
	var response fortiResponse
	if err := json.Unmarshal([]byte(`{"results":[{"id":99,"q_origin_key":7}]}`), &response); err != nil {
		t.Fatalf("numeric q_origin_key must decode: %v", err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("unexpected decoded response: %#v", response)
	}
	if got := recordID(response.Results[0]); got != "7" {
		t.Fatalf("numeric q_origin_key must take precedence over id, got %q", got)
	}
}

func TestListRecordsFollowsPagination(t *testing.T) {
	client := newTestClient(t)
	var starts []string
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		start := r.URL.Query().Get("start")
		starts = append(starts, start)
		if got := r.URL.Query().Get("count"); got != "1000" {
			t.Fatalf("fixed page count = %q, want 1000", got)
		}
		if got := r.URL.Query().Get("vdom"); got != "root" {
			t.Fatalf("vdom must be retained on every page, got %q", got)
		}
		switch start {
		case "0":
			return response(http.StatusOK, fortiResponseBody(fortiResponse{
				Results:      []fortiRecord{{ID: float64(7), Hostname: "a.example.com", Type: "A", IP: "203.0.113.7"}},
				LimitReached: pointer(true), MatchedCount: pointer(2), NextIndex: pointer(0), Revision: pointer("rev-1"),
			})), nil
		case "1":
			return response(http.StatusOK, fortiResponseBody(fortiResponse{
				Results:      []fortiRecord{{ID: float64(8), Hostname: "b.example.com", Type: "A", IP: "203.0.113.8"}},
				LimitReached: pointer(false), MatchedCount: pointer(2), NextIndex: pointer(1), Revision: pointer("rev-1"),
			})), nil
		default:
			t.Fatalf("unexpected pagination start %q", start)
			return nil, nil
		}
	})

	records, err := client.ListRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(starts, ",") != "0,1" {
		t.Fatalf("pagination starts = %v, want [0 1]", starts)
	}
	if len(records) != 2 || records[0].ProviderID != "7" || records[1].ProviderID != "8" {
		t.Fatalf("unexpected paginated records: %#v", records)
	}
}

func TestListRecordsRejectsMissingPaginationMetadata(t *testing.T) {
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"results":[]}`), nil
	})

	records, err := client.ListRecords(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing pagination metadata") {
		t.Fatalf("expected missing-metadata error, records=%#v err=%v", records, err)
	}
	if records != nil {
		t.Fatalf("an incomplete snapshot must not return partial records: %#v", records)
	}
}

func TestListRecordsRejectsIncompleteTerminalCount(t *testing.T) {
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, fortiResponseBody(fortiResponse{
			Results:      []fortiRecord{{ID: float64(7), Hostname: "a.example.com", Type: "A", IP: "203.0.113.7"}},
			LimitReached: pointer(false), MatchedCount: pointer(2), NextIndex: pointer(0), Revision: pointer("rev-1"),
		})), nil
	})

	if records, err := client.ListRecords(context.Background()); err == nil || records != nil || !strings.Contains(err.Error(), "collected 1 of 2") {
		t.Fatalf("expected incomplete-count error with no records, records=%#v err=%v", records, err)
	}
}

func TestListRecordsRejectsNonAdvancingPagination(t *testing.T) {
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("start") == "0" {
			return response(http.StatusOK, fortiResponseBody(fortiResponse{
				Results:      []fortiRecord{{ID: float64(7), Hostname: "a.example.com", Type: "A", IP: "203.0.113.7"}},
				LimitReached: pointer(true), MatchedCount: pointer(3), NextIndex: pointer(0), Revision: pointer("rev-1"),
			})), nil
		}
		return response(http.StatusOK, fortiResponseBody(fortiResponse{
			Results:      []fortiRecord{{ID: float64(8), Hostname: "b.example.com", Type: "A", IP: "203.0.113.8"}},
			LimitReached: pointer(true), MatchedCount: pointer(3), NextIndex: pointer(0), Revision: pointer("rev-1"),
		})), nil
	})

	if records, err := client.ListRecords(context.Background()); err == nil || records != nil || !strings.Contains(err.Error(), "did not advance") {
		t.Fatalf("expected non-advancing pagination error, records=%#v err=%v", records, err)
	}
}

func TestListRecordsRejectsRevisionChangeAcrossPages(t *testing.T) {
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("start") == "0" {
			return response(http.StatusOK, fortiResponseBody(fortiResponse{
				Results:      []fortiRecord{{ID: float64(7), Hostname: "a.example.com", Type: "A", IP: "203.0.113.7"}},
				LimitReached: pointer(true), MatchedCount: pointer(2), NextIndex: pointer(0), Revision: pointer("rev-1"),
			})), nil
		}
		return response(http.StatusOK, fortiResponseBody(fortiResponse{
			Results:      []fortiRecord{{ID: float64(8), Hostname: "b.example.com", Type: "A", IP: "203.0.113.8"}},
			LimitReached: pointer(false), MatchedCount: pointer(2), NextIndex: pointer(1), Revision: pointer("rev-2"),
		})), nil
	})

	if records, err := client.ListRecords(context.Background()); err == nil || records != nil || !strings.Contains(err.Error(), "revision changed") {
		t.Fatalf("expected revision-change error, records=%#v err=%v", records, err)
	}
}

func TestListRecordsRejectsEmptyRevisionAcrossPages(t *testing.T) {
	cases := []struct {
		name           string
		firstRevision  string
		secondRevision string
	}{
		{name: "first page", firstRevision: "", secondRevision: "rev-1"},
		{name: "terminal page", firstRevision: "rev-1", secondRevision: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t)
			client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Query().Get("start") == "0" {
					return response(http.StatusOK, fortiResponseBody(fortiResponse{
						Results:      []fortiRecord{{ID: float64(7), Hostname: "a.example.com", Type: "A", IP: "203.0.113.7"}},
						LimitReached: pointer(true), MatchedCount: pointer(2), NextIndex: pointer(0), Revision: pointer(tc.firstRevision),
					})), nil
				}
				return response(http.StatusOK, fortiResponseBody(fortiResponse{
					Results:      []fortiRecord{{ID: float64(8), Hostname: "b.example.com", Type: "A", IP: "203.0.113.8"}},
					LimitReached: pointer(false), MatchedCount: pointer(2), NextIndex: pointer(1), Revision: pointer(tc.secondRevision),
				})), nil
			})

			if records, err := client.ListRecords(context.Background()); err == nil || records != nil || !strings.Contains(err.Error(), "non-empty revision") {
				t.Fatalf("expected empty-revision pagination error with no records, records=%#v err=%v", records, err)
			}
		})
	}
}

func TestListRecordsAllowsEmptyRevisionForSinglePage(t *testing.T) {
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, fortiResponseBody(fortiResponse{
			Results:      []fortiRecord{{ID: float64(7), Hostname: "a.example.com", Type: "A", IP: "203.0.113.7"}},
			LimitReached: pointer(false), MatchedCount: pointer(1), NextIndex: pointer(0), Revision: pointer(""),
		})), nil
	})

	records, err := client.ListRecords(context.Background())
	if err != nil || len(records) != 1 {
		t.Fatalf("a complete single page does not need cross-page revision stability, records=%#v err=%v", records, err)
	}
}

func TestRecordsRevisionIsDeterministicAndCoversProviderState(t *testing.T) {
	first := []dns.Endpoint{
		{ProviderID: "2", Zone: "example.com", DNSName: "b.example.com", RecordType: dns.RecordA, Targets: []string{"203.0.113.2"}, TTL: 300},
		{ProviderID: "1", Zone: "Example.COM.", DNSName: "A.Example.COM.", RecordType: "a", Targets: []string{"203.0.113.1"}, TTL: 60},
	}
	second := []dns.Endpoint{first[1], first[0]}
	revisionA, err := recordsRevision(first)
	if err != nil {
		t.Fatal(err)
	}
	revisionB, err := recordsRevision(second)
	if err != nil {
		t.Fatal(err)
	}
	if revisionA != revisionB || !strings.HasPrefix(revisionA, "sha256:") || len(revisionA) != len("sha256:")+64 {
		t.Fatalf("snapshot revisions differ or are malformed: %q %q", revisionA, revisionB)
	}
	changed := append([]dns.Endpoint(nil), first...)
	changed[0].TTL++
	revisionChanged, err := recordsRevision(changed)
	if err != nil {
		t.Fatal(err)
	}
	if revisionChanged == revisionA {
		t.Fatal("provider state change did not change snapshot revision")
	}
}

func TestListRecordsRejectsDuplicateProviderIDAcrossPages(t *testing.T) {
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		limited := r.URL.Query().Get("start") == "0"
		nextIndex := 1
		if limited {
			nextIndex = 0
		}
		return response(http.StatusOK, fortiResponseBody(fortiResponse{
			Results:      []fortiRecord{{ID: float64(7), Hostname: "a.example.com", Type: "A", IP: "203.0.113.7"}},
			LimitReached: pointer(limited), MatchedCount: pointer(2), NextIndex: pointer(nextIndex), Revision: pointer("rev-1"),
		})), nil
	})

	if records, err := client.ListRecords(context.Background()); err == nil || records != nil || !strings.Contains(err.Error(), "repeats provider ID") {
		t.Fatalf("expected duplicate-provider-ID error, records=%#v err=%v", records, err)
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
		return response(http.StatusOK, completeListBody(nil)), nil
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

func TestApplySkipsDependentCrossTypeCleanupAfterFailedCreate(t *testing.T) {
	recorder := &recordingRecorder{}
	client := newTestClient(t)
	client.recorder = recorder
	var requests []string
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPost {
			return response(http.StatusBadRequest, `{"status":"error"}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/7") || strings.HasSuffix(r.URL.Path, "/9") {
			t.Fatalf("dependent cleanup must not be requested after the create failure: %s %s", r.Method, r.URL.Path)
		}
		return response(http.StatusOK, `{"status":"success"}`), nil
	})

	create := endpoint("app.example.com", "CNAME", "lb.example.net")
	staleA := endpoint("app.example.com", "A", "203.0.113.7")
	staleA.ProviderID = "7"
	staleAAAA := endpoint("app.example.com", "AAAA", "2001:db8::9")
	staleAAAA.ProviderID = "9"
	deactivatedAAAA := staleAAAA
	deactivatedAAAA.Disabled = true
	independent := endpoint("other.example.com", "A", "203.0.113.8")
	independent.ProviderID = "8"

	ops := []plan.Operation{
		{Type: plan.OperationCreate, Desired: create},
		{Type: plan.OperationDelete, Current: staleA},
		{Type: plan.OperationDeactivate, Desired: deactivatedAAAA, Current: staleAAAA},
		{Type: plan.OperationDelete, Current: independent},
	}
	err := client.Apply(context.Background(), ops, false)
	if err == nil || !strings.Contains(err.Error(), "app.example.com") {
		t.Fatalf("failed create must remain visible in the aggregate error, got %v", err)
	}
	wantRequests := []string{
		"POST /api/v2/cmdb/system/dns-database/example.com/dns-entry",
		"DELETE /api/v2/cmdb/system/dns-database/example.com/dns-entry/8",
	}
	if strings.Join(requests, "\n") != strings.Join(wantRequests, "\n") {
		t.Fatalf("unexpected requests:\n%s", strings.Join(requests, "\n"))
	}
	wantOutcomes := "create:failed,delete:skipped,deactivate:skipped,delete:applied"
	if got := strings.Join(recorder.calls, ","); got != wantOutcomes {
		t.Fatalf("operation outcomes = %q, want %q", got, wantOutcomes)
	}
}

func TestApplySkipsDependentCleanupAfterFailedUpdateOrReplace(t *testing.T) {
	for _, operationType := range []string{plan.OperationUpdate, plan.OperationReplace} {
		t.Run(operationType, func(t *testing.T) {
			recorder := &recordingRecorder{}
			client := newTestClient(t)
			client.recorder = recorder
			var requests []string
			client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				switch {
				case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/7"):
					return response(http.StatusBadRequest, `{"status":"error"}`), nil
				case strings.HasSuffix(r.URL.Path, "/8"):
					t.Fatalf("dependent cleanup must not be requested after failed %s: %s %s", operationType, r.Method, r.URL.Path)
				case strings.HasSuffix(r.URL.Path, "/9"):
					return response(http.StatusOK, `{"status":"success"}`), nil
				}
				return response(http.StatusOK, `{"status":"success"}`), nil
			})

			current := endpoint("app.example.com", "A", "203.0.113.7")
			current.ProviderID = "7"
			desired := endpoint("app.example.com", "A", "203.0.113.10")
			dependent := endpoint("app.example.com", "A", "203.0.113.8")
			dependent.ProviderID = "8"
			independent := endpoint("other.example.com", "A", "203.0.113.9")
			independent.ProviderID = "9"

			ops := []plan.Operation{
				{Type: operationType, Desired: desired, Current: current},
				{Type: plan.OperationDelete, Current: dependent},
				{Type: plan.OperationDelete, Current: independent},
			}
			err := client.Apply(context.Background(), ops, false)
			if err == nil || !strings.Contains(err.Error(), "app.example.com") {
				t.Fatalf("failed %s must remain visible in the aggregate error, got %v", operationType, err)
			}
			wantRequests := []string{
				"PUT /api/v2/cmdb/system/dns-database/example.com/dns-entry/7",
				"DELETE /api/v2/cmdb/system/dns-database/example.com/dns-entry/9",
			}
			if strings.Join(requests, "\n") != strings.Join(wantRequests, "\n") {
				t.Fatalf("unexpected requests:\n%s", strings.Join(requests, "\n"))
			}
			wantOutcomes := operationType + ":failed,delete:skipped,delete:applied"
			if got := strings.Join(recorder.calls, ","); got != wantOutcomes {
				t.Fatalf("operation outcomes = %q, want %q", got, wantOutcomes)
			}
		})
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

func TestEndpointToRecordDoesNotSerializeOwnershipMetadata(t *testing.T) {
	ep := dns.Endpoint{
		DNSName:    "web.example.com",
		RecordType: "A",
		Targets:    []string{"203.0.113.10"},
		TTL:        300,
		Zone:       "example.com",
		OwnerID:    "cluster a west",
		Source:     dns.SourceRef{Kind: "Service", Namespace: "apps", Name: "web"},
	}.Normalize()

	raw, err := json.Marshal(endpointToRecord(ep))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"comment", "owner-id", "cluster a west", "Service/apps/web"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("FortiGate payload must not serialize ownership metadata %q: %s", forbidden, text)
		}
	}
}

func TestListRecordsIgnoresUndocumentedCommentProperty(t *testing.T) {
	client := newTestClient(t)
	client.httpClient.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"limit_reached":false,"matched_count":1,"next_idx":0,"revision":"rev-1","results":[{"id":7,"hostname":"web.example.com","type":"A","ip":"203.0.113.10","ttl":300,"comment":"managed-by=fortigate-external-dns;owner-id=cluster-a;source=Service/apps/web"}]}`), nil
	})

	records, err := client.ListRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].OwnerID != "" || records[0].Source != (dns.SourceRef{}) {
		t.Fatalf("undocumented comment must not establish ownership, got %#v", records)
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

func completeFortiResponse(records []fortiRecord) fortiResponse {
	limitReached := false
	matchedCount := len(records)
	nextIndex := 0
	if len(records) > 0 {
		nextIndex = len(records) - 1
	}
	revision := "rev-1"
	return fortiResponse{
		Results:      records,
		LimitReached: &limitReached,
		MatchedCount: &matchedCount,
		NextIndex:    &nextIndex,
		Revision:     &revision,
	}
}

func fortiResponseBody(value fortiResponse) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func completeListBody(records []fortiRecord) string {
	return fortiResponseBody(completeFortiResponse(records))
}

func pointer[T any](value T) *T {
	return &value
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

// newTLSTestClient builds a client with the real HTTP transport (no fake
// RoundTripper) so TLS verification behavior is exercised end to end.
func newTLSTestClient(t *testing.T, baseURL, caFile string) *Client {
	t.Helper()
	client, err := NewClient(config.FortiGateConfig{
		BaseURL:  baseURL,
		APIToken: "unit-test-credential",
		Zone:     "example.com",
		CAFile:   caFile,
		Timeout:  5 * time.Second,
		Retries:  0,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeServerCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(path, pemData, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClientVerifiesPrivateCAViaCAFile(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, completeListBody(nil))
	}))
	defer server.Close()

	client := newTLSTestClient(t, server.URL, writeServerCA(t, server))
	if _, err := client.ListRecords(context.Background()); err != nil {
		t.Fatalf("server signed by the configured CA bundle should verify: %v", err)
	}
}

func TestClientRejectsPrivateCAWithoutCAFile(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, completeListBody(nil))
	}))
	defer server.Close()

	client := newTLSTestClient(t, server.URL, "")
	if _, err := client.ListRecords(context.Background()); err == nil {
		t.Fatal("a self-signed server must fail verification when no CA file is configured")
	}
}

func TestClientRefusesLegacyTLSVersions(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, completeListBody(nil))
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11} //nolint:gosec
	server.StartTLS()
	defer server.Close()

	client := newTLSTestClient(t, server.URL, writeServerCA(t, server))
	if _, err := client.ListRecords(context.Background()); err == nil {
		t.Fatal("a TLS 1.1-only server must be refused by the TLS 1.2 minimum")
	}
}
