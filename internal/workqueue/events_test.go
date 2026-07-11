package workqueue

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"
)

func TestSemanticFingerprintAndUpdateNoiseFiltering(t *testing.T) {
	oldFingerprint, err := NewSemanticFingerprint(EventService, SemanticFields{
		"generation": "7",
		"spec":       "external-name=api.example.net",
	})
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := NewSemanticFingerprint(EventService, SemanticFields{
		"spec":       "external-name=api.example.net",
		"generation": "7",
	})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := NewSemanticFingerprint(EventService, SemanticFields{
		"generation": "8",
		"spec":       "external-name=api.example.net",
	})
	if err != nil {
		t.Fatal(err)
	}
	if oldFingerprint != reordered || oldFingerprint == changed {
		t.Fatalf("semantic fingerprint is not deterministic: old=%s reordered=%s changed=%s", oldFingerprint, reordered, changed)
	}

	queue := newTestQueue(t, clocktesting.NewFakeClock(time.Unix(0, 0)), Config{})
	defer queue.ShutDown()
	mapper := &recordingMapper{keys: []TargetKey{mustKey(t, "dns-system", "b"), mustKey(t, "dns-system", "a"), mustKey(t, "dns-system", "a")}}
	dispatcher := Dispatcher{Queue: queue, Mapper: mapper}

	result, err := dispatcher.Handle(context.Background(), Event{
		Kind: EventService, Action: EventUpdate, Namespace: "apps", Name: "api",
		OldFingerprint: oldFingerprint, NewFingerprint: reordered,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ignored || mapper.calls.Load() != 0 || queue.Len() != 0 {
		t.Fatalf("status-only/noise update was not ignored: result=%#v calls=%d len=%d", result, mapper.calls.Load(), queue.Len())
	}

	result, err = dispatcher.Handle(context.Background(), Event{
		Kind: EventService, Action: EventUpdate, Namespace: "apps", Name: "api",
		OldFingerprint: oldFingerprint, NewFingerprint: changed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Enqueued != 2 || result.Ignored || mapper.calls.Load() != 1 {
		t.Fatalf("meaningful event mapping result = %#v calls=%d", result, mapper.calls.Load())
	}
	first := getKey(t, queue)
	queue.Complete(first, nil)
	second := getKey(t, queue)
	queue.Complete(second, nil)
	if first.String() != "dns-system/a" || second.String() != "dns-system/b" {
		t.Fatalf("mapped target order = %s, %s", first, second)
	}
}

func TestFixedInformerEventKinds(t *testing.T) {
	kinds := []EventKind{
		EventService, EventIngress, EventGateway, EventHTTPRoute, EventEndpointSlice,
		EventTarget, EventPolicy, EventOwnership, EventChangePlan, EventSecret,
	}
	if len(kinds) != len(eventKinds) {
		t.Fatalf("fixed event kind set mismatch: got %d want %d", len(kinds), len(eventKinds))
	}
	for _, kind := range kinds {
		if !validEventKind(kind) {
			t.Fatalf("event kind %s is not recognized", kind)
		}
		if _, err := NewSemanticFingerprint(kind, SemanticFields{"semantic": "state"}); err != nil {
			t.Fatalf("event kind %s fingerprint failed: %v", kind, err)
		}
	}
	if _, err := NewSemanticFingerprint(EventKind("Pod"), nil); err == nil {
		t.Fatal("unsupported informer kind unexpectedly accepted")
	}
}

func TestTargetDeletionForgetsAndAddReactivates(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	queue := newTestQueue(t, clock, Config{RetryBase: time.Second, RetryMax: time.Second, MaxRetries: 2, Jitter: 0.01})
	defer queue.ShutDown()
	key := mustKey(t, "dns-system", "edge")
	mapper := &recordingMapper{keys: []TargetKey{key}}
	dispatcher := Dispatcher{Queue: queue, Mapper: mapper}

	queue.EnqueuePeriodic(key)
	got := getKey(t, queue)
	if completion := queue.Complete(got, context.DeadlineExceeded); completion != CompletionRetried || queue.NumRequeues(key) != 1 {
		t.Fatalf("initial retry state = %s/%d", completion, queue.NumRequeues(key))
	}
	result, err := dispatcher.Handle(context.Background(), Event{Kind: EventTarget, Action: EventDelete, Namespace: key.Namespace, Name: key.Name})
	if err != nil {
		t.Fatal(err)
	}
	if result.Forgotten != 1 || queue.NumRequeues(key) != 0 {
		t.Fatalf("target delete did not forget state: result=%#v retries=%d", result, queue.NumRequeues(key))
	}
	clock.Step(2 * time.Second)
	waitFor(t, func() bool { return queue.Len() == 0 })
	if queue.EnqueuePeriodic(key) {
		t.Fatal("deleted target accepted periodic work")
	}

	result, err = dispatcher.Handle(context.Background(), Event{Kind: EventTarget, Action: EventAdd, Namespace: key.Namespace, Name: key.Name})
	if err != nil {
		t.Fatal(err)
	}
	if result.Enqueued != 1 {
		t.Fatalf("target add did not reactivate queue: %#v", result)
	}
	got = getKey(t, queue)
	if got != key {
		t.Fatalf("reactivated key = %s, want %s", got, key)
	}
	queue.Complete(got, nil)
}

func TestUpdateRequiresSemanticFingerprints(t *testing.T) {
	queue := newTestQueue(t, clocktesting.NewFakeClock(time.Unix(0, 0)), Config{})
	defer queue.ShutDown()
	dispatcher := Dispatcher{Queue: queue, Mapper: &recordingMapper{}}
	if _, err := dispatcher.Handle(context.Background(), Event{Kind: EventPolicy, Action: EventUpdate}); err == nil || !strings.Contains(err.Error(), "fingerprints") {
		t.Fatalf("invalid update error = %v", err)
	}
}

type recordingMapper struct {
	keys  []TargetKey
	calls atomic.Int32
}

func (m *recordingMapper) TargetsForEvent(_ context.Context, _ Event) ([]TargetKey, error) {
	m.calls.Add(1)
	return append([]TargetKey(nil), m.keys...), nil
}
