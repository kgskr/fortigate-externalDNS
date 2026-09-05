package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	maxMetricTargets = 256
	invalidTarget    = "invalid"
	overflowTarget   = "overflow"
	maxGaugeValue    = 1_000_000_000
)

// Source is a fixed source category suitable for a Prometheus label.
type Source string

const (
	SourceService       Source = "service"
	SourceIngress       Source = "ingress"
	SourceGateway       Source = "gateway"
	SourceHTTPRoute     Source = "httproute"
	SourceEndpointSlice Source = "endpointslice"
	SourcePolicy        Source = "policy"
	SourceOwnership     Source = "ownership"
	SourceTarget        Source = "target"
	SourceChangePlan    Source = "changeplan"
	SourceSecret        Source = "secret"
	SourceProvider      Source = "provider"
	SourceUnknown       Source = "unknown"
)

// ApplyOutcome is a fixed terminal/current apply outcome.
type ApplyOutcome string

const (
	ApplySucceeded       ApplyOutcome = "succeeded"
	ApplyFailed          ApplyOutcome = "failed"
	ApplyInterrupted     ApplyOutcome = "interrupted"
	ApplyStale           ApplyOutcome = "stale"
	ApplyPendingApproval ApplyOutcome = "pending-approval"
	ApplyRejected        ApplyOutcome = "rejected"
	ApplyUnknown         ApplyOutcome = "unknown"
)

// TargetCounts contains bounded per-target record gauges.
type TargetCounts struct {
	Desired   int64
	Current   int64
	Drift     int64
	Conflicts int64
}

type platformState struct {
	targets map[string]*targetState
}

type targetState struct {
	ready              float64
	counts             TargetCounts
	incompleteSources  map[Source]float64
	providerSnapshotAt time.Time
	queueDepth         float64
	queueRetries       float64
	plans              map[string]float64
	applies            map[ApplyOutcome]uint64
}

func newPlatformState() *platformState {
	return &platformState{targets: map[string]*targetState{}}
}

func newTargetState() *targetState {
	return &targetState{
		incompleteSources: map[Source]float64{},
		plans:             map[string]float64{},
		applies:           map[ApplyOutcome]uint64{},
	}
}

// SetTargetReadiness sets the current per-target Ready condition as 0 or 1.
func (m *Metrics) SetTargetReadiness(target string, ready bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.targetLocked(target)
	state.ready = boolFloat(ready)
}

// SetTargetCounts sets bounded desired/current/drift/conflict gauges.
func (m *Metrics) SetTargetCounts(target string, counts TargetCounts) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.targetLocked(target)
	state.counts = TargetCounts{
		Desired:   boundedInt(counts.Desired),
		Current:   boundedInt(counts.Current),
		Drift:     boundedInt(counts.Drift),
		Conflicts: boundedInt(counts.Conflicts),
	}
}

// SetSourceIncomplete records whether one fixed source category is incomplete.
func (m *Metrics) SetSourceIncomplete(target string, source Source, incomplete bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targetLocked(target).incompleteSources[boundedSource(source)] = boolFloat(incomplete)
}

// SetProviderSnapshotAge records the age of the last stable provider snapshot.
func (m *Metrics) SetProviderSnapshotAge(target string, age time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if age < 0 || math.IsNaN(age.Seconds()) {
		age = 0
	}
	if math.IsInf(age.Seconds(), 0) || age.Seconds() > maxGaugeValue {
		age = time.Duration(maxGaugeValue) * time.Second
	}
	m.targetLocked(target).providerSnapshotAt = m.now().Add(-age)
}

// SetCurrentPlanPhase records a one-hot view of the current reconciliation
// plan. Passing an unknown phase clears all known phases.
func (m *Metrics) SetCurrentPlanPhase(target string, phase api.ChangePlanPhase) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.targetLocked(target)
	for _, known := range knownPlanPhases() {
		state.plans[known] = 0
	}
	if bounded := boundedPlanPhase(phase); bounded != "unknown" {
		state.plans[bounded] = 1
	}
}

// SetQueueState sets current queue depth and retry count for one target.
func (m *Metrics) SetQueueState(target string, depth, retries int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.targetLocked(target)
	state.queueDepth = float64(boundedInt(int64(depth)))
	state.queueRetries = float64(boundedInt(int64(retries)))
}

// SetPlansByPhase sets the number of current plan objects in a fixed phase.
func (m *Metrics) SetPlansByPhase(target string, phase api.ChangePlanPhase, count int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targetLocked(target).plans[boundedPlanPhase(phase)] = float64(boundedInt(int64(count)))
}

// RecordApply increments one bounded apply-outcome counter.
func (m *Metrics) RecordApply(target string, outcome ApplyOutcome) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.targetLocked(target)
	state.applies[boundedApplyOutcome(outcome)]++
}

// RemoveTarget releases all series owned by a deleted target.
func (m *Metrics) RemoveTarget(target string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.platformLocked().targets, metricTarget(target))
}

