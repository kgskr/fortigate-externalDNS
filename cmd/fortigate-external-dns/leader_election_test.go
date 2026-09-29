package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kgskr/fortigate-external-dns/internal/config"
)

func TestRunAsLeaderReleasesLeaseOnlyAfterRunReturns(t *testing.T) {
	stop, cancelStop := context.WithCancel(context.Background())
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()

	var runReturned, releasedBeforeReturn atomic.Bool
	release := func() {
		if !runReturned.Load() {
			releasedBeforeReturn.Store(true)
		}
	}
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runAsLeader(stop, leaderCtx, func(workCtx context.Context) error {
			close(entered)
			<-workCtx.Done()
			// Simulate an in-flight apply that finishes after cancellation.
			time.Sleep(50 * time.Millisecond)
			runReturned.Store(true)
			return workCtx.Err()
		}, release)
	}()
	<-entered
	cancelStop()
	err := <-done
	if releasedBeforeReturn.Load() {
		t.Fatal("lease was released while run() was still applying")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown result = %v, want context canceled (treated as clean by main)", err)
	}
}

func TestRunAsLeaderReportsLostLeadershipAsError(t *testing.T) {
	stop := context.Background()
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	released := false
	err := runAsLeader(stop, leaderCtx, func(workCtx context.Context) error {
		cancelLeader() // client-go cancels the leader context on renew failure
		<-workCtx.Done()
		return workCtx.Err()
	}, func() { released = true })
	if !errors.Is(err, errLeadershipLost) {
		t.Fatalf("error = %v, want errLeadershipLost", err)
	}
	if !released {
		t.Fatal("elector was not released after run returned")
	}
}

func TestRunAsLeaderKeepsRunErrorAndCleanReturn(t *testing.T) {
	boom := errors.New("boom")
	leaderCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runAsLeader(context.Background(), leaderCtx, func(context.Context) error { return boom }, func() {}); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want run's own error", err)
	}
	if err := runAsLeader(context.Background(), leaderCtx, func(context.Context) error { return nil }, func() {}); err != nil {
		t.Fatalf("a run that returns nil on its own = %v", err)
	}
}

func TestRunAsLeaderSkipsWorkWhenAlreadyCanceled(t *testing.T) {
	for _, cancelSource := range []string{"shutdown", "leadership"} {
		t.Run(cancelSource, func(t *testing.T) {
			stop, cancelStop := context.WithCancel(context.Background())
			defer cancelStop()
			leaderCtx, cancelLeader := context.WithCancel(context.Background())
			defer cancelLeader()
			want := context.Canceled
			if cancelSource == "shutdown" {
				cancelStop()
			} else {
				cancelLeader()
				want = errLeadershipLost
			}
			ran, released := false, false
			err := runAsLeader(stop, leaderCtx, func(context.Context) error {
				ran = true
				return nil
			}, func() { released = true })
			if ran || !released || !errors.Is(err, want) {
				t.Fatalf("run=%v released=%v err=%v, want no work, release, and %v", ran, released, err, want)
			}
		})
	}
}

func TestRunWithLeaderElectionCanceledDuringAcquireSkipsWork(t *testing.T) {
	t.Setenv("POD_NAME", "pod-a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := fake.NewSimpleClientset()
	// The API commits acquisition as shutdown arrives, before client-go can
	// schedule OnStartedLeading. The late callback must not invoke run.
	client.PrependReactor("create", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		cancel()
		return false, nil, nil
	})
	cfg := config.Config{LeaderElection: true, LeaderElectionID: "cancel-acquire", LeaderElectionNamespace: "default"}
	var ran atomic.Bool
	err := runWithLeaderElection(ctx, cfg, client, discardLogger(), func(context.Context) error {
		ran.Store(true)
		return errors.New("work started during shutdown")
	})
	if err != nil || ran.Load() {
		t.Fatalf("canceled acquisition must skip work: run=%v err=%v", ran.Load(), err)
	}
	lease, err := client.CoordinationV1().Leases("default").Get(context.Background(), "cancel-acquire", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "" {
		t.Fatalf("canceled acquisition left the lease held by %q", *lease.Spec.HolderIdentity)
	}
}

func shortLeaderTiming(t *testing.T) {
	t.Helper()
	previous := leaderElectionTiming
	leaderElectionTiming.leaseDuration = 1500 * time.Millisecond
	leaderElectionTiming.renewDeadline = time.Second
	leaderElectionTiming.retryPeriod = 100 * time.Millisecond
	t.Cleanup(func() { leaderElectionTiming = previous })
}

func TestRunWithLeaderElectionShutdownDrainsRunThenReleasesLease(t *testing.T) {
	shortLeaderTiming(t)
	t.Setenv("POD_NAME", "pod-a")
	client := fake.NewSimpleClientset()
	cfg := config.Config{LeaderElection: true, LeaderElectionID: "drain-lease", LeaderElectionNamespace: "default"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entered := make(chan struct{})
	var runReturned atomic.Bool
	var holderWhileRunning atomic.Value
	holderWhileRunning.Store("unset")
	done := make(chan error, 1)
	go func() {
		done <- runWithLeaderElection(ctx, cfg, client, discardLogger(), func(workCtx context.Context) error {
			close(entered)
			<-workCtx.Done()
			// The Lease must still be held while the work drains.
			lease, err := client.CoordinationV1().Leases("default").Get(context.Background(), "drain-lease", metav1.GetOptions{})
			if err == nil && lease.Spec.HolderIdentity != nil {
				holderWhileRunning.Store(*lease.Spec.HolderIdentity)
			}
			time.Sleep(100 * time.Millisecond)
			runReturned.Store(true)
			return workCtx.Err()
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("never became leader")
	}
	cancel()
	select {
	case err := <-done:
		if !runReturned.Load() {
			t.Fatal("runWithLeaderElection returned before run() finished")
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not complete")
	}
	if holder := holderWhileRunning.Load().(string); holder != "pod-a" {
		t.Fatalf("lease holder while draining = %q, want pod-a (released too early)", holder)
	}
	lease, err := client.CoordinationV1().Leases("default").Get(context.Background(), "drain-lease", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "" {
		t.Fatalf("lease still held by %q after shutdown", *lease.Spec.HolderIdentity)
	}
}

func TestRunWithLeaderElectionLossWithoutShutdownIsAnError(t *testing.T) {
	shortLeaderTiming(t)
	t.Setenv("POD_NAME", "pod-a")
	client := fake.NewSimpleClientset()
	cfg := config.Config{LeaderElection: true, LeaderElectionID: "loss-lease", LeaderElectionNamespace: "default"}

	var failUpdates atomic.Bool
	client.PrependReactor("update", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		if failUpdates.Load() {
			return true, nil, errors.New("apiserver unavailable")
		}
		return false, nil, nil
	})
	done := make(chan error, 1)
	go func() {
		done <- runWithLeaderElection(context.Background(), cfg, client, discardLogger(), func(workCtx context.Context) error {
			failUpdates.Store(true)
			<-workCtx.Done()
			return workCtx.Err()
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errLeadershipLost) {
			t.Fatalf("error = %v, want errLeadershipLost", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("leadership loss did not end the process loop")
	}
}
