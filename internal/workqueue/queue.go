package workqueue

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sync"
	"time"

	clientworkqueue "k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
)

const (
	DefaultDebounce   = 500 * time.Millisecond
	DefaultRetryBase  = time.Second
	DefaultRetryMax   = time.Minute
	DefaultMaxRetries = 8
	DefaultJitter     = 0.2
)

// Config controls target debounce and bounded retry behavior.
type Config struct {
	Name     string
	Debounce time.Duration
	// DisableDebounce is intended for one-shot/tests. A zero Debounce without
	// this flag selects DefaultDebounce.
	DisableDebounce bool
	RetryBase       time.Duration
	RetryMax        time.Duration
	MaxRetries      int
	Jitter          float64
	Clock           clock.WithTicker
}

// Completion describes how a processed target was finalized.
type Completion string

const (
	CompletionSucceeded Completion = "succeeded"
	CompletionRetried   Completion = "retried"
	CompletionExhausted Completion = "exhausted"
	CompletionForgotten Completion = "forgotten"
)

// TargetQueue wraps the client-go typed rate-limiting queue. client-go's dirty
// and processing sets guarantee coalescing and one in-flight worker per key.
type TargetQueue struct {
	queue      clientworkqueue.TypedRateLimitingInterface[TargetKey]
	limiter    clientworkqueue.TypedRateLimiter[TargetKey]
	debounce   time.Duration
	maxRetries int
	clock      clock.Clock

	mu        sync.RWMutex
	deleted   map[TargetKey]struct{}
	pending   map[TargetKey]pendingDebounce
	retrying  map[TargetKey]pendingDebounce
	nextTimer uint64
	stopCh    chan struct{}
	stopOnce  sync.Once
}

type pendingDebounce struct {
	timer      clock.Timer
	generation uint64
}

func New(config Config) (*TargetQueue, error) {
	if config.Debounce < 0 {
		return nil, errors.New("debounce cannot be negative")
	}
	if config.DisableDebounce && config.Debounce != 0 {
		return nil, errors.New("disable debounce conflicts with a nonzero debounce")
	}
	if config.Debounce == 0 && !config.DisableDebounce {
		config.Debounce = DefaultDebounce
	}
	if config.RetryBase == 0 {
		config.RetryBase = DefaultRetryBase
	}
	if config.RetryMax == 0 {
		config.RetryMax = DefaultRetryMax
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = DefaultMaxRetries
	}
	if config.Jitter == 0 {
		config.Jitter = DefaultJitter
	}
	if config.RetryBase < 0 || config.RetryMax < config.RetryBase {
		return nil, errors.New("retry delays must be positive and max must be at least base")
	}
	if config.MaxRetries < 0 {
		return nil, errors.New("max retries cannot be negative")
	}
	if config.Jitter < 0 || config.Jitter > 1 {
		return nil, errors.New("jitter must be between zero and one")
	}
	if config.Clock == nil {
		config.Clock = clock.RealClock{}
	}
	limiter := newJitteredRateLimiter(config.RetryBase, config.RetryMax, config.Jitter)
	queue := clientworkqueue.NewTypedRateLimitingQueueWithConfig[TargetKey](
		limiter,
		clientworkqueue.TypedRateLimitingQueueConfig[TargetKey]{Name: config.Name, Clock: config.Clock},
	)
	return &TargetQueue{
		queue: queue, limiter: limiter, debounce: config.Debounce, maxRetries: config.MaxRetries,
		clock: config.Clock, deleted: map[TargetKey]struct{}{}, pending: map[TargetKey]pendingDebounce{}, retrying: map[TargetKey]pendingDebounce{},
		stopCh: make(chan struct{}),
	}, nil
}

