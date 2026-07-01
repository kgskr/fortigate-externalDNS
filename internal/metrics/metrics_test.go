package metrics

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsExposition(t *testing.T) {
	m := New()
	m.RecordReconcile(120*time.Millisecond, nil)
	m.RecordReconcile(2*time.Second, errors.New("boom"))
	m.RecordOperation("create", "planned")
	m.RecordOperation("create", "planned")
	m.RecordOperation("delete", "planned")

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	wants := []string{
		"fortigate_external_dns_reconcile_total 2",
		"fortigate_external_dns_reconcile_errors_total 1",
		"fortigate_external_dns_reconcile_duration_seconds_count 2",
		`fortigate_external_dns_operations_total{type="create",result="planned"} 2`,
		`fortigate_external_dns_operations_total{type="delete",result="planned"} 1`,
		"fortigate_external_dns_last_successful_reconcile_timestamp_seconds",
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q\n--- got ---\n%s", want, body)
		}
	}
}

func TestHistogramBucketsAreCumulative(t *testing.T) {
	m := New()
	m.RecordReconcile(20*time.Millisecond, nil)  // 0.02s
	m.RecordReconcile(300*time.Millisecond, nil) // 0.3s
	m.RecordReconcile(2*time.Second, nil)        // 2s

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	wants := []string{
		`fortigate_external_dns_reconcile_duration_seconds_bucket{le="0.05"} 1`,
		`fortigate_external_dns_reconcile_duration_seconds_bucket{le="0.5"} 2`,
		`fortigate_external_dns_reconcile_duration_seconds_bucket{le="2.5"} 3`,
		`fortigate_external_dns_reconcile_duration_seconds_bucket{le="+Inf"} 3`,
		`fortigate_external_dns_reconcile_duration_seconds_count 3`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("cumulative histogram missing %q\n--- got ---\n%s", want, body)
		}
	}
}

func TestMetricsNilSafe(t *testing.T) {
	var m *Metrics
	// Must not panic on a nil recorder (the controller injects it optionally).
	m.RecordReconcile(time.Second, nil)
	m.RecordOperation("create", "planned")
}

func TestMetricsDoNotLeakSecrets(t *testing.T) {
	m := New()
	m.RecordReconcile(time.Second, errors.New("token=super-secret-value failed"))
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(rec.Body.String(), "super-secret-value") {
		t.Fatal("metrics output leaked an error detail; only counts should be exposed")
	}
}
