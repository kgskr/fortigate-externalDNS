package workqueue

import (
	"errors"
	"sort"
	"time"
)

// TargetSchedule binds a cadence to one version of a target. Revision must not
// change for status-only updates.
type TargetSchedule struct {
	Key      TargetKey
	Revision string
	Interval time.Duration
}

type auditDeadline struct {
	revision string
	interval time.Duration
	next     time.Time
}

// AuditSchedule is owned by a single scheduling loop. Missed ticks coalesce
// into one audit, and removed targets lose their old deadlines.
type AuditSchedule struct {
	deadlines map[TargetKey]auditDeadline
}

// Due updates the schedule and returns only targets whose audit is due. Polling
// callers set immediateNew; event callers leave it false because informer
// events already enqueue new or changed targets through their debounce gate.
func (s *AuditSchedule) Due(now time.Time, targets []TargetSchedule, fallback time.Duration, immediateNew bool) ([]TargetKey, error) {
	if fallback <= 0 {
		return nil, errors.New("audit fallback interval must be positive")
	}
	next := make(map[TargetKey]auditDeadline, len(targets))
	var due []TargetKey
	for _, target := range targets {
		if !target.Key.valid() || target.Interval < 0 {
			return nil, errors.New("invalid target audit schedule")
		}
		if _, duplicate := next[target.Key]; duplicate {
			continue
		}
		interval := target.Interval
		if interval == 0 {
			interval = fallback
		}
		entry, exists := s.deadlines[target.Key]
		if !exists || entry.revision != target.Revision || entry.interval != interval {
			entry = auditDeadline{revision: target.Revision, interval: interval, next: now.Add(interval)}
			if immediateNew {
				entry.next = now
			}
		}
		if !entry.next.After(now) {
			due = append(due, target.Key)
			entry.next = now.Add(interval)
		}
		next[target.Key] = entry
	}
	s.deadlines = next
	sort.Slice(due, func(i, j int) bool { return due[i].String() < due[j].String() })
	return due, nil
}

// Delay caps the idle wait at fallback so polling can still discover target or
// credential changes, even when every target requests a longer audit interval.
func (s *AuditSchedule) Delay(now time.Time, fallback time.Duration) time.Duration {
	delay := fallback
	for _, entry := range s.deadlines {
		remaining := entry.next.Sub(now)
		if remaining < delay {
			delay = remaining
		}
	}
	if delay < 0 {
		return 0
	}
	return delay
}