// Enqueue schedules one semantic event after the configured minimum debounce.
func (q *TargetQueue) Enqueue(key TargetKey) bool {
	if q == nil || !key.valid() || q.queue.ShuttingDown() {
		return false
	}
	q.mu.RLock()
	if _, deleted := q.deleted[key]; deleted {
		q.mu.RUnlock()
		return false
	}
	if q.debounce == 0 {
		q.queue.Add(key)
		q.mu.RUnlock()
		return true
	}
	if _, pending := q.pending[key]; pending {
		q.mu.RUnlock()
		return true
	}
	q.mu.RUnlock()

	q.mu.Lock()
	if _, deleted := q.deleted[key]; deleted {
		q.mu.Unlock()
		return false
	}
	if _, pending := q.pending[key]; pending {
		q.mu.Unlock()
		return true
	}
	q.stopRetryLocked(key)
	q.nextTimer++
	generation := q.nextTimer
	timer := q.clock.NewTimer(q.debounce)
	q.pending[key] = pendingDebounce{timer: timer, generation: generation}
	q.mu.Unlock()
	go q.waitForDebounce(key, timer, generation)
	return true
}

// EnqueuePeriodic bypasses event debounce for a periodic full audit.
func (q *TargetQueue) EnqueuePeriodic(key TargetKey) bool {
	if q == nil || !key.valid() || q.queue.ShuttingDown() {
		return false
	}
	q.mu.RLock()
	if _, deleted := q.deleted[key]; deleted {
		q.mu.RUnlock()
		return false
	}
	q.mu.RUnlock()
	q.mu.Lock()
	if _, deleted := q.deleted[key]; deleted {
		q.mu.Unlock()
		return false
	}
	if pending, ok := q.pending[key]; ok {
		pending.timer.Stop()
		delete(q.pending, key)
	}
	q.stopRetryLocked(key)
	q.queue.Add(key)
	q.mu.Unlock()
	return true
}

// ActivateTarget permits future events for a newly added/recreated target.
func (q *TargetQueue) ActivateTarget(key TargetKey) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.deleted, key)
}

// ForgetTarget drops retry bookkeeping and suppresses delayed/tombstoned work.
// A later target Add event must explicitly ActivateTarget before enqueueing.
func (q *TargetQueue) ForgetTarget(key TargetKey) {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.deleted[key] = struct{}{}
	if pending, ok := q.pending[key]; ok {
		pending.timer.Stop()
		delete(q.pending, key)
	}
	q.stopRetryLocked(key)
	q.mu.Unlock()
	q.queue.Forget(key)
}

// Get returns the next non-deleted target. The underlying typed queue blocks.
func (q *TargetQueue) Get() (TargetKey, bool) {
	if q == nil {
		return TargetKey{}, true
	}
	for {
		key, shutdown := q.queue.Get()
		if shutdown {
			return TargetKey{}, true
		}
		if !q.isDeleted(key) {
			return key, false
		}
		q.queue.Forget(key)
		q.queue.Done(key)
	}
}

// Complete records success or schedules a bounded retry for a processed key.
func (q *TargetQueue) Complete(key TargetKey, reconcileErr error) Completion {
	if q == nil {
		return CompletionForgotten
	}
	q.mu.RLock()
	if _, deleted := q.deleted[key]; deleted {
		q.mu.RUnlock()
		q.queue.Forget(key)
		q.queue.Done(key)
		return CompletionForgotten
	}
	q.mu.RUnlock()
	if reconcileErr == nil {
		q.queue.Forget(key)
		q.queue.Done(key)
		return CompletionSucceeded
	}
	if q.queue.NumRequeues(key) >= q.maxRetries {
		q.queue.Forget(key)
		q.queue.Done(key)
		return CompletionExhausted
	}
	delay := q.limiter.When(key)
	q.queue.Done(key)
	if !q.scheduleRetry(key, delay) {
		q.queue.Forget(key)
		return CompletionForgotten
	}
	return CompletionRetried
}

func (q *TargetQueue) NumRequeues(key TargetKey) int {
	if q == nil {
		return 0
	}
	return q.queue.NumRequeues(key)
}

// IsTargetForgotten reports whether a target deletion tombstone currently
// suppresses event, retry, and periodic work.
func (q *TargetQueue) IsTargetForgotten(key TargetKey) bool {
	if q == nil {
		return true
	}
	return q.isDeleted(key)
}

func (q *TargetQueue) Len() int {
	if q == nil {
		return 0
	}
	return q.queue.Len()
}

