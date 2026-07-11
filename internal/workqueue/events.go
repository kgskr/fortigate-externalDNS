package workqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// EventKind is a fixed informer category. Secret adapters must fingerprint
// referenced metadata only and must never include Secret data values.
type EventKind string

const (
	EventService       EventKind = "Service"
	EventIngress       EventKind = "Ingress"
	EventGateway       EventKind = "Gateway"
	EventHTTPRoute     EventKind = "HTTPRoute"
	EventEndpointSlice EventKind = "EndpointSlice"
	EventTarget        EventKind = "FortiGateDNSTarget"
	EventPolicy        EventKind = "FortiGateDNSPolicy"
	EventOwnership     EventKind = "FortiGateDNSRecordOwnership"
	EventChangePlan    EventKind = "FortiGateDNSChangePlan"
	EventSecret        EventKind = "SecretMetadata"
)

var eventKinds = map[EventKind]struct{}{
	EventService: {}, EventIngress: {}, EventGateway: {}, EventHTTPRoute: {},
	EventEndpointSlice: {}, EventTarget: {}, EventPolicy: {}, EventOwnership: {},
	EventChangePlan: {}, EventSecret: {},
}

// EventAction is a fixed informer transition.
type EventAction string

const (
	EventAdd    EventAction = "Add"
	EventUpdate EventAction = "Update"
	EventDelete EventAction = "Delete"
)

// SemanticFingerprint is a deterministic digest of only fields that affect
// discovery, policy, ownership, approval, credentials, or target routing.
type SemanticFingerprint string

// SemanticFields is populated by informer-specific adapters. Status,
// resourceVersion noise, Secret values, and provider response data must be
// excluded by those adapters.
type SemanticFields map[string]string

// NewSemanticFingerprint canonicalizes named relevant fields and binds them
// to the fixed event kind. Named fields avoid ordering-dependent updates.
func NewSemanticFingerprint(kind EventKind, fields SemanticFields) (SemanticFingerprint, error) {
	if !validEventKind(kind) {
		return "", fmt.Errorf("unsupported event kind %q", kind)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	writeFingerprintPart(hash, string(kind))
	for _, key := range keys {
		writeFingerprintPart(hash, key)
		writeFingerprintPart(hash, fields[key])
	}
	return SemanticFingerprint(hex.EncodeToString(hash.Sum(nil))), nil
}

// Event is the semantic boundary between informer adapters and target mapping.
type Event struct {
	Kind      EventKind
	Action    EventAction
	Namespace string
	Name      string
	// RelatedTarget carries a targetRef extracted by a fixed CRD adapter. It is
	// never used as a metric label or persisted diagnostic.
	RelatedTarget  string
	OldFingerprint SemanticFingerprint
	NewFingerprint SemanticFingerprint
}

func (e Event) validate() error {
	if !validEventKind(e.Kind) {
		return fmt.Errorf("unsupported event kind %q", e.Kind)
	}
	switch e.Action {
	case EventAdd, EventDelete:
		return nil
	case EventUpdate:
		if e.OldFingerprint == "" || e.NewFingerprint == "" {
			return errors.New("update event requires old and new semantic fingerprints")
		}
		return nil
	default:
		return fmt.Errorf("unsupported event action %q", e.Action)
	}
}

// Meaningful reports whether an event can change reconciliation inputs.
func (e Event) Meaningful() bool {
	if e.Action != EventUpdate {
		return true
	}
	return e.OldFingerprint != e.NewFingerprint
}

// EventMapper maps a semantic event to every affected target. Implementations
// are supplied by the future informer wiring layer.
type EventMapper interface {
	TargetsForEvent(context.Context, Event) ([]TargetKey, error)
}

// DispatchResult is bounded bookkeeping for one event.
type DispatchResult struct {
	Enqueued  int
	Forgotten int
	Ignored   bool
}

// Dispatcher filters semantic update noise and enqueues every unique mapped
// target. Target deletion forgets pending retry/debounce state instead.
type Dispatcher struct {
	Queue           *TargetQueue
	Mapper          EventMapper
	OnTargetDeleted func(context.Context, TargetKey) error
}

func (d Dispatcher) Handle(ctx context.Context, event Event) (DispatchResult, error) {
	if d.Queue == nil || d.Mapper == nil {
		return DispatchResult{}, errors.New("event dispatcher requires queue and mapper")
	}
	if err := event.validate(); err != nil {
		return DispatchResult{}, err
	}
	if !event.Meaningful() {
		return DispatchResult{Ignored: true}, nil
	}
	keys, err := d.Mapper.TargetsForEvent(ctx, event)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("map event to targets: %w", err)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	result := DispatchResult{}
	seen := make(map[TargetKey]struct{}, len(keys))
	for _, key := range keys {
		if !key.valid() {
			return result, fmt.Errorf("mapper returned invalid target key %q", key.String())
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if event.Kind == EventTarget && event.Action == EventDelete {
			if d.OnTargetDeleted != nil {
				if err := d.OnTargetDeleted(ctx, key); err != nil {
					return result, fmt.Errorf("handle deleted target: %w", err)
				}
			}
			d.Queue.ForgetTarget(key)
			result.Forgotten++
			continue
		}
		if event.Kind == EventTarget && event.Action == EventAdd {
			d.Queue.ActivateTarget(key)
		}
		if d.Queue.Enqueue(key) {
			result.Enqueued++
		}
	}
	return result, nil
}

func validEventKind(kind EventKind) bool {
	_, ok := eventKinds[kind]
	return ok
}

type fingerprintWriter interface {
	Write([]byte) (int, error)
}

func writeFingerprintPart(w fingerprintWriter, value string) {
	_, _ = w.Write([]byte(strconv.Itoa(len(value))))
	_, _ = w.Write([]byte{':'})
	_, _ = w.Write([]byte(value))
	_, _ = w.Write([]byte{0})
}
