package controller

import (
	"context"
	"testing"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
)

func TestRuntimeQueueReadsUpdatedTargetDebounceFromCache(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	clients, _, _ := newPlatformTestClients(t)
	runtime, err := NewPlatformRuntime(clients, PlatformRuntimeConfig{
		Namespace: "dns-system", Sources: []string{"service"}, PeriodicInterval: time.Hour,
		Clock: clock, Queue: platformqueue.Config{Debounce: time.Second},
	}, newRecordingExecutor())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.queue.ShutDown()
	key, _ := platformqueue.NewTargetKey("dns-system", "edge")
	target := newAPITarget("dns-system", "edge", nil)
	for _, duration := range []time.Duration{4 * time.Second, 2 * time.Second, 0} {
		target.Spec.Debounce = metav1.Duration{Duration: duration}
		object, err := api.ToUnstructured(target)
		if err != nil {
			t.Fatal(err)
		}
		if err := runtime.mapper.targets.GetIndexer().Update(object); err != nil {
			t.Fatal(err)
		}
		if got := runtime.mapper.TargetDebounce(key); got != duration {
			t.Fatalf("cached debounce = %s, want %s", got, duration)
		}
		runtime.queue.Enqueue(key)
		wantDelay := duration
		if wantDelay == 0 {
			wantDelay = time.Second
		}
		clock.Step(wantDelay - time.Nanosecond)
		if runtime.queue.Len() != 0 {
			t.Fatalf("debounce %s delivered early", duration)
		}
		clock.Step(time.Nanosecond)
		waitUntil(t, func() bool { return runtime.queue.Len() == 1 })
		got, shutdown := runtime.queue.Get()
		if shutdown || got != key {
			t.Fatalf("queue.Get() = %s, shutdown = %v", got, shutdown)
		}
		runtime.queue.Complete(got, nil)
	}
}

func TestRuntimeIdleRequiresSynchronizedCachesAndNoActiveWork(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	clients, _, _ := newPlatformTestClients(t, newAPITarget("dns-system", "edge", nil))
	executor := newRecordingExecutor()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	executor.audit = func(ctx context.Context, _ platformqueue.TargetKey) (TargetAudit, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return TargetAudit{}, nil
		case <-ctx.Done():
			return TargetAudit{}, ctx.Err()
		}
	}
	runtime := newTestPlatformRuntime(t, clients, clock, executor)
	if runtime.Idle() {
		t.Fatal("runtime reported idle before caches synchronized")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPlatformRuntime(runtime, ctx)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not begin initial audit")
	}
	if runtime.Idle() {
		t.Fatal("blocked audit worker was mistaken for an idle runtime")
	}
	close(release)
	receiveKey(t, executor.applied)
	waitUntil(t, runtime.Idle)
	cancel()
	if err := receiveRunResult(t, done); err != nil {
		t.Fatal(err)
	}
	if runtime.Idle() {
		t.Fatal("stopped runtime reported idle")
	}
}

func TestRuntimeIdleRejectsQueuedWork(t *testing.T) {
	clients, _, _ := newPlatformTestClients(t)
	runtime, err := NewPlatformRuntime(clients, PlatformRuntimeConfig{
		Namespace: "dns-system", PeriodicInterval: time.Hour,
		Queue: platformqueue.Config{DisableDebounce: true},
	}, newRecordingExecutor())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.queue.ShutDown()
	runtime.cacheReady.Store(true)
	if !runtime.Idle() {
		t.Fatal("empty synchronized runtime should be idle")
	}
	key, _ := platformqueue.NewTargetKey("dns-system", "edge")
	runtime.queue.EnqueuePeriodic(key)
	if runtime.Idle() {
		t.Fatal("queued audit was mistaken for an idle runtime")
	}
}
