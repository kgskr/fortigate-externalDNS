package controller

import (
	"sync"
	"time"
)

// Heartbeat tracks reconcile-loop progress for the liveness probe. It reports
// unhealthy only when this replica is responsible for reconciling (it holds
// leadership, or leader election is disabled) and no reconcile attempt has
// completed within the staleness window. Attempts count whether they succeed
// or fail: a reachable-but-erroring FortiGate is not a reason to restart the
// pod, while a wedged loop (no attempts completing at all) is.
type Heartbeat struct {
	mu          sync.Mutex
	active      bool
	activeSince time.Time
	lastAttempt time.Time
	now         func() time.Time
}

// NewHeartbeat returns a heartbeat that is not yet active.
func NewHeartbeat() *Heartbeat {
	return &Heartbeat{now: time.Now}
}

// SetActive marks whether this replica is currently responsible for
// reconciling. Activation records its own timestamp so a freshly elected
// leader is healthy until its first attempt has had the window to complete.
func (h *Heartbeat) SetActive(active bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if active && !h.active {
		h.activeSince = h.now()
	}
	h.active = active
}

// MarkAttempt records the completion of one reconcile attempt (successful or
// not).
func (h *Heartbeat) MarkAttempt() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastAttempt = h.now()
}

// Healthy reports whether the liveness probe should pass for the given
// staleness window. Inactive replicas (non-leaders) are always healthy.
func (h *Heartbeat) Healthy(window time.Duration) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active {
		return true
	}
	reference := h.lastAttempt
	if h.activeSince.After(reference) {
		// No attempt has completed during this activation yet (or the last one
		// belongs to a previous leadership stint); measure from activation so a
		// new leader gets the full window before the probe can fail it.
		reference = h.activeSince
	}
	return h.now().Sub(reference) <= window
}