func (m *Metrics) writePlatformLocked(w io.Writer) {
	platform := m.platformLocked()

	writeGaugeHeader(w, "target_ready", "Whether the target Ready condition is true.")
	writeGaugeHeader(w, "target_desired_records", "Desired logical records for the target.")
	writeGaugeHeader(w, "target_current_records", "Current provider records observed for the target.")
	writeGaugeHeader(w, "target_drift_records", "Logical records currently drifting for the target.")
	writeGaugeHeader(w, "target_conflicts", "Current bounded conflicts for the target.")
	writeGaugeHeader(w, "provider_snapshot_age_seconds", "Age in seconds of the last stable provider snapshot.")
	writeGaugeHeader(w, "target_queue_depth", "Current queued work for the target.")
	writeGaugeHeader(w, "target_queue_retries", "Current retry count for the target key.")
	writeGaugeHeader(w, "source_incomplete", "Whether a fixed discovery source category is incomplete.")
	writeGaugeHeader(w, "plans", "Current reconciliation plan state by bounded phase (one-hot).")
	fmt.Fprintf(w, "# HELP %s_applies_total Plan apply attempts by bounded outcome.\n", namespace)
	fmt.Fprintf(w, "# TYPE %s_applies_total counter\n", namespace)

	for _, target := range sortedTargetKeys(platform.targets) {
		state := platform.targets[target]
		writeTargetGauge(w, "target_ready", target, state.ready)
		writeTargetGauge(w, "target_desired_records", target, float64(state.counts.Desired))
		writeTargetGauge(w, "target_current_records", target, float64(state.counts.Current))
		writeTargetGauge(w, "target_drift_records", target, float64(state.counts.Drift))
		writeTargetGauge(w, "target_conflicts", target, float64(state.counts.Conflicts))
		if !state.providerSnapshotAt.IsZero() {
			age := m.now().Sub(state.providerSnapshotAt).Seconds()
			if age < 0 || math.IsNaN(age) {
				age = 0
			}
			if math.IsInf(age, 0) || age > maxGaugeValue {
				age = maxGaugeValue
			}
			writeTargetGauge(w, "provider_snapshot_age_seconds", target, age)
		}
		writeTargetGauge(w, "target_queue_depth", target, state.queueDepth)
		writeTargetGauge(w, "target_queue_retries", target, state.queueRetries)

		for _, source := range sortedSourceKeys(state.incompleteSources) {
			fmt.Fprintf(w, "%s_source_incomplete{target=%q,source=%q} %s\n", namespace, target, source, formatGauge(state.incompleteSources[source]))
		}
		for _, phase := range sortedStringFloatKeys(state.plans) {
			fmt.Fprintf(w, "%s_plans{target=%q,phase=%q} %s\n", namespace, target, phase, formatGauge(state.plans[phase]))
		}
		for _, outcome := range sortedApplyKeys(state.applies) {
			fmt.Fprintf(w, "%s_applies_total{target=%q,outcome=%q} %d\n", namespace, target, outcome, state.applies[outcome])
		}
	}
}

func (m *Metrics) targetLocked(target string) *targetState {
	platform := m.platformLocked()
	label := metricTarget(target)
	if state, ok := platform.targets[label]; ok {
		return state
	}
	if label != invalidTarget && label != overflowTarget && len(platform.targets) >= maxMetricTargets {
		label = overflowTarget
		if state, ok := platform.targets[label]; ok {
			return state
		}
	}
	state := newTargetState()
	platform.targets[label] = state
	return state
}

func (m *Metrics) platformLocked() *platformState {
	if m.platform == nil {
		m.platform = newPlatformState()
	}
	return m.platform
}

func metricTarget(target string) string {
	parts := strings.Split(target, "/")
	if len(parts) == 1 && len(validation.IsDNS1123Subdomain(parts[0])) == 0 {
		return target
	}
	if len(parts) == 2 && len(validation.IsDNS1123Label(parts[0])) == 0 && len(validation.IsDNS1123Subdomain(parts[1])) == 0 {
		return target
	}
	if len(parts) == 0 {
		return invalidTarget
	}
	return invalidTarget
}

func boundedSource(source Source) Source {
	switch source {
	case SourceService, SourceIngress, SourceGateway, SourceHTTPRoute, SourceEndpointSlice,
		SourcePolicy, SourceOwnership, SourceTarget, SourceChangePlan, SourceSecret, SourceProvider:
		return source
	default:
		return SourceUnknown
	}
}

func boundedPlanPhase(phase api.ChangePlanPhase) string {
	switch phase {
	case api.ChangePlanPendingApproval, api.ChangePlanApproved, api.ChangePlanApplying,
		api.ChangePlanSucceeded, api.ChangePlanFailed, api.ChangePlanStale, api.ChangePlanInterrupted:
		return string(phase)
	default:
		return "unknown"
	}
}

func knownPlanPhases() []string {
	return []string{
		string(api.ChangePlanPendingApproval), string(api.ChangePlanApproved), string(api.ChangePlanApplying),
		string(api.ChangePlanSucceeded), string(api.ChangePlanFailed), string(api.ChangePlanStale), string(api.ChangePlanInterrupted),
	}
}

func boundedApplyOutcome(outcome ApplyOutcome) ApplyOutcome {
	switch outcome {
	case ApplySucceeded, ApplyFailed, ApplyInterrupted, ApplyStale, ApplyPendingApproval, ApplyRejected:
		return outcome
	default:
		return ApplyUnknown
	}
}

func boundedInt(value int64) int64 {
	if value < 0 {
		return 0
	}
	if value > maxGaugeValue {
		return maxGaugeValue
	}
	return value
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func writeGaugeHeader(w io.Writer, name, help string) {
	fmt.Fprintf(w, "# HELP %s_%s %s\n", namespace, name, help)
	fmt.Fprintf(w, "# TYPE %s_%s gauge\n", namespace, name)
}

func writeTargetGauge(w io.Writer, name, target string, value float64) {
	fmt.Fprintf(w, "%s_%s{target=%q} %s\n", namespace, name, target, formatGauge(value))
}

func formatGauge(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func sortedTargetKeys(values map[string]*targetState) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSourceKeys(values map[Source]float64) []Source {
	keys := make([]Source, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func sortedStringFloatKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedApplyKeys(values map[ApplyOutcome]uint64) []ApplyOutcome {
	keys := make([]ApplyOutcome, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
