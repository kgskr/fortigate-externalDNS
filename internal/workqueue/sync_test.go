package workqueue

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCacheSyncAndCleanupSafetyGate(t *testing.T) {
	var serviceSynced atomic.Bool
	var endpointSliceSynced atomic.Bool
	endpointSliceSynced.Store(true)
	gate, err := NewCacheSyncGate(
		CacheRequirement{Kind: EventService, HasSynced: serviceSynced.Load},
		CacheRequirement{Kind: EventEndpointSlice, HasSynced: endpointSliceSynced.Load},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.RequireSynchronized(); !errors.Is(err, ErrCachesNotSynchronized) || !strings.Contains(err.Error(), string(EventService)) {
		t.Fatalf("cache sync error = %v", err)
	}
	if err := (AuditEvidence{Caches: gate, DiscoveryComplete: true, ProviderSnapshotStable: true}).RequireCleanupSafety(); !errors.Is(err, ErrCachesNotSynchronized) {
		t.Fatalf("unsynchronized cache authorized cleanup: %v", err)
	}

	serviceSynced.Store(true)
	if err := gate.RequireSynchronized(); err != nil {
		t.Fatal(err)
	}
	if err := (AuditEvidence{Caches: gate, ProviderSnapshotStable: true}).RequireCleanupSafety(); !errors.Is(err, ErrDiscoveryIncomplete) {
		t.Fatalf("incomplete discovery error = %v", err)
	}
	if err := (AuditEvidence{Caches: gate, DiscoveryComplete: true}).RequireCleanupSafety(); !errors.Is(err, ErrProviderSnapshotUnstable) {
		t.Fatalf("unstable provider error = %v", err)
	}
	if err := (AuditEvidence{Caches: gate, DiscoveryComplete: true, ProviderSnapshotStable: true}).RequireCleanupSafety(); err != nil {
		t.Fatalf("complete full audit rejected: %v", err)
	}
}

func TestCacheSyncGateRejectsUnsupportedAndDuplicateKinds(t *testing.T) {
	if _, err := NewCacheSyncGate(CacheRequirement{Kind: EventKind("Pod"), HasSynced: func() bool { return true }}); err == nil {
		t.Fatal("unsupported cache kind accepted")
	}
	if _, err := NewCacheSyncGate(
		CacheRequirement{Kind: EventPolicy, HasSynced: func() bool { return true }},
		CacheRequirement{Kind: EventPolicy, HasSynced: func() bool { return true }},
	); err == nil {
		t.Fatal("duplicate cache kind accepted")
	}
}
