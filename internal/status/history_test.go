package status

import (
	"context"
	"testing"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
)

func TestWriterDoesNotAppendIdenticalHistoryEntries(t *testing.T) {
	client := newFakeClient(t)
	writer, err := NewWriter(client, "dns-system", "edge", 5)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC)
	writer.now = func() time.Time { return now }
	counts := api.ReconcileCounts{Desired: 4, Current: 4}
	write := func(offset time.Duration, hash string, phase api.ChangePlanPhase, counts api.ReconcileCounts) {
		t.Helper()
		snapshot := Snapshot{
			TargetGeneration: 1, PlanHash: hash, Conditions: healthyConditions(1),
			Audit: &Audit{PlanHash: hash, Phase: phase, Timestamp: now.Add(offset), Counts: counts},
		}
		if err := writer.Write(context.Background(), snapshot); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		write(time.Duration(i)*time.Minute, testPlanHash, api.ChangePlanSucceeded, counts)
	}
	got := getStatus(t, client, "dns-system", "edge")
	if len(got.Status.History) != 1 {
		t.Fatalf("identical cycles appended %d entries, want 1: %#v", len(got.Status.History), got.Status.History)
	}
	if !got.Status.History[0].Timestamp.Time.Equal(now) {
		t.Fatalf("entry timestamp must stay at first observation, got %s", got.Status.History[0].Timestamp)
	}

	// A different phase, different counts, or a different hash is a new entry.
	write(11*time.Minute, testPlanHash, api.ChangePlanFailed, counts)
	write(12*time.Minute, testPlanHash, api.ChangePlanFailed, api.ReconcileCounts{Desired: 4, Current: 4, Drift: 1})
	write(13*time.Minute, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", api.ChangePlanFailed, api.ReconcileCounts{Desired: 4, Current: 4, Drift: 1})
	got = getStatus(t, client, "dns-system", "edge")
	if len(got.Status.History) != 4 {
		t.Fatalf("history length = %d, want 4: %#v", len(got.Status.History), got.Status.History)
	}

	// Only the LATEST entry is compared: returning to an earlier value is new information.
	write(14*time.Minute, testPlanHash, api.ChangePlanSucceeded, counts)
	got = getStatus(t, client, "dns-system", "edge")
	if len(got.Status.History) != 5 {
		t.Fatalf("history length = %d, want 5", len(got.Status.History))
	}
}
