package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
)

type ProviderClient interface {
	ListRecords(context.Context) ([]dns.Endpoint, error)
	Apply(context.Context, []plan.Operation, bool) error
}

type ClientFactory interface {
	NewClient(context.Context, Definition, *CredentialMaterial) (ProviderClient, error)
}

// StoreHandles are target-scoped adapters. Their concrete implementations are
// supplied by the future 9.1 wiring so this package does not own global clients.
type StoreHandles struct {
	PlanStore      any
	OwnershipStore any
	StatusStore    any
	Close          func() error
}

type ResourceFactory interface {
	NewResources(context.Context, Definition) (StoreHandles, error)
}

type FailureReason string

const (
	FailureCredentials FailureReason = "credentials-unavailable"
	FailureClient      FailureReason = "client-unavailable"
	FailureResources   FailureReason = "resources-unavailable"
	FailureProvider    FailureReason = "provider-failed"
	FailurePolicy      FailureReason = "policy-failed"
	FailureOwnership   FailureReason = "ownership-failed"
	FailureApproval    FailureReason = "approval-failed"
	FailureRetry       FailureReason = "retry-exhausted"
	FailureCancelled   FailureReason = "cancelled"
)

type RuntimeError struct {
	Reason FailureReason
}

func (e *RuntimeError) Error() string {
	return "target runtime failed: " + string(e.Reason)
}

func Fail(reason FailureReason) error {
	return &RuntimeError{Reason: boundedFailureReason(reason)}
}

type QueueState struct {
	mu       sync.RWMutex
	depth    int
	inFlight bool
}

func (q *QueueState) SetDepth(depth int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if depth < 0 {
		depth = 0
	}
	q.depth = depth
}

func (q *QueueState) begin() {
	q.mu.Lock()
	q.inFlight = true
	q.mu.Unlock()
}

func (q *QueueState) finish() {
	q.mu.Lock()
	q.inFlight = false
	q.mu.Unlock()
}

func (q *QueueState) Snapshot() (depth int, inFlight bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.depth, q.inFlight
}

type RetrySnapshot struct {
	Attempts    int
	CircuitOpen bool
	LastReason  FailureReason
}

type RetryState struct {
	mu          sync.RWMutex
	attempts    int
	circuitOpen bool
	lastReason  FailureReason
}

func (r *RetryState) recordFailure(reason FailureReason, maximum int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	r.lastReason = boundedFailureReason(reason)
	if maximum >= 0 && r.attempts > maximum {
		r.circuitOpen = true
	}
}

func (r *RetryState) reset() {
	r.mu.Lock()
	r.attempts = 0
	r.circuitOpen = false
	r.lastReason = ""
	r.mu.Unlock()
}

func (r *RetryState) Snapshot() RetrySnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return RetrySnapshot{Attempts: r.attempts, CircuitOpen: r.circuitOpen, LastReason: r.lastReason}
}

type MetricsContext struct {
	Target string
	Global *metrics.Metrics
}

type Runtime struct {
	Definition Definition
	Stores     StoreHandles
	Queue      *QueueState
	Retry      *RetryState
	Metrics    MetricsContext

	client                ProviderClient
	credentialFingerprint string
	definitionFingerprint string
	ctx                   context.Context
	cancel                context.CancelFunc
	runMu                 sync.Mutex
}

func (r *Runtime) CredentialFingerprint() string {
	if r == nil {
		return ""
	}
	return r.credentialFingerprint
}

func (r *Runtime) ProviderClient() ProviderClient {
	if r == nil {
		return nil
	}
	return r.client
}

func (r *Runtime) String() string {
	if r == nil {
		return "<nil-target-runtime>"
	}
	return "target-runtime(" + r.Definition.Key() + ")"
}

func (r *Runtime) GoString() string { return r.String() }

func (r *Runtime) stop(closeStores bool) {
	if r == nil {
		return
	}
	r.cancel()
	if closer, ok := r.client.(io.Closer); ok {
		_ = closer.Close()
	}
	if closeStores && r.Stores.Close != nil {
		_ = r.Stores.Close()
	}
}

type EnqueueFunc func(targetKey string)

type RuntimeManager struct {
	resolver  *Resolver
	clients   ClientFactory
	resources ResourceFactory
	metrics   *metrics.Metrics
	enqueue   EnqueueFunc

	syncMu      sync.Mutex
	mu          sync.RWMutex
	runtimes    map[string]*Runtime
	definitions map[string]Definition
	references  map[string][]string
}

func NewRuntimeManager(resolver *Resolver, clients ClientFactory, resources ResourceFactory, metricRecorder *metrics.Metrics, enqueue EnqueueFunc) (*RuntimeManager, error) {
	if clients == nil {
		return nil, errors.New("target client factory is required")
	}
	if resources == nil {
		return nil, errors.New("target resource factory is required")
	}
	return &RuntimeManager{
		resolver:    resolver,
		clients:     clients,
		resources:   resources,
		metrics:     metricRecorder,
		enqueue:     enqueue,
		runtimes:    map[string]*Runtime{},
		definitions: map[string]Definition{},
		references:  map[string][]string{},
	}, nil
}

