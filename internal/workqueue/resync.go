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

// PeriodicEnqueuer retains full-provider audits as the drift safety fallback.
type PeriodicEnqueuer struct {
	Queue    *TargetQueue
	Lister   TargetLister
	Interval time.Duration
	Clock    clock.WithTicker
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
	ticker := p.Clock.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C():
			if err := p.EnqueueAll(ctx); err != nil {
				return err
			}
		}
	}
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
