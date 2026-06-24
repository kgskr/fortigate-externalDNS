package serve

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthAlwaysOK(t *testing.T) {
	s := New(":0", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", rec.Code)
	}
}

func TestReadinessReflectsState(t *testing.T) {
	s := New(":0", nil)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz before ready = %d, want 503", rec.Code)
	}

	s.SetReady(true)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz after SetReady = %d, want 200", rec.Code)
	}
}

func TestMetricsRouteServed(t *testing.T) {
	served := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	})
	s := New(":0", handler)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !served || rec.Code != http.StatusOK {
		t.Fatalf("/metrics not served: served=%v code=%d", served, rec.Code)
	}
}