type SyncResult struct {
	Ready    []string
	Removed  []string
	Failures map[string]FailureReason
}

// Sync independently resolves and constructs each target. A credential,
// client, or resource failure removes only that target's runnable instance and
// never prevents healthy siblings from becoming ready.
func (m *RuntimeManager) Sync(ctx context.Context, definitions []Definition) (SyncResult, error) {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	definitions = cloneDefinitions(definitions)
	if err := ValidateAll(definitions); err != nil {
		return SyncResult{}, err
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Key() < definitions[j].Key() })

	m.mu.RLock()
	old := make(map[string]*Runtime, len(m.runtimes))
	for key, runtime := range m.runtimes {
		old[key] = runtime
	}
	m.mu.RUnlock()

	result := SyncResult{Failures: map[string]FailureReason{}}
	nextRuntimes := make(map[string]*Runtime, len(definitions))
	nextDefinitions := make(map[string]Definition, len(definitions))
	nextReferences := map[string][]string{}
	changed := make([]string, 0, len(definitions))
	resourcesTransferred := map[string]bool{}

	for _, definition := range definitions {
		key := definition.Key()
		nextDefinitions[key] = sanitizedDefinition(definition)
		for _, reference := range credentialReferences(definition) {
			nextReferences[reference] = append(nextReferences[reference], key)
		}

		material, err := m.resolveCredentials(ctx, definition)
		if err != nil {
			result.Failures[key] = FailureCredentials
			m.metrics.SetTargetReadiness(key, false)
			continue
		}
		credentialFingerprint := material.Fingerprint()
		definitionFingerprint := fingerprintDefinition(definition)
		if existing := old[key]; existing != nil && existing.credentialFingerprint == credentialFingerprint && existing.definitionFingerprint == definitionFingerprint {
			material.Clear()
			nextRuntimes[key] = existing
			result.Ready = append(result.Ready, key)
			resourcesTransferred[key] = true
			continue
		}

		safeDefinition := sanitizedDefinition(definition)
		client, clientErr := m.clients.NewClient(ctx, safeDefinition, material)
		material.Clear()
		if clientErr != nil || client == nil {
			result.Failures[key] = FailureClient
			m.metrics.SetTargetReadiness(key, false)
			continue
		}

		handles := StoreHandles{}
		if existing := old[key]; existing != nil {
			handles = existing.Stores
			resourcesTransferred[key] = true
		} else {
			handles, err = m.resources.NewResources(ctx, safeDefinition)
			if err != nil {
				if closer, ok := client.(io.Closer); ok {
					_ = closer.Close()
				}
				result.Failures[key] = FailureResources
				m.metrics.SetTargetReadiness(key, false)
				continue
			}
		}

		runtimeCtx, cancel := context.WithCancel(context.Background())
		runtime := &Runtime{
			Definition:            safeDefinition,
			client:                client,
			Stores:                handles,
			Queue:                 &QueueState{},
			Retry:                 &RetryState{},
			Metrics:               MetricsContext{Target: key, Global: m.metrics},
			credentialFingerprint: credentialFingerprint,
			definitionFingerprint: definitionFingerprint,
			ctx:                   runtimeCtx,
			cancel:                cancel,
		}
		nextRuntimes[key] = runtime
		result.Ready = append(result.Ready, key)
		m.metrics.SetTargetReadiness(key, true)
		changed = append(changed, key)
	}

	for reference := range nextReferences {
		nextReferences[reference] = uniqueSorted(nextReferences[reference])
	}
	for key, runtime := range old {
		next := nextRuntimes[key]
		if next == runtime {
			continue
		}
		closeStores := !resourcesTransferred[key]
		runtime.stop(closeStores)
		if _, configured := nextDefinitions[key]; !configured {
			result.Removed = append(result.Removed, key)
			if m.metrics != nil {
				m.metrics.RemoveTarget(key)
			}
		}
	}

	sort.Strings(result.Ready)
	sort.Strings(result.Removed)
	m.mu.Lock()
	m.runtimes = nextRuntimes
	m.definitions = nextDefinitions
	m.references = nextReferences
	m.mu.Unlock()
	for _, key := range changed {
		if m.enqueue != nil {
			m.enqueue(key)
		}
	}
	return result, nil
}

func (m *RuntimeManager) resolveCredentials(ctx context.Context, definition Definition) (*CredentialMaterial, error) {
	if definition.Legacy {
		return (&Resolver{}).Resolve(ctx, definition)
	}
	if m.resolver == nil {
		return nil, &CredentialError{Reason: CredentialSecretUnavailable}
	}
	return m.resolver.Resolve(ctx, definition)
}

func (m *RuntimeManager) Runtime(key string) (*Runtime, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	runtime, exists := m.runtimes[key]
	return runtime, exists
}

func (m *RuntimeManager) Definitions() []Definition {
	m.mu.RLock()
	defer m.mu.RUnlock()
	definitions := make([]Definition, 0, len(m.definitions))
	for _, definition := range m.definitions {
		definitions = append(definitions, cloneDefinition(definition))
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Key() < definitions[j].Key() })
	return definitions
}

