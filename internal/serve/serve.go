// Package serve exposes the controller's health, readiness, and metrics
// endpoints over a small HTTP server suitable for Kubernetes probes.
package serve

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

const (
	readTimeout  = 5 * time.Second
	writeTimeout = 5 * time.Second
	idleTimeout  = 30 * time.Second
)

// Server serves /healthz, /readyz, and (optionally) /metrics.
type Server struct {
	ready atomic.Bool
	// live is an optional liveness check consulted by /healthz. Stored as a
	// pointer so probes racing SetLivenessCheck read a consistent value.
	live atomic.Pointer[func() bool]
	srv  *http.Server
}

// New builds a server bound to addr. If metricsHandler is non-nil it is served
// at /metrics. The server starts not-ready; call SetReady once the controller
// is able to serve its configured role.
func New(addr string, metricsHandler http.Handler) *Server {
	s := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if check := s.live.Load(); check != nil && !(*check)() {
			writeText(w, http.StatusServiceUnavailable, "reconcile heartbeat stale")
			return
		}
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
		Handler:           validateRequest(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    16 << 10,
	}
	return s
}

func validateRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeText(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
			r.Close = true
			w.Header().Set("Connection", "close")
			writeText(w, http.StatusBadRequest, "request body not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SetReady toggles the readiness state reported by /readyz.
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// SetLivenessCheck installs a check consulted by /healthz: when it returns
// false the endpoint reports 503. A nil check restores unconditional success.
// The check must be cheap and safe for concurrent use.
func (s *Server) SetLivenessCheck(check func() bool) {
	if check == nil {
		s.live.Store(nil)
		return
	}
	s.live.Store(&check)
}

// Listen binds the server's address. It returns the bind error synchronously so
// the caller can treat a failed bind (for example an address already in use) as
// fatal, instead of discovering it asynchronously after readiness is reported.
func (s *Server) Listen() (net.Listener, error) {
	return net.Listen("tcp", s.srv.Addr)
}

// Serve blocks serving on a listener from Listen until the server is shut down.
func (s *Server) Serve(listener net.Listener) error { return s.srv.Serve(listener) }

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

// Handler exposes the routing handler for testing.
func (s *Server) Handler() http.Handler { return s.srv.Handler }

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
