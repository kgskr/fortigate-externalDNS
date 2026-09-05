package metrics

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
)

func TestPlatformMetricsExposition(t *testing.T) {
	m := New()
	m.SetTargetReadiness("edge", true)
	m.SetTargetCounts("edge", TargetCounts{Desired: 8, Current: 7, Drift: 1, Conflicts: 2})
	m.SetSourceIncomplete("edge", SourceEndpointSlice, true)
	m.SetProviderSnapshotAge("edge", 90*time.Second)
	m.SetQueueState("edge", 3, 2)
	m.SetPlansByPhase("edge", api.ChangePlanPendingApproval, 1)
	m.RecordApply("edge", ApplyFailed)
	m.RecordApply("edge", ApplyFailed)

	body := scrape(m)
	wants := []string{
		`fortigate_external_dns_target_ready{target="edge"} 1`,
		`fortigate_external_dns_target_desired_records{target="edge"} 8`,
		`fortigate_external_dns_target_current_records{target="edge"} 7`,
		`fortigate_external_dns_target_drift_records{target="edge"} 1`,
		`fortigate_external_dns_target_conflicts{target="edge"} 2`,
		`fortigate_external_dns_source_incomplete{target="edge",source="endpointslice"} 1`,
		`fortigate_external_dns_provider_snapshot_age_seconds{target="edge"} 90`,
		`fortigate_external_dns_target_queue_depth{target="edge"} 3`,
		`fortigate_external_dns_target_queue_retries{target="edge"} 2`,
		`fortigate_external_dns_plans{target="edge",phase="PendingApproval"} 1`,
		`fortigate_external_dns_applies_total{target="edge",outcome="failed"} 2`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("platform metrics missing %q\n--- got ---\n%s", want, body)
		}
	}
}

func TestMetricLabelsCollapseCardinalityAttackInputs(t *testing.T) {
	m := New()
	attackInputs := []string{
		"api/attacker/example.com",
		"source-object-7f98d6c5",
		"uid-27f27e86-87a1-43ac-b755-61266bdb21e1",
		"provider-record-id-98431",
		"token=super-secret-value Authorization: Bearer credentials",
		`{"results":[{"hostname":"full-record.example.com"}]}`,
	}

	// Each attacker-controlled value is submitted through a public recording
	// boundary. Only fixed fallback labels may be exposed.
	m.SetTargetReadiness(attackInputs[0], true)
	m.SetSourceIncomplete("edge", Source(attackInputs[2]), true)
	m.SetPlansByPhase("edge", api.ChangePlanPhase(attackInputs[3]), 1)
	m.RecordApply("edge", ApplyOutcome(attackInputs[4]))
	m.RecordOperation(attackInputs[1], attackInputs[3])
	m.RecordCleanupRefused(attackInputs[5])
	m.RecordReconcile(time.Second, errors.New(attackInputs[4]))

	body := scrape(m)
	for _, value := range attackInputs {
		if strings.Contains(body, value) {
			t.Fatalf("metric label leaked cardinality attack input %q:\n%s", value, body)
		}
	}
	wants := []string{
		`target="invalid"`,
		`source="unknown"`,
		`phase="unknown"`,
		`outcome="unknown"`,
		`type="unknown",result="unknown"`,
		`cleanup_refused_total{reason="unknown"}`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("bounded fallback label missing %q:\n%s", want, body)
		}
	}
}

func TestTrackedTargetSeriesAreCapped(t *testing.T) {
	m := New()
	for i := 0; i < maxMetricTargets+50; i++ {
		m.SetTargetReadiness(fmt.Sprintf("target-%03d", i), true)
	}
	body := scrape(m)
	if !strings.Contains(body, `target="overflow"`) {
		t.Fatalf("overflow target series missing:\n%s", body)
	}
	if strings.Contains(body, fmt.Sprintf(`target="target-%03d"`, maxMetricTargets+49)) {
		t.Fatal("target series exceeded the configured cardinality cap")
	}
	if got := strings.Count(body, "fortigate_external_dns_target_ready{target="); got > maxMetricTargets+1 {
		t.Fatalf("ready series count = %d, want <= %d", got, maxMetricTargets+1)
	}
}

