package workqueue

import (
	"context"
	"errors"
	"sort"
	"time"

	"k8s.io/utils/clock"
)

// TargetLister supplies the current configured targets for periodic full
// audits. Implementations will be backed by the target informer cache.
type TargetLister interface {
	ListTargetKeys(context.Context) ([]TargetKey, error)
}

type TargetScheduleLister interface {
	ListTargetSchedules(context.Context) ([]TargetSchedule, error)
}

// PeriodicEnqueuer retains full-provider audits as the drift safety fallback.
type PeriodicEnqueuer struct {
	Queue    *TargetQueue
	Lister   TargetLister
	Interval time.Duration
	Clock    clock.WithTicker
	// Changed wakes the scheduler when a target's requested cadence changes.
	Changed <-chan struct{}
}

func (p PeriodicEnqueuer) Run(ctx context.Context) error {
	if p.Queue == nil || p.Lister == nil {
		return errors.New("periodic enqueuer requires queue and target lister")
	}
	if p.Interval <= 0 {
		return errors.New("periodic interval must be positive")
	}
	if p.Clock == nil {
		p.Clock = clock.RealClock{}
	}
	var schedule AuditSchedule
	if _, err := p.enqueueDue(ctx, &schedule); err != nil {
		return err
	}
	timer := p.Clock.NewTimer(schedule.Delay(p.Clock.Now(), p.Interval))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C():
		case <-p.Changed:
		}
		if _, err := p.enqueueDue(ctx, &schedule); err != nil {
			return err
		}
		if !timer.Stop() {
			select {
			case <-timer.C():
			default:
			}
		}
		timer.Reset(schedule.Delay(p.Clock.Now(), p.Interval))
	}
}

func (p PeriodicEnqueuer) enqueueDue(ctx context.Context, schedule *AuditSchedule) ([]TargetKey, error) {
	var targets []TargetSchedule
	if lister, ok := p.Lister.(TargetScheduleLister); ok {
		var err error
		targets, err = lister.ListTargetSchedules(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		keys, err := p.Lister.ListTargetKeys(ctx)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			targets = append(targets, TargetSchedule{Key: key})
		}
	}
	due, err := schedule.Due(p.Clock.Now(), targets, p.Interval, false)
	if err != nil {
		return nil, err
	}
	for _, key := range due {
		p.Queue.EnqueuePeriodic(key)
	}
	return due, nil
}

func (p PeriodicEnqueuer) EnqueueAll(ctx context.Context) error {
	if p.Queue == nil || p.Lister == nil {
		return errors.New("periodic enqueuer requires queue and target lister")
	}
	keys, err := p.Lister.ListTargetKeys(ctx)
	if err != nil {
		return err
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	seen := make(map[TargetKey]struct{}, len(keys))
	for _, key := range keys {
		if !key.valid() {
			return errors.New("target lister returned an invalid key")
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		p.Queue.EnqueuePeriodic(key)
	}
	return nil
}
