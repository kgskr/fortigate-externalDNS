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

func TestFailureBackoffDelaysEventsFromInFlightAttempt(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		for _, alreadyDirty := range []bool{false, true} {
			name := "retry/pending"
			if exhausted {
				name = "exhausted/pending"
			}
			if alreadyDirty {
				name += "-already-delivered"
			}
			t.Run(name, func(t *testing.T) {
				clock := clocktesting.NewFakeClock(time.Unix(0, 0))
				queue := newTestQueue(t, clock, Config{Debounce: time.Second, RetryBase: 8 * time.Second, RetryMax: 8 * time.Second, MaxRetries: 1})
				defer queue.ShutDown()
				key := mustKey(t, "dns-system", "edge")
				queue.EnqueuePeriodic(key)
				if exhausted {
					failOnce(t, queue, CompletionRetried)
					clock.Step(9 * time.Second)
				}
				got := getKey(t, queue)
				// A source changes while the provider request is in flight.
				queue.Enqueue(key)
				if alreadyDirty {
					clock.Step(time.Second)
					waitFor(t, func() bool {
						queue.mu.RLock()
						defer queue.mu.RUnlock()
						_, pending := queue.pending[key]
						return !pending
					})
				}
				want := CompletionRetried
				if exhausted {
					want = CompletionExhausted
				}
				if result := queue.Complete(got, errors.New("provider unavailable")); result != want {
					t.Fatalf("completion = %s, want %s", result, want)
				}
				delivered := make(chan TargetKey, 1)
				go func() {
					if next, shutdown := queue.Get(); !shutdown {
						delivered <- next
					}
				}()
				clock.Step(7 * time.Second)
				select {
				case <-delivered:
					t.Fatal("in-flight event bypassed the failure backoff")
				case <-time.After(10 * time.Millisecond):
				}
				clock.Step(2 * time.Second)
				select {
				case next := <-delivered:
					queue.Complete(next, nil)
				case <-time.After(time.Second):
					t.Fatal("event was lost after backoff elapsed")
				}
				clock.Step(10 * time.Second)
				if queue.Len() != 0 || queue.NumRequeues(key) != 0 {
					t.Fatal("successful retry left duplicate work or retry state")
				}
			})
		}
	}
}

func TestPeriodicDuringFailedAttemptBypassesBackoffWithoutDuplicateRetry(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	queue := newTestQueue(t, clock, Config{RetryBase: 8 * time.Second, RetryMax: 8 * time.Second})
	defer queue.ShutDown()
	key := mustKey(t, "dns-system", "edge")
	queue.EnqueuePeriodic(key)
	got := getKey(t, queue)
	queue.EnqueuePeriodic(key)
	queue.Complete(got, errors.New("provider unavailable"))
	// Periodic full audits intentionally remain immediately eligible.
	got = getKey(t, queue)
	queue.Complete(got, nil)
	clock.Step(10 * time.Second)
	queue.mu.RLock()
	_, retrying := queue.retrying[key]
	queue.mu.RUnlock()
	if retrying || queue.Len() != 0 {
		t.Fatal("periodic audit left a duplicate delayed retry")
	}
}

func TestHeldDirtyDeliveryCanBeForgottenOrShutDown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "forget-and-reactivate"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			clock := clocktesting.NewFakeClock(time.Unix(0, 0))
			queue := newTestQueue(t, clock, Config{RetryBase: 8 * time.Second, RetryMax: 8 * time.Second, MaxRetries: 1})
			defer queue.ShutDown()
			key := mustKey(t, "dns-system", "edge")
			queue.EnqueuePeriodic(key)
			failOnce(t, queue, CompletionRetried)
			clock.Step(9 * time.Second)
			got := getKey(t, queue)
			queue.Enqueue(key)
			queue.Complete(got, errors.New("provider unavailable"))
			stopped := make(chan bool, 1)
			go func() {
				_, shutdown := queue.Get()
				stopped <- shutdown
			}()
			waitFor(t, func() bool {
				queue.mu.RLock()
				defer queue.mu.RUnlock()
				_, retrying := queue.retrying[key]
				return retrying
			})
			if shutdown {
				queue.ShutDown()
			} else {
				queue.ForgetTarget(key)
				queue.ActivateTarget(key)
				queue.Enqueue(key)
			}
			select {
			case gotShutdown := <-stopped:
				if gotShutdown != shutdown {
					t.Fatalf("Get shutdown = %v, want %v", gotShutdown, shutdown)
				}
			case <-time.After(time.Second):
				t.Fatal("held delivery did not stop or reactivate")
			}
			if !shutdown {
				queue.Complete(key, nil)
				clock.Step(10 * time.Second)
				if queue.Len() != 0 {
					t.Fatal("forgotten hold delivered work after reactivation")
				}
			}
		})
	}
}
