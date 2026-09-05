package serve

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthOKWithoutLivenessCheck(t *testing.T) {
	s := New(":0", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", rec.Code)
	}
}

func TestHealthReflectsLivenessCheck(t *testing.T) {
	s := New(":0", nil)
	healthy := true
	s.SetLivenessCheck(func() bool { return healthy })

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz with passing check = %d, want 200", rec.Code)
	}

	healthy = false
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/healthz with failing check = %d, want 503", rec.Code)
	}

	// Readiness is independent of the liveness check.
	s.SetReady(true)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz must not consult the liveness check, got %d", rec.Code)
	}

	s.SetLivenessCheck(nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz after clearing the check = %d, want 200", rec.Code)
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

func TestProbeMethodsAndBodiesAreBounded(t *testing.T) {
	s := New(":0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	s.SetReady(true)
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s = %d", method, path, rec.Code)
			}
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", strings.NewReader("x")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET with body = %d", rec.Code)
	}
}

func TestServerRejectsStalledBodyAndClosesIdleConnection(t *testing.T) {
	s := New("127.0.0.1:0", nil)
	s.srv.IdleTimeout = 50 * time.Millisecond
	listener, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(listener) }()
	defer func() {
		_ = s.Shutdown(context.Background())
		<-done
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = fmt.Fprintf(conn, "GET /healthz HTTP/1.1\r\nHost: test\r\nContent-Length: 1\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("stalled body response = %d", response.StatusCode)
	}

	idle, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	_, _ = fmt.Fprintf(idle, "GET /healthz HTTP/1.1\r\nHost: test\r\n\r\n")
	idleResponse, err := http.ReadResponse(bufio.NewReader(idle), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = idleResponse.Body.Close()
	_ = idle.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := idle.Read(one[:]); err == nil {
		t.Fatal("idle keep-alive connection remained open")
	}
}
