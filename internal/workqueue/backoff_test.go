package workqueue

import (
	"errors"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"
)

func failOnce(t *testing.T, queue *TargetQueue, want Completion) {
	t.Helper()
	got := getKey(t, queue)
	if result := queue.Complete(got, errors.New("provider unavailable")); result != want {
		t.Fatalf("completion = %s, want %s", result, want)
	}
}

func TestEventChurnDoesNotCancelRetryBackoff(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	queue := newTestQueue(t, clock, Config{Debounce: time.Second, RetryBase: 8 * time.Second, RetryMax: 8 * time.Second})
	defer queue.ShutDown()
	key := mustKey(t, "dns-system", "edge")
	queue.EnqueuePeriodic(key)
	failOnce(t, queue, CompletionRetried)

	for elapsed := time.Duration(0); elapsed < 7*time.Second; elapsed += time.Second {
		if !queue.Enqueue(key) {
			t.Fatal("event rejected")
		}
		clock.Step(time.Second)
		if queue.Len() != 0 {
			t.Fatalf("event churn delivered the key after %s, before its backoff elapsed", elapsed+time.Second)
		}
	}
	clock.Step(1100 * time.Millisecond)
	getKey(t, queue)
}

func TestEventDeliversAtDebounceWhenBackoffIsShorter(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	queue := newTestQueue(t, clock, Config{Debounce: 5 * time.Second, RetryBase: time.Second, RetryMax: time.Second})
	defer queue.ShutDown()
	key := mustKey(t, "dns-system", "edge")
	queue.EnqueuePeriodic(key)
	failOnce(t, queue, CompletionRetried)
	queue.Enqueue(key)
	clock.Step(2 * time.Second)
	if queue.Len() != 0 {
		t.Fatal("key delivered before the debounce deadline")
	}
	clock.Step(3 * time.Second)
	getKey(t, queue)
}

func TestEventDuringFailureHonorsLongerBackoff(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	queue := newTestQueue(t, clock, Config{Debounce: time.Second, RetryBase: 8 * time.Second, RetryMax: 8 * time.Second})
	defer queue.ShutDown()
	key := mustKey(t, "dns-system", "edge")
	queue.EnqueuePeriodic(key)
	got := getKey(t, queue)
	queue.Enqueue(key)
	if result := queue.Complete(got, errors.New("provider unavailable")); result != CompletionRetried {
		t.Fatalf("completion = %s", result)
	}
	clock.Step(2 * time.Second)
	if queue.Len() != 0 {
		t.Fatal("pending event delivered the key before its backoff elapsed")
	}
	clock.Step(6200 * time.Millisecond)
	getKey(t, queue)
}

func TestExhaustedKeyStaysAtMaxBackoffUntilSuccess(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	queue := newTestQueue(t, clock, Config{Debounce: time.Second, RetryBase: time.Second, RetryMax: 8 * time.Second, MaxRetries: 1})
	defer queue.ShutDown()
	key := mustKey(t, "dns-system", "edge")
	queue.EnqueuePeriodic(key)
	failOnce(t, queue, CompletionRetried)
	clock.Step(2 * time.Second)
	failOnce(t, queue, CompletionExhausted)
	if queue.NumRequeues(key) != 1 {
		t.Fatalf("exhaustion reset the retry counter to %d", queue.NumRequeues(key))
	}

	for round := 0; round < 2; round++ {
		queue.Enqueue(key)
		clock.Step(7 * time.Second)
		if queue.Len() != 0 {
			t.Fatalf("round %d: exhausted key was delivered before the maximum backoff elapsed", round)
		}
		clock.Step(1100 * time.Millisecond)
		failOnce(t, queue, CompletionExhausted)
	}

	queue.Enqueue(key)
	clock.Step(8100 * time.Millisecond)
	got := getKey(t, queue)
	if result := queue.Complete(got, nil); result != CompletionSucceeded || queue.NumRequeues(key) != 0 {
		t.Fatalf("success = %s retries=%d", result, queue.NumRequeues(key))
	}
	queue.Enqueue(key)
	clock.Step(1100 * time.Millisecond)
	getKey(t, queue)
}
