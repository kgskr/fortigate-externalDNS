package workqueue

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"
)

func TestEventStormCoalescesAfterDebounce(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	queue := newTestQueue(t, clock, Config{Debounce: 5 * time.Second})
	defer queue.ShutDown()
	key := mustKey(t, "dns-system", "edge")
	for i := 0; i < 1000; i++ {
		if !queue.Enqueue(key) {
			t.Fatal("valid target event was rejected")
		}
	}
	if queue.Len() != 0 {
		t.Fatalf("debounced queue length = %d before clock advance", queue.Len())
	}
	clock.Step(5*time.Second - time.Nanosecond)
	if queue.Len() != 0 {
		t.Fatal("target became ready before debounce elapsed")
	}
	clock.Step(time.Nanosecond)
	key = getKey(t, queue)
	if completion := queue.Complete(key, nil); completion != CompletionSucceeded {
		t.Fatalf("completion = %s", completion)
	}
	if queue.Len() != 0 {
		t.Fatalf("event storm produced extra queue entries: %d", queue.Len())
	}
}

func TestRetryResetExhaustionAndPeriodicEligibility(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	queue := newTestQueue(t, clock, Config{
		RetryBase: time.Second, RetryMax: 2 * time.Second, MaxRetries: 2, Jitter: 0.01,
	})
	defer queue.ShutDown()
	key := mustKey(t, "dns-system", "edge")

	queue.EnqueuePeriodic(key)
	got := getKey(t, queue)
	if result := queue.Complete(got, errors.New("temporary")); result != CompletionRetried || queue.NumRequeues(key) != 1 {
		t.Fatalf("first failure = %s retries=%d", result, queue.NumRequeues(key))
	}
	clock.Step(2 * time.Second)
	got = getKey(t, queue)
	if result := queue.Complete(got, nil); result != CompletionSucceeded || queue.NumRequeues(key) != 0 {
		t.Fatalf("success did not reset retry state: %s retries=%d", result, queue.NumRequeues(key))
	}

	queue.EnqueuePeriodic(key)
	for retry := 0; retry < 2; retry++ {
		got = getKey(t, queue)
		if result := queue.Complete(got, errors.New("still failing")); result != CompletionRetried {
			t.Fatalf("retry %d completion = %s", retry, result)
		}
		clock.Step(2 * time.Second)
	}
	got = getKey(t, queue)
	if result := queue.Complete(got, errors.New("exhausted")); result != CompletionExhausted || queue.NumRequeues(key) != 0 {
		t.Fatalf("retry exhaustion = %s retries=%d", result, queue.NumRequeues(key))
	}
	if !queue.EnqueuePeriodic(key) {
		t.Fatal("exhausted target is not eligible for periodic audit")
	}
	got = getKey(t, queue)
	queue.Complete(got, nil)
}

func TestOneInFlightWorkerPerTarget(t *testing.T) {
	queue := newTestQueue(t, clocktesting.NewFakeClock(time.Unix(0, 0)), Config{})
	defer queue.ShutDown()
	key := mustKey(t, "dns-system", "edge")
	queue.EnqueuePeriodic(key)
	first := getKey(t, queue)
	queue.EnqueuePeriodic(key)
	if queue.Len() != 0 {
		t.Fatal("same key was made available while already processing")
	}
	if result := queue.Complete(first, nil); result != CompletionSucceeded {
		t.Fatalf("first completion = %s", result)
	}
	second := getKey(t, queue)
	if second != key {
		t.Fatalf("requeued key = %s, want %s", second, key)
	}
	queue.Complete(second, nil)
}

func TestEventDuringFailureRetainsSinglePendingDelivery(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	queue := newTestQueue(t, clock, Config{Debounce: 2 * time.Second, RetryBase: time.Second, RetryMax: time.Second})
	defer queue.ShutDown()
	key := mustKey(t, "dns-system", "edge")
	queue.EnqueuePeriodic(key)
	first := getKey(t, queue)
	queue.Enqueue(key)
	if result := queue.Complete(first, errors.New("provider unavailable")); result != CompletionRetried {
		t.Fatalf("failure completion = %s", result)
	}
	clock.Step(2 * time.Second)
	second := getKey(t, queue)
	queue.Complete(second, nil)
	clock.Step(2 * time.Second)
	if queue.Len() != 0 {
		t.Fatalf("event and retry produced duplicate deliveries: %d", queue.Len())
	}
}