// CredentialEvent returns and enqueues only targets referencing the changed
// namespaced object. Informer registration remains in task 9.1.
func (m *RuntimeManager) CredentialEvent(kind, namespace, name string) []string {
	m.mu.RLock()
	targets := append([]string(nil), m.references[credentialReferenceKey(kind, namespace, name)]...)
	m.mu.RUnlock()
	for _, key := range targets {
		if m.enqueue != nil {
			m.enqueue(key)
		}
	}
	return targets
}

type RunResult struct {
	Succeeded bool
	Reason    FailureReason
}

type Worker func(context.Context, *Runtime) error

// RunAll executes each target in an independent goroutine. Errors are reduced
// to fixed reasons; raw provider, policy, ownership, or approval errors are not
// returned or persisted by this layer.
func (m *RuntimeManager) RunAll(ctx context.Context, worker Worker) map[string]RunResult {
	if worker == nil {
		return map[string]RunResult{}
	}
	m.mu.RLock()
	keys := make([]string, 0, len(m.runtimes))
	runtimes := make(map[string]*Runtime, len(m.runtimes))
	for key, runtime := range m.runtimes {
		keys = append(keys, key)
		runtimes[key] = runtime
	}
	m.mu.RUnlock()
	sort.Strings(keys)

	results := make(map[string]RunResult, len(keys))
	var resultMu sync.Mutex
	var wait sync.WaitGroup
	for _, key := range keys {
		key := key
		runtime := runtimes[key]
		wait.Add(1)
		go func() {
			defer wait.Done()
			result := runtime.run(ctx, worker)
			resultMu.Lock()
			results[key] = result
			resultMu.Unlock()
		}()
	}
	wait.Wait()
	return results
}

func (r *Runtime) run(parent context.Context, worker Worker) RunResult {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	r.Queue.begin()
	defer r.Queue.finish()

	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(r.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	if err := ctx.Err(); err != nil {
		r.Retry.recordFailure(FailureCancelled, r.Definition.Retries)
		return RunResult{Reason: FailureCancelled}
	}
	err := worker(ctx, r)
	if err == nil {
		r.Retry.reset()
		if r.Metrics.Global != nil {
			depth, _ := r.Queue.Snapshot()
			r.Metrics.Global.SetQueueState(r.Metrics.Target, depth, 0)
		}
		return RunResult{Succeeded: true}
	}
	reason := failureReason(err)
	r.Retry.recordFailure(reason, r.Definition.Retries)
	if r.Metrics.Global != nil {
		depth, _ := r.Queue.Snapshot()
		r.Metrics.Global.SetQueueState(r.Metrics.Target, depth, r.Retry.Snapshot().Attempts)
	}
	return RunResult{Reason: reason}
}

func (d Definition) Key() string {
	if strings.TrimSpace(d.Namespace) == "" {
		return strings.TrimSpace(d.Name)
	}
	return strings.TrimSpace(d.Namespace) + "/" + strings.TrimSpace(d.Name)
}

func (d Definition) String() string   { return "target-definition(" + d.Key() + ")" }
func (d Definition) GoString() string { return d.String() }

func sanitizedDefinition(definition Definition) Definition {
	result := cloneDefinition(definition)
	result.APIToken = ""
	return result
}

func cloneDefinitions(definitions []Definition) []Definition {
	result := make([]Definition, len(definitions))
	for index := range definitions {
		result[index] = cloneDefinition(definitions[index])
	}
	return result
}

func cloneDefinition(definition Definition) Definition {
	definition.Sources = copyStrings(definition.Sources)
	definition.Namespaces = copyStrings(definition.Namespaces)
	definition.GatewayTargetNamespaces = copyStrings(definition.GatewayTargetNamespaces)
	definition.DomainFilters = copyStrings(definition.DomainFilters)
	if definition.APITokenSecretRef != nil {
		copy := *definition.APITokenSecretRef
		if definition.APITokenSecretRef.Optional != nil {
			optional := *definition.APITokenSecretRef.Optional
			copy.Optional = &optional
		}
		definition.APITokenSecretRef = &copy
	}
	if definition.CARef != nil {
		copy := *definition.CARef
		definition.CARef = &copy
	}
	return definition
}

func fingerprintDefinition(definition Definition) string {
	definition = sanitizedDefinition(definition)
	definition.ResourceVersion = ""
	encoded, err := json.Marshal(definition)
	if err != nil {
		panic(fmt.Sprintf("target definition cannot be fingerprinted: %v", err))
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func failureReason(err error) FailureReason {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return FailureCancelled
	}
	var runtimeErr *RuntimeError
	if errors.As(err, &runtimeErr) {
		return boundedFailureReason(runtimeErr.Reason)
	}
	return FailureProvider
}

func boundedFailureReason(reason FailureReason) FailureReason {
	switch reason {
	case FailureCredentials, FailureClient, FailureResources, FailureProvider, FailurePolicy, FailureOwnership, FailureApproval, FailureRetry, FailureCancelled:
		return reason
	default:
		return FailureProvider
	}
}
