package flowstore_test

import (
	"testing"
	"time"

	"github.com/brian-bell/flowstate/flowstore"
)

// TestRefreshPhaseReadinessExported proves the readiness recompute is reachable
// cross-package alongside DeriveStatus, so the fake and future repository
// implementations reuse one set of derived-state rules.
func TestRefreshPhaseReadinessExported(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	record := flowstore.FlowRecord{
		Phases: []flowstore.FlowPhase{
			{PhaseID: "plan", Status: flowstore.PhaseCompleted, Order: 1},
			{PhaseID: "plan-review", Status: flowstore.PhasePending, Order: 2, Outcome: ""},
		},
	}

	refreshed := flowstore.RefreshPhaseReadiness(record, now)

	if got := refreshed.Phases[1].Status; got != flowstore.PhaseReady {
		t.Fatalf("plan-review status = %q, want ready after completed plan", got)
	}
	if !refreshed.Phases[1].UpdatedAt.Equal(now) {
		t.Fatalf("plan-review UpdatedAt = %s, want %s", refreshed.Phases[1].UpdatedAt, now)
	}
	if status := flowstore.DeriveStatus(refreshed); status == "" {
		t.Fatal("DeriveStatus returned empty status")
	}
}
