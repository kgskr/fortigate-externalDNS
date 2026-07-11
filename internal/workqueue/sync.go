package workqueue

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

var (
	ErrCachesNotSynchronized    = errors.New("required informer caches are not synchronized")
	ErrDiscoveryIncomplete      = errors.New("cached discovery is incomplete")
	ErrProviderSnapshotUnstable = errors.New("provider snapshot is not stable and complete")
)

// CacheRequirement describes one enabled informer cache needed by a target.
type CacheRequirement struct {
	Kind      EventKind
	HasSynced func() bool
}

// CacheSyncGate requires every configured cache to report synchronized before
// a worker may proceed to mutation planning.
type CacheSyncGate struct {
	requirements []CacheRequirement
	mu           sync.RWMutex
}

func NewCacheSyncGate(requirements ...CacheRequirement) (*CacheSyncGate, error) {
	seen := make(map[EventKind]struct{}, len(requirements))
	copyRequirements := make([]CacheRequirement, 0, len(requirements))
	for _, requirement := range requirements {
		if !validEventKind(requirement.Kind) {
			return nil, fmt.Errorf("unsupported cache kind %q", requirement.Kind)
		}
		if requirement.HasSynced == nil {
			return nil, fmt.Errorf("cache %s has no sync function", requirement.Kind)
		}
		if _, duplicate := seen[requirement.Kind]; duplicate {
			return nil, fmt.Errorf("duplicate cache kind %s", requirement.Kind)
		}
		seen[requirement.Kind] = struct{}{}
		copyRequirements = append(copyRequirements, requirement)
	}
	sort.Slice(copyRequirements, func(i, j int) bool { return copyRequirements[i].Kind < copyRequirements[j].Kind })
	return &CacheSyncGate{requirements: copyRequirements}, nil
}

// RequireSynchronized returns a bounded error listing fixed cache kinds only.
func (g *CacheSyncGate) RequireSynchronized() error {
	if g == nil {
		return ErrCachesNotSynchronized
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	missing := make([]string, 0)
	for _, requirement := range g.requirements {
		if !requirement.HasSynced() {
			missing = append(missing, string(requirement.Kind))
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("%w: %s", ErrCachesNotSynchronized, strings.Join(missing, ","))
	}
	return nil
}

// AuditEvidence is the cleanup safety boundary supplied by a full target
// reconciliation after the cache gate passes.
type AuditEvidence struct {
	Caches                 *CacheSyncGate
	DiscoveryComplete      bool
	ProviderSnapshotStable bool
}

// RequireCleanupSafety proves the full cached discovery and stable provider
// snapshot prerequisites. Event identity alone is intentionally absent.
func (e AuditEvidence) RequireCleanupSafety() error {
	if err := e.Caches.RequireSynchronized(); err != nil {
		return err
	}
	if !e.DiscoveryComplete {
		return ErrDiscoveryIncomplete
	}
	if !e.ProviderSnapshotStable {
		return ErrProviderSnapshotUnstable
	}
	return nil
}
