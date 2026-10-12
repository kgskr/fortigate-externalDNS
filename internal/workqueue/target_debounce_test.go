package workqueue

import (
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"
)

func TestDebounceOverridesAreIndependentAndReadOnNextEnqueue(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	left := mustKey(t, "dns-system", "left")
	right := mustKey(t, "dns-system", "right")
	fallback := mustKey(t, "dns-system", "fallback")
	overrides := map[TargetKey]time.Duration{left: 2 * time.Second, right: 5 * time.Second}
	calls := map[TargetKey]int{}
	queue, err := New(Config{
		Clock: clock, Debounce: 3 * time.Second,
		DebounceFor: func(key TargetKey) time.Duration {
			calls[key]++
			return overrides[key]
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.ShutDown()
	for _, key := range []TargetKey{left, right, fallback} {
		if !queue.Enqueue(key) || calls[key] != 1 {
			t.Fatalf("target %s must resolve its debounce once per enqueue", key)
		}
	}
	clock.Step(2*time.Second - time.Nanosecond)
	if queue.Len() != 0 {
		t.Fatal("a target was delivered before its configured debounce")
	}
	clock.Step(time.Nanosecond)
	if got := getKey(t, queue); got != left {
		t.Fatalf("first target = %s, want %s", got, left)
	}
	queue.Complete(left, nil)
	clock.Step(time.Second)
	if got := getKey(t, queue); got != fallback {
		t.Fatalf("fallback target = %s, want %s", got, fallback)
	}
	queue.Complete(fallback, nil)
	clock.Step(2 * time.Second)
	if got := getKey(t, queue); got != right {
		t.Fatalf("last target = %s, want %s", got, right)
	}
	queue.Complete(right, nil)

	// The callback may change between semantic events without rebuilding the
	// queue. A new target spec affects the next independently scheduled event.
	overrides[left] = 7 * time.Second
	queue.Enqueue(left)
	clock.Step(7*time.Second - time.Nanosecond)
	if queue.Len() != 0 {
		t.Fatal("updated target retained its old debounce")
	}
	clock.Step(time.Nanosecond)
	if got := getKey(t, queue); got != left || calls[left] != 2 {
		t.Fatalf("updated target = %s, callback calls = %d", got, calls[left])
	}
	queue.Complete(left, nil)
}

func TestDisableDebounceIgnoresTargetOverride(t *testing.T) {
	queue, err := New(Config{
		DisableDebounce: true,
		DebounceFor: func(TargetKey) time.Duration {
			t.Fatal("disabled debounce must not consult target settings")
			return time.Hour
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.ShutDown()
	key := mustKey(t, "dns-system", "edge")
	queue.Enqueue(key)
	if queue.Len() != 1 {
		t.Fatal("disabled debounce did not immediately enqueue target")
	}
	if got := getKey(t, queue); got != key {
		t.Fatalf("target = %s, want %s", got, key)
	}
	queue.Complete(key, nil)
}