func TestPlatformMetricsClampInvalidGauges(t *testing.T) {
	m := New()
	now := time.Unix(1_700_000_000, 0)
	m.now = func() time.Time { return now }
	m.SetTargetCounts("edge", TargetCounts{Desired: -1, Current: maxGaugeValue + 1, Drift: -2, Conflicts: maxGaugeValue + 2})
	m.SetProviderSnapshotAge("edge", -time.Second)
	m.SetQueueState("edge", -1, -2)
	m.SetPlansByPhase("edge", api.ChangePlanSucceeded, -1)

	body := scrape(m)
	wants := []string{
		`target_desired_records{target="edge"} 0`,
		`target_current_records{target="edge"} 1e+09`,
		`target_drift_records{target="edge"} 0`,
		`target_conflicts{target="edge"} 1e+09`,
		`provider_snapshot_age_seconds{target="edge"} 0`,
		`target_queue_depth{target="edge"} 0`,
		`target_queue_retries{target="edge"} 0`,
		`plans{target="edge",phase="Succeeded"} 0`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("clamped metric missing %q:\n%s", want, body)
		}
	}
}

func TestPlatformMetricsConcurrentUpdatesAndScrapes(t *testing.T) {
	m := New()
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for n := 0; n < 100; n++ {
				target := fmt.Sprintf("target-%02d", worker%4)
				m.SetTargetReadiness(target, n%2 == 0)
				m.SetTargetCounts(target, TargetCounts{Desired: int64(n), Current: int64(n - 1), Drift: 1})
				m.SetSourceIncomplete(target, SourceService, n%3 == 0)
				m.SetProviderSnapshotAge(target, time.Duration(n)*time.Second)
				m.SetQueueState(target, n, worker)
				m.SetPlansByPhase(target, api.ChangePlanApplying, 1)
				m.RecordApply(target, ApplySucceeded)
				if n%10 == 0 {
					_ = scrape(m)
				}
			}
		}(i)
	}
	workers.Wait()
	if body := scrape(m); !strings.Contains(body, "fortigate_external_dns_applies_total") {
		t.Fatal("concurrent metrics scrape lost platform exposition")
	}
}

func TestRemoveTargetDropsOwnedSeries(t *testing.T) {
	m := New()
	m.SetTargetReadiness("edge", true)
	m.RecordApply("edge", ApplySucceeded)
	m.RemoveTarget("edge")
	if body := scrape(m); strings.Contains(body, `target="edge"`) {
		t.Fatalf("deleted target series remain:\n%s", body)
	}
}

func TestNamespacedTargetsHaveIndependentSeries(t *testing.T) {
	m := New()
	m.SetTargetReadiness("team-a/edge", true)
	m.SetTargetReadiness("team-b/edge", false)
	body := scrape(m)
	for _, want := range []string{
		`target_ready{target="team-a/edge"} 1`,
		`target_ready{target="team-b/edge"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q:\n%s", want, body)
		}
	}
}

func TestSnapshotAgeAdvancesAndUnobservedIsAbsent(t *testing.T) {
	m := New()
	now := time.Unix(1_700_000_000, 0)
	m.now = func() time.Time { return now }
	m.SetTargetReadiness("system/unobserved", false)
	if body := scrape(m); strings.Contains(body, `provider_snapshot_age_seconds{target="system/unobserved"}`) {
		t.Fatalf("unobserved snapshot emitted an age:\n%s", body)
	}
	m.SetProviderSnapshotAge("system/edge", 10*time.Second)
	now = now.Add(5 * time.Second)
	if body := scrape(m); !strings.Contains(body, `provider_snapshot_age_seconds{target="system/edge"} 15`) {
		t.Fatalf("snapshot age did not advance:\n%s", body)
	}
}

func TestCurrentPlanPhaseIsOneHotAndDottedTargetIsValid(t *testing.T) {
	m := New()
	target := "system/edge.prod.example"
	m.SetCurrentPlanPhase(target, api.ChangePlanPendingApproval)
	m.SetCurrentPlanPhase(target, api.ChangePlanStale)
	body := scrape(m)
	if !strings.Contains(body, `plans{target="system/edge.prod.example",phase="Stale"} 1`) ||
		!strings.Contains(body, `plans{target="system/edge.prod.example",phase="PendingApproval"} 0`) {
		t.Fatalf("current plan phase is not one-hot:\n%s", body)
	}
}

func scrape(m *Metrics) string {
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	return recorder.Body.String()
}
