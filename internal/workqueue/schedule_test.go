package workqueue

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
)

func TestAuditScheduleHonorsIndependentCadencesAndRevisions(t *testing.T) {
	fast, slow, inherited := mustKey(t, "dns", "fast"), mustKey(t, "dns", "slow"), mustKey(t, "dns", "inherited")
	targets := []TargetSchedule{{Key: fast, Revision: "v1", Interval: 10 * time.Second}, {Key: slow, Revision: "v1", Interval: time.Hour}, {Key: inherited}}
	start := time.Unix(0, 0)
	var schedule AuditSchedule
	check := func(at time.Duration, want ...TargetKey) {
		t.Helper()
		got, err := schedule.Due(start.Add(at), targets, time.Minute, true)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("at %s: due=%v err=%v, want %v", at, got, err, want)
		}
	}
	check(0, fast, inherited, slow)
	if delay := schedule.Delay(start, time.Minute); delay != 10*time.Second {
		t.Fatalf("next wait = %s", delay)
	}
	check(9 * time.Second)
	check(10*time.Second, fast)
	check(time.Minute, fast, inherited)
	// A config or credential revision triggers an immediate polling audit.
	targets[1].Revision = "v2"
	check(time.Minute, slow)
	// Missing targets are forgotten; recreating the same identity is immediate.
	targets = targets[:1]
	check(time.Minute)
	targets = append(targets, TargetSchedule{Key: slow, Revision: "v2", Interval: time.Hour})
	check(time.Minute, slow)
	// Long missed windows coalesce to one audit, not a catch-up burst.
	check(2*time.Hour, fast, slow)
	check(2 * time.Hour)
}

func TestEventScheduleDefersNewRevisionToItsCadence(t *testing.T) {
	key := mustKey(t, "dns", "edge")
	var schedule AuditSchedule
	now := time.Unix(0, 0)
	targets := []TargetSchedule{{Key: key, Revision: "v1", Interval: time.Hour}}
	for _, target := range []TargetSchedule{targets[0], {Key: key, Revision: "v2", Interval: time.Second}} {
		targets[0] = target
		if due, err := schedule.Due(now, targets, time.Minute, false); err != nil || len(due) != 0 {
			t.Fatalf("target event should be delivered by debounce, due=%v err=%v", due, err)
		}
	}
	if delay := schedule.Delay(now, time.Minute); delay != time.Second {
		t.Fatalf("updated cadence did not shorten wait: %s", delay)
	}
	if due, err := schedule.Due(now.Add(time.Second), targets, time.Minute, false); err != nil || !slices.Equal(due, []TargetKey{key}) {
		t.Fatalf("updated periodic audit=%v err=%v", due, err)
	}
}

type scheduledLister struct {
	mu      sync.Mutex
	targets []TargetSchedule
}

func (l *scheduledLister) ListTargetKeys(ctx context.Context) ([]TargetKey, error) {
	targets, _ := l.ListTargetSchedules(ctx)
	keys := make([]TargetKey, len(targets))
	for i := range targets {
		keys[i] = targets[i].Key
	}
	return keys, nil
}

func (l *scheduledLister) ListTargetSchedules(context.Context) ([]TargetSchedule, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]TargetSchedule(nil), l.targets...), nil
}

type resetObservedClock struct {
	*clocktesting.FakeClock
	resets chan time.Duration
}

func (c resetObservedClock) NewTimer(d time.Duration) clock.Timer {
	return resetObservedTimer{Timer: c.FakeClock.NewTimer(d), resets: c.resets}
}

type resetObservedTimer struct {
	clock.Timer
	resets chan time.Duration
}

func (t resetObservedTimer) Reset(d time.Duration) bool {
	active := t.Timer.Reset(d)
	t.resets <- d
	return active
}

func TestPeriodicEnqueuerUsesTargetIntervalsAndWakesForChanges(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Unix(0, 0))
	queue := newTestQueue(t, clock, Config{DisableDebounce: true})
	defer queue.ShutDown()
	fast, slow := mustKey(t, "dns", "fast"), mustKey(t, "dns", "slow")
	lister := &scheduledLister{targets: []TargetSchedule{{Key: fast, Interval: 10 * time.Second}, {Key: slow, Interval: time.Hour}}}
	changed := make(chan struct{}, 1)
	resets := make(chan time.Duration, 16)
	p := PeriodicEnqueuer{Queue: queue, Lister: lister, Interval: time.Minute, Clock: resetObservedClock{clock, resets}, Changed: changed}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	waitFor(t, func() bool { return clock.Waiters() >= 2 })
	clock.Step(10 * time.Second)
	got := getKey(t, queue)
	if got != fast {
		t.Fatalf("first periodic target = %v", got)
	}
	queue.Complete(got, nil)
	waitFor(t, func() bool { return clock.Waiters() >= 2 })
	clock.Step(50 * time.Second)
	got = getKey(t, queue)
	if got != fast {
		t.Fatalf("global resync audited the slow target: %v", got)
	}
	queue.Complete(got, nil)
	if queue.Len() != 0 {
		t.Fatal("slow target was audited at the global cadence")
	}
	// The wake causes a new target cadence to take effect without waiting for
	// its previous one-hour deadline (or the global one-minute fallback).
	lister.mu.Lock()
	lister.targets[1].Interval = time.Second
	lister.mu.Unlock()
	changed <- struct{}{}
	// Wait for the actual timer reset, not just consumption of the wake signal.
	deadline := time.After(time.Second)
waitReset:
	for {
		select {
		case d := <-resets:
			if d == time.Second {
				break waitReset
			}
		case <-deadline:
			t.Fatal("target change did not reset periodic timer")
		}
	}
	clock.Step(time.Second)
	got = getKey(t, queue)
	if got != slow {
		t.Fatalf("updated periodic target = %v", got)
	}
	queue.Complete(got, nil)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}
