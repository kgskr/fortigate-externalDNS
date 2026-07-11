package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	"k8s.io/utils/clock"
)

var ErrPlatformCacheSync = errors.New("platform informer caches did not synchronize")

// TargetAudit is the immutable handoff between discovery/provider reads and
// the external mutation boundary.
type TargetAudit struct {
	CleanupCapable         bool
	DiscoveryComplete      bool
	ProviderSnapshotStable bool
	State                  any
}

// TargetExecutor keeps provider-specific discovery and mutation outside the
// informer/workqueue runtime. Apply must honor context cancellation between
// provider operations.
type TargetExecutor interface {
	Audit(context.Context, platformqueue.TargetKey) (TargetAudit, error)
	Apply(context.Context, platformqueue.TargetKey, TargetAudit) error
}

type TargetDeletionExecutor interface {
	TargetDeleted(context.Context, platformqueue.TargetKey) error
}

type PlatformRuntimeConfig struct {
	Namespace        string
	InformerResync   time.Duration
	PeriodicInterval time.Duration
	Workers          int
	Queue            platformqueue.Config
	Clock            clock.WithTicker
}

type PlatformRuntime struct {
	config    PlatformRuntimeConfig
	factories *informerFactories
	mapper    *informerTargetMapper
	gate      *platformqueue.CacheSyncGate
	queue     *platformqueue.TargetQueue
	executor  TargetExecutor
	errors    chan error
	running   atomic.Bool
}

func NewPlatformRuntime(clients PlatformClients, config PlatformRuntimeConfig, executor TargetExecutor) (*PlatformRuntime, error) {
	if executor == nil {
		return nil, errors.New("target executor is required")
	}
	if config.Workers <= 0 {
		config.Workers = 1
	}
	if config.PeriodicInterval <= 0 {
		return nil, errors.New("periodic full-audit interval must be positive")
	}
	if config.Clock == nil {
		if config.Queue.Clock != nil {
			config.Clock = config.Queue.Clock
		} else {
			config.Clock = clock.RealClock{}
		}
	}
	if config.Queue.Clock == nil {
		config.Queue.Clock = config.Clock
	}
	queue, err := platformqueue.New(config.Queue)
	if err != nil {
		return nil, err
	}
	factories, err := newInformerFactories(clients, config.Namespace, config.InformerResync)
	if err != nil {
		queue.ShutDown()
		return nil, err
	}
	mapper := &informerTargetMapper{targets: factories.targets}
	gate, err := factories.SyncGate()
	if err != nil {
		queue.ShutDown()
		return nil, err
	}
	runtime := &PlatformRuntime{
		config: config, factories: factories, mapper: mapper, gate: gate,
		queue: queue, executor: executor, errors: make(chan error, 64),
	}
	dispatcher := platformqueue.Dispatcher{Queue: queue, Mapper: mapper}
	if deletionExecutor, ok := executor.(TargetDeletionExecutor); ok {
		dispatcher.OnTargetDeleted = deletionExecutor.TargetDeleted
	}
	if err := factories.RegisterHandlers(dispatcher, runtime.report); err != nil {
		queue.ShutDown()
		return nil, err
	}
	return runtime, nil
}

// Errors exposes bounded asynchronous informer-adapter failures. Consumers
// should drain it for logging; periodic full audits remain the missed-event
// fallback.
func (r *PlatformRuntime) Errors() <-chan error {
	if r == nil {
		return nil
	}
	return r.errors
}

func (r *PlatformRuntime) Run(ctx context.Context) error {
	if r == nil {
		return errors.New("platform runtime is required")
	}
	if !r.running.CompareAndSwap(false, true) {
		return errors.New("platform runtime can only be run once")
	}
	runCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		r.queue.ShutDown()
		workers.Wait()
		r.factories.Shutdown()
	}()

	r.factories.Start(runCtx.Done())
	if !r.factories.WaitForSync(runCtx) {
		if runCtx.Err() != nil {
			return nil
		}
		return ErrPlatformCacheSync
	}
	if err := r.gate.RequireSynchronized(); err != nil {
		return fmt.Errorf("verify platform cache gate: %w", err)
	}

	periodic := platformqueue.PeriodicEnqueuer{
		Queue: r.queue, Lister: r.mapper, Interval: r.config.PeriodicInterval, Clock: r.config.Clock,
	}
	if err := periodic.EnqueueAll(runCtx); err != nil {
		return fmt.Errorf("enqueue initial target audits: %w", err)
	}
	for worker := 0; worker < r.config.Workers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r.runWorker(runCtx)
		}()
	}
	fatal := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		if err := periodic.Run(runCtx); err != nil {
			select {
			case fatal <- err:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-fatal:
		cancel()
		return fmt.Errorf("periodic target audit: %w", err)
	}
}

func (r *PlatformRuntime) runWorker(ctx context.Context) {
	for {
		key, shutdown := r.queue.Get()
		if shutdown {
			return
		}
		if !r.mapper.HasTarget(key) {
			r.queue.ForgetTarget(key)
			r.queue.Complete(key, nil)
			continue
		}
		err := r.processTarget(ctx, key)
		r.queue.Complete(key, err)
	}
}

func (r *PlatformRuntime) processTarget(ctx context.Context, key platformqueue.TargetKey) error {
	if err := r.gate.RequireSynchronized(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	audit, err := r.executor.Audit(ctx, key)
	if err != nil {
		return err
	}
	if audit.CleanupCapable {
		evidence := platformqueue.AuditEvidence{
			Caches: r.gate, DiscoveryComplete: audit.DiscoveryComplete,
			ProviderSnapshotStable: audit.ProviderSnapshotStable,
		}
		if err := evidence.RequireCleanupSafety(); err != nil {
			return err
		}
	}
	// Leadership loss or shutdown between audit and apply must prevent a new
	// provider mutation from starting.
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.executor.Apply(ctx, key, audit)
}

func (r *PlatformRuntime) report(err error) {
	if r == nil || err == nil {
		return
	}
	select {
	case r.errors <- err:
	default:
	}
}