func (q *TargetQueue) ShutDown() {
	if q == nil {
		return
	}
	q.stopOnce.Do(func() {
		q.mu.Lock()
		for key, pending := range q.pending {
			pending.timer.Stop()
			delete(q.pending, key)
		}
		for key, pending := range q.retrying {
			pending.timer.Stop()
			delete(q.retrying, key)
		}
		close(q.stopCh)
		q.mu.Unlock()
		q.queue.ShutDown()
	})
}

func (q *TargetQueue) isDeleted(key TargetKey) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	_, deleted := q.deleted[key]
	return deleted
}

func (q *TargetQueue) waitForDebounce(key TargetKey, timer clock.Timer, generation uint64) {
	select {
	case <-q.stopCh:
		timer.Stop()
	case <-timer.C():
		q.mu.Lock()
		pending, ok := q.pending[key]
		if !ok || pending.generation != generation {
			q.mu.Unlock()
			return
		}
		delete(q.pending, key)
		if _, deleted := q.deleted[key]; !deleted && !q.queue.ShuttingDown() {
			q.queue.Add(key)
		}
		q.mu.Unlock()
	}
}

func (q *TargetQueue) scheduleRetry(key TargetKey, delay time.Duration) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, deleted := q.deleted[key]; deleted || q.queue.ShuttingDown() {
		return false
	}
	// A semantic event that arrived while this key was processing already owns
	// the single pending slot; retain the retry count but do not create a second
	// delayed delivery.
	if _, pending := q.pending[key]; pending {
		return true
	}
	q.stopRetryLocked(key)
	if delay <= 0 {
		q.queue.Add(key)
		return true
	}
	q.nextTimer++
	generation := q.nextTimer
	timer := q.clock.NewTimer(delay)
	q.retrying[key] = pendingDebounce{timer: timer, generation: generation}
	go q.waitForRetry(key, timer, generation)
	return true
}

func (q *TargetQueue) waitForRetry(key TargetKey, timer clock.Timer, generation uint64) {
	select {
	case <-q.stopCh:
		timer.Stop()
	case <-timer.C():
		q.mu.Lock()
		pending, ok := q.retrying[key]
		if !ok || pending.generation != generation {
			q.mu.Unlock()
			return
		}
		delete(q.retrying, key)
		if _, deleted := q.deleted[key]; !deleted && !q.queue.ShuttingDown() {
			q.queue.Add(key)
		}
		q.mu.Unlock()
	}
}

func (q *TargetQueue) stopRetryLocked(key TargetKey) {
	if pending, ok := q.retrying[key]; ok {
		pending.timer.Stop()
		delete(q.retrying, key)
	}
}

type jitteredRateLimiter struct {
	inner    clientworkqueue.TypedRateLimiter[TargetKey]
	maxDelay time.Duration
	jitter   float64
}

func newJitteredRateLimiter(baseDelay, maxDelay time.Duration, jitter float64) clientworkqueue.TypedRateLimiter[TargetKey] {
	return &jitteredRateLimiter{
		inner:    clientworkqueue.NewTypedItemExponentialFailureRateLimiter[TargetKey](baseDelay, maxDelay),
		maxDelay: maxDelay,
		jitter:   jitter,
	}
}

func (r *jitteredRateLimiter) When(item TargetKey) time.Duration {
	delay := r.inner.When(item)
	attempt := r.inner.NumRequeues(item)
	if r.jitter == 0 || delay == 0 {
		return delay
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(item.String()))
	_, _ = hash.Write([]byte(fmt.Sprintf("/%d", attempt)))
	unit := float64(hash.Sum64()) / float64(math.MaxUint64)
	factor := (1 - r.jitter) + (2 * r.jitter * unit)
	jittered := time.Duration(float64(delay) * factor)
	if jittered < 0 {
		return 0
	}
	if jittered > r.maxDelay {
		return r.maxDelay
	}
	return jittered
}

func (r *jitteredRateLimiter) Forget(item TargetKey) {
	r.inner.Forget(item)
}

func (r *jitteredRateLimiter) NumRequeues(item TargetKey) int {
	return r.inner.NumRequeues(item)
}
