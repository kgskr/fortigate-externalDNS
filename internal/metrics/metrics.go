// Package metrics provides a small, dependency-free Prometheus text-exposition
// recorder for the controller. It intentionally avoids pulling in a metrics
// framework: the controller currently depends only on the Kubernetes client
// libraries, and the exposed surface is small and stable.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

const namespace = "fortigate_external_dns"

var defaultBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// Metrics is a concurrency-safe recorder. The zero value is not usable; call New.
type Metrics struct {
	mu sync.Mutex

	reconcileTotal  uint64
	reconcileErrors uint64
	lastSuccessUnix int64

	buckets       []float64
	bucketCounts  []uint64
	durationSum   float64
	durationCount uint64

	operations map[opKey]uint64

	cleanupRefused map[string]uint64

	buildVersion string
	buildCommit  string

	platform *platformState

	now func() time.Time
}

type opKey struct {
	opType string
	result string
}

// New returns a ready-to-use recorder.
func New() *Metrics {
	return &Metrics{
		buckets:        append([]float64(nil), defaultBuckets...),
		bucketCounts:   make([]uint64, len(defaultBuckets)),
		operations:     map[opKey]uint64{},
		cleanupRefused: map[string]uint64{},
		platform:       newPlatformState(),
		now:            time.Now,
	}
}

// SetBuildInfo records the stamped build identity exposed by the build_info
// gauge so a running pod can be correlated to code.
func (m *Metrics) SetBuildInfo(version, commit string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buildVersion = version
	m.buildCommit = commit
}

// RecordCleanupRefused records one mass-cleanup guard trip by reason.
func (m *Metrics) RecordCleanupRefused(reason string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupRefused[boundedCleanupReason(reason)]++
}

// RecordReconcile records the outcome of one reconcile loop.
func (m *Metrics) RecordReconcile(duration time.Duration, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.reconcileTotal++
	seconds := duration.Seconds()
	m.durationSum += seconds
	m.durationCount++
	for i, upper := range m.buckets {
		if seconds <= upper {
			m.bucketCounts[i]++
		}
	}
	if err != nil {
		m.reconcileErrors++
		return
	}
	m.lastSuccessUnix = m.now().Unix()
}

// RecordOperation records a planned or applied operation by type and result.
func (m *Metrics) RecordOperation(opType, result string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.operations[opKey{opType: boundedOperationType(opType), result: boundedOperationResult(result)}]++
}

// Handler serves the metrics in Prometheus text exposition format.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		m.write(w)
	})
}

func (m *Metrics) write(w http.ResponseWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Fprintf(w, "# HELP %s_reconcile_total Total reconcile loops executed.\n", namespace)
	fmt.Fprintf(w, "# TYPE %s_reconcile_total counter\n", namespace)
	fmt.Fprintf(w, "%s_reconcile_total %d\n", namespace, m.reconcileTotal)

	fmt.Fprintf(w, "# HELP %s_reconcile_errors_total Total reconcile loops that returned an error.\n", namespace)
	fmt.Fprintf(w, "# TYPE %s_reconcile_errors_total counter\n", namespace)
	fmt.Fprintf(w, "%s_reconcile_errors_total %d\n", namespace, m.reconcileErrors)

	fmt.Fprintf(w, "# HELP %s_last_successful_reconcile_timestamp_seconds Unix timestamp of the last successful reconcile.\n", namespace)
	fmt.Fprintf(w, "# TYPE %s_last_successful_reconcile_timestamp_seconds gauge\n", namespace)
	fmt.Fprintf(w, "%s_last_successful_reconcile_timestamp_seconds %d\n", namespace, m.lastSuccessUnix)

	fmt.Fprintf(w, "# HELP %s_reconcile_duration_seconds Duration of reconcile loops in seconds.\n", namespace)
	fmt.Fprintf(w, "# TYPE %s_reconcile_duration_seconds histogram\n", namespace)
	for i, upper := range m.buckets {
		fmt.Fprintf(w, "%s_reconcile_duration_seconds_bucket{le=\"%s\"} %d\n",
			namespace, strconv.FormatFloat(upper, 'g', -1, 64), m.bucketCounts[i])
	}
	fmt.Fprintf(w, "%s_reconcile_duration_seconds_bucket{le=\"+Inf\"} %d\n", namespace, m.durationCount)
	fmt.Fprintf(w, "%s_reconcile_duration_seconds_sum %s\n", namespace, strconv.FormatFloat(m.durationSum, 'g', -1, 64))
	fmt.Fprintf(w, "%s_reconcile_duration_seconds_count %d\n", namespace, m.durationCount)

	fmt.Fprintf(w, "# HELP %s_operations_total DNS operations by type and result.\n", namespace)
	fmt.Fprintf(w, "# TYPE %s_operations_total counter\n", namespace)
	for _, key := range sortedOpKeys(m.operations) {
		fmt.Fprintf(w, "%s_operations_total{type=%q,result=%q} %d\n", namespace, key.opType, key.result, m.operations[key])
	}

	fmt.Fprintf(w, "# HELP %s_cleanup_refused_total Reconcile cycles whose cleanup operations were refused by the mass-cleanup guard, by reason.\n", namespace)
	fmt.Fprintf(w, "# TYPE %s_cleanup_refused_total counter\n", namespace)
	for _, reason := range sortedKeys(m.cleanupRefused) {
		fmt.Fprintf(w, "%s_cleanup_refused_total{reason=%q} %d\n", namespace, reason, m.cleanupRefused[reason])
	}

	if m.buildVersion != "" {
		fmt.Fprintf(w, "# HELP %s_build_info Build identity of the running controller.\n", namespace)
		fmt.Fprintf(w, "# TYPE %s_build_info gauge\n", namespace)
		fmt.Fprintf(w, "%s_build_info{version=%q,commit=%q} 1\n", namespace, m.buildVersion, m.buildCommit)
	}

	m.writePlatformLocked(w)
}

func boundedOperationType(value string) string {
	switch value {
	case "create", "update", "replace", "delete", "deactivate", "conflict":
		return value
	default:
		return "unknown"
	}
}

func boundedOperationResult(value string) string {
	switch value {
	case "planned", "conflict", "skipped", "failed", "applied":
		return value
	default:
		return "unknown"
	}
}

func boundedCleanupReason(value string) string {
	switch value {
	case "empty-desired", "cap-exceeded", "source-incomplete", "prerequisite-failed":
		return value
	default:
		return "unknown"
	}
}

func sortedKeys(counters map[string]uint64) []string {
	keys := make([]string, 0, len(counters))
	for key := range counters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedOpKeys(operations map[opKey]uint64) []opKey {
	keys := make([]opKey, 0, len(operations))
	for key := range operations {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].opType != keys[j].opType {
			return keys[i].opType < keys[j].opType
		}
		return keys[i].result < keys[j].result
	})
	return keys
}
