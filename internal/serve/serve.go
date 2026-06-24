// Package serve exposes the controller's health, readiness, and metrics
// endpoints over a small HTTP server suitable for Kubernetes probes.
package serve

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

// Server serves /healthz, /readyz, and (optionally) /metrics.
type Server struct {
	ready atomic.Bool
	srv   *http.Server
}

// New builds a server bound to addr. If metricsHandler is non-nil it is served
// at /metrics. The server starts not-ready; call SetReady once the controller
// is able to serve its configured role.
func New(addr string, metricsHandler http.Handler) *Server {
	s := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeText(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.ready.Load() {
			writeText(w, http.StatusOK, "ok")
			return
		}
		writeText(w, http.StatusServiceUnavailable, "not ready")
	})
	if metricsHandler != nil {
		mux.Handle("/metrics", metricsHandler)
	}
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// SetReady toggles the readiness state reported by /readyz.
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// ListenAndServe blocks serving until the server is shut down.
func (s *Server) ListenAndServe() error { return s.srv.ListenAndServe() }

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

// Handler exposes the routing handler for testing.
func (s *Server) Handler() http.Handler { return s.srv.Handler }

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
