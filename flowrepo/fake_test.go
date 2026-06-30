package flowrepo_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/brian-bell/flowstate/flowrepo"
	"github.com/brian-bell/flowstate/flowstore"
)

func TestFakeCopiesRecordsAtRepositoryBoundary(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	repo := flowrepo.NewFake(flowrepo.FakeOptions{Now: func() time.Time { return now }})

	created, err := repo.Create(flowstore.FlowRecord{
		FlowID:       "copy-boundary",
		Title:        "Copy boundary",
		Instructions: "protect fake state",
		RepoPath:     filepath.Join(t.TempDir(), "repo"),
		Phases: []flowstore.FlowPhase{{
			PhaseID:   "plan",
			Title:     "Plan",
			Kind:      "plan",
			Status:    flowstore.PhaseReady,
			Order:     1,
			LaunchIDs: []string{"launch-1"},
			Sessions:  []flowstore.Session{{Provider: "codex", SessionID: "session-1", LaunchID: "launch-1"}},
		}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	created.Phases[0].Status = flowstore.PhaseCompleted
	created.Phases[0].LaunchIDs[0] = "mutated-launch"
	created.Phases[0].Sessions[0].SessionID = "mutated-session"

	read, err := repo.Read("copy-boundary")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if phase := read.Phases[0]; phase.Status != flowstore.PhaseReady || phase.LaunchIDs[0] != "launch-1" || phase.Sessions[0].SessionID != "session-1" {
		t.Fatalf("Read() phase = %#v, want original values", phase)
	}

	read.Phases[0].Status = flowstore.PhaseCompleted
	listed, err := repo.List(flowstore.FlowFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	listed[0].Phases[0].Status = flowstore.PhaseCompleted

	reread, err := repo.Read("copy-boundary")
	if err != nil {
		t.Fatalf("Read() after list mutation error = %v", err)
	}
	if got := reread.Phases[0].Status; got != flowstore.PhaseReady {
		t.Fatalf("persisted phase status = %q, want ready", got)
	}
}
