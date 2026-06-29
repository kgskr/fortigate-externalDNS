package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/gilsu/fortigate-external-dns/internal/config"
	"github.com/gilsu/fortigate-external-dns/internal/metrics"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestStartProbeServerDisabledWhenAddrEmpty(t *testing.T) {
	server, err := startProbeServer(config.Config{MetricsAddr: ""}, metrics.New(), discardLogger())
	if err != nil || server != nil {
		t.Fatalf("an empty metrics address should disable the server, got server=%v err=%v", server, err)
	}
}

func TestStartProbeServerFatalOnBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	server, err := startProbeServer(config.Config{MetricsAddr: ln.Addr().String()}, metrics.New(), discardLogger())
	if err == nil {
		if server != nil {
			_ = server.Shutdown(context.Background())
		}
		t.Fatal("binding an already-used address must be a fatal startup error, not a silent background failure")
	}
}

func TestUseLeaderElection(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{name: "default long-running", cfg: config.Config{LeaderElection: true}, want: true},
		{name: "once bypasses election", cfg: config.Config{LeaderElection: true, Once: true}, want: false},
		{name: "disabled for local testing", cfg: config.Config{LeaderElection: false}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := useLeaderElection(tc.cfg); got != tc.want {
				t.Fatalf("useLeaderElection() = %v, want %v", got, tc.want)
			}
		})
	}
}