func TestJitteredExponentialBackoffIsDeterministicAndCapped(t *testing.T) {
	key := mustKey(t, "dns-system", "edge")
	first := newJitteredRateLimiter(time.Second, 8*time.Second, 0.2)
	second := newJitteredRateLimiter(time.Second, 8*time.Second, 0.2)
	for attempt := 0; attempt < 10; attempt++ {
		a := first.When(key)
		b := second.When(key)
		if a != b {
			t.Fatalf("attempt %d jitter differs: %s != %s", attempt, a, b)
		}
		if a <= 0 || a > 8*time.Second {
			t.Fatalf("attempt %d delay out of bounds: %s", attempt, a)
		}
	}
	first.Forget(key)
	if first.NumRequeues(key) != 0 {
		t.Fatal("rate limiter forget did not reset retry count")
	}
}

func TestPeriodicEnqueueAllUsesFakeClockAndDeduplicates(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	queue := newTestQueue(t, clock, Config{Debounce: time.Hour})
	defer queue.ShutDown()
	keys := []TargetKey{
		mustKey(t, "dns-system", "b"), mustKey(t, "dns-system", "a"), mustKey(t, "dns-system", "a"),
	}
	periodic := PeriodicEnqueuer{Queue: queue, Lister: staticLister(keys), Interval: time.Minute, Clock: clock}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- periodic.Run(ctx) }()
	waitFor(t, func() bool { return clock.Waiters() >= 2 })
	clock.Step(time.Minute)
	first := getKey(t, queue)
	queue.Complete(first, nil)
	second := getKey(t, queue)
	queue.Complete(second, nil)
	if first.String() != "dns-system/a" || second.String() != "dns-system/b" {
		t.Fatalf("periodic target order = %s, %s", first, second)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("periodic enqueuer did not stop")
	}
}

func TestConcurrentDifferentTargetsRemainIndependent(t *testing.T) {
	queue := newTestQueue(t, clocktesting.NewFakeClock(time.Unix(0, 0)), Config{})
	defer queue.ShutDown()
	for i := 0; i < 20; i++ {
		queue.EnqueuePeriodic(mustKey(t, "dns-system", fmt.Sprintf("target-%02d", i)))
	}
	var workers sync.WaitGroup
	seen := sync.Map{}
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				key, shutdown := queue.Get()
				if shutdown {
					return
				}
				seen.Store(key, struct{}{})
				queue.Complete(key, nil)
				if countSyncMap(&seen) == 20 {
					return
				}
			}
		}()
	}
	waitFor(t, func() bool { return countSyncMap(&seen) == 20 })
	queue.ShutDown()
	workers.Wait()
}

func TestCancelledDebounceWaitersAreReclaimed(t *testing.T) {
	queue := newTestQueue(t, clocktesting.NewFakeClock(time.Unix(0, 0)), Config{Debounce: time.Hour})
	defer queue.ShutDown()
	baseline := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		key := mustKey(t, "dns-system", fmt.Sprintf("target-%03d", i))
		queue.Enqueue(key)
		queue.EnqueuePeriodic(key)
		got := getKey(t, queue)
		queue.Complete(got, nil)
	}
	waitFor(t, func() bool { return runtime.NumGoroutine() <= baseline+8 })
}

type staticLister []TargetKey

func (s staticLister) ListTargetKeys(context.Context) ([]TargetKey, error) {
	return append([]TargetKey(nil), s...), nil
}

func newTestQueue(t *testing.T, fakeClock *clocktesting.FakeClock, override Config) *TargetQueue {
	t.Helper()
	config := Config{
		DisableDebounce: true, RetryBase: time.Second, RetryMax: 8 * time.Second,
		MaxRetries: 3, Jitter: 0.01, Clock: fakeClock,
	}
	if override.Debounce != 0 {
		config.Debounce = override.Debounce
		config.DisableDebounce = false
	}
	if override.RetryBase != 0 {
		config.RetryBase = override.RetryBase
	}
	if override.RetryMax != 0 {
		config.RetryMax = override.RetryMax
	}
	if override.MaxRetries != 0 {
		config.MaxRetries = override.MaxRetries
	}
	if override.Jitter != 0 {
		config.Jitter = override.Jitter
	}
	queue, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return queue
}

func mustKey(t *testing.T, namespace, name string) TargetKey {
	t.Helper()
	key, err := NewTargetKey(namespace, name)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func getKey(t *testing.T, queue *TargetQueue) TargetKey {
	t.Helper()
	result := make(chan TargetKey, 1)
	go func() {
		key, shutdown := queue.Get()
		if !shutdown {
			result <- key
		}
	}()
	select {
	case key := <-result:
		return key
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for target key")
		return TargetKey{}
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		runtime.Gosched()
	}
}

func countSyncMap(values *sync.Map) int {
	count := 0
	values.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}
