package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kgskr/fortigate-external-dns/internal/metrics"
)

func TestEventModeInitialListErrorRetriesInsteadOfExiting(t *testing.T) {
	clients := integrationKubernetes(t, nil, nil)
	fakeDynamic := clients.Dynamic.(interface {
		PrependReactor(string, string, k8stesting.ReactionFunc)
	})
	fakeDynamic.PrependReactor("list", "fortigatednstargets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	cfg := isolationConfig()
	cfg.EventDriven = true
	cfg.Resync = 10 * time.Millisecond
	manager := isolationManager(t, clients, newIntegrationClientFactory(), discardLogger(), metrics.New())
	heartbeat := newStaleHeartbeat(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runEventTargetMode(ctx, cfg, clients, manager, metrics.New(), discardLogger(), heartbeat)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !heartbeat.Healthy(30 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("failed initial loads did not mark heartbeat attempts")
		}
		select {
		case err := <-done:
			t.Fatalf("event mode exited on a transient list error: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("runEventTargetMode() = %v, want context canceled", err)
	}
}
