package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunWithLeaderElectionPropagatesRunResult(t *testing.T) {
	t.Setenv("POD_NAME", "test-identity")
	cfg := config.Config{LeaderElection: true, LeaderElectionID: "test-lease", LeaderElectionNamespace: "default"}
	sentinel := errors.New("run finished")
	ran := false

	err := runWithLeaderElection(context.Background(), cfg, fake.NewSimpleClientset(), discardLogger(), func(context.Context) error {
		ran = true
		return sentinel
	})
	if !ran {
		t.Fatal("run was not invoked after acquiring leadership")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("run's result must propagate once leadership is acquired, got %v", err)
	}
}

func TestRunWithLeaderElectionCanceledBeforeAcquireReturnsNil(t *testing.T) {
	t.Setenv("POD_NAME", "test-identity")
	cfg := config.Config{LeaderElection: true, LeaderElectionID: "test-lease", LeaderElectionNamespace: "default"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false

	err := runWithLeaderElection(ctx, cfg, fake.NewSimpleClientset(), discardLogger(), func(context.Context) error {
		ran = true
		return errors.New("should not run")
	})
	if err != nil {
		t.Fatalf("expected nil when leadership is never acquired, got %v", err)
	}
	if ran {
		t.Fatal("run must not execute when the context is canceled before acquisition")
	}
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
