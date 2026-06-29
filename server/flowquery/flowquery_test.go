package flowquery_test

import (
	"testing"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/server/flowquery"
)

func TestViewComputesLaunchabilityAndNextLaunchablePhase(t *testing.T) {
	record := flowstore.FlowRecord{
		FlowID: "flow-1",
		PR: flowstore.PullRequest{
			Provider:   "github",
			Number:     42,
			URL:        "https://github.com/brian-bell/flowstate/pull/42",
			HeadBranch: "flow/read",
			BaseBranch: "main",
		},
		Phases: []flowstore.FlowPhase{
			{PhaseID: "plan", Status: flowstore.PhaseCompleted, Order: 1},
			{PhaseID: "plan-review", Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved, Order: 2},
			{PhaseID: "implementation", Status: flowstore.PhaseCompleted, Order: 3},
			{PhaseID: "review-loop", Status: flowstore.PhaseCompleted, Order: 4},
			{PhaseID: "pr-creation", Status: flowstore.PhaseCompleted, Order: 5},
			{PhaseID: "autoreview", Status: flowstore.PhaseBlocked, Order: 6},
			{PhaseID: "merge", Status: flowstore.PhaseReady, Order: 7},
		},
	}

	view := flowquery.Build(record)

	if view.NextLaunchablePhase == nil || view.NextLaunchablePhase.PhaseID != "autoreview" {
		t.Fatalf("next launchable phase = %#v, want autoreview recovery", view.NextLaunchablePhase)
	}
	if !view.Phases[5].Launchable {
		t.Fatal("blocked autoreview with PR target and satisfied predecessors should be launchable")
	}
	if !view.Phases[6].Launchable {
		t.Fatal("ready merge should remain directly launchable")
	}
}

func TestViewReportsMergeAsNextLaunchablePhase(t *testing.T) {
	record := flowstore.FlowRecord{
		FlowID: "flow-1",
		Phases: []flowstore.FlowPhase{
			{PhaseID: "plan", Status: flowstore.PhaseCompleted, Order: 1},
			{PhaseID: "plan-review", Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved, Order: 2},
			{PhaseID: "implementation", Status: flowstore.PhaseCompleted, Order: 3},
			{PhaseID: "review-loop", Status: flowstore.PhaseCompleted, Order: 4},
			{PhaseID: "pr-creation", Status: flowstore.PhaseCompleted, Order: 5},
			{PhaseID: "autoreview", Status: flowstore.PhaseCompleted, Order: 6},
			{PhaseID: "merge", Status: flowstore.PhaseReady, Order: 7},
		},
	}

	view := flowquery.Build(record)

	if view.NextLaunchablePhase == nil || view.NextLaunchablePhase.PhaseID != "merge" {
		t.Fatalf("next launchable phase = %#v, want merge", view.NextLaunchablePhase)
	}
}

func TestPhaseCanLaunchRejectsAutoreviewWithoutSatisfiedPredecessorsOrPR(t *testing.T) {
	phase := flowstore.FlowPhase{PhaseID: "autoreview", Status: flowstore.PhaseNeedsAttention, Order: 6}
	withoutPR := flowstore.FlowRecord{
		Phases: []flowstore.FlowPhase{
			{PhaseID: "plan", Status: flowstore.PhaseCompleted, Order: 1},
			{PhaseID: "plan-review", Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved, Order: 2},
			{PhaseID: "implementation", Status: flowstore.PhaseCompleted, Order: 3},
			{PhaseID: "review-loop", Status: flowstore.PhaseCompleted, Order: 4},
			{PhaseID: "pr-creation", Status: flowstore.PhaseCompleted, Order: 5},
			phase,
		},
	}
	if flowquery.PhaseCanLaunch(withoutPR, phase) {
		t.Fatal("autoreview should not launch without PR target")
	}

	withPR := withoutPR
	withPR.PR = flowstore.PullRequest{
		Provider:   "github",
		Number:     42,
		URL:        "https://github.com/brian-bell/flowstate/pull/42",
		HeadBranch: "flow/read",
		BaseBranch: "main",
	}
	withPR.Phases[3].Status = flowstore.PhaseRunning
	if flowquery.PhaseCanLaunch(withPR, phase) {
		t.Fatal("autoreview should not launch with unsatisfied predecessors")
	}
}

func TestStaleRunningStatusUsesSessionHelpersWithoutRuntimeVisibility(t *testing.T) {
	mismatch := flowquery.BuildPhase(flowstore.FlowRecord{}, flowstore.FlowPhase{
		PhaseID:   "review-loop",
		Status:    flowstore.PhaseNeedsAttention,
		LaunchIDs: []string{"launch-1"},
		Sessions:  []flowstore.Session{{SessionID: "codex-1", LaunchID: "unknown-launch"}},
	})
	if mismatch.StaleRunningStatus == nil || *mismatch.StaleRunningStatus != flowquery.StaleSessionMismatch {
		t.Fatalf("session-mismatch stale status = %#v, want session mismatch", mismatch.StaleRunningStatus)
	}

	awaiting := flowquery.BuildPhase(flowstore.FlowRecord{}, flowstore.FlowPhase{
		PhaseID:   "implementation",
		Status:    flowstore.PhaseRunning,
		LaunchIDs: []string{"launch-1"},
	})
	if awaiting.StaleRunningStatus == nil || *awaiting.StaleRunningStatus != flowquery.StaleAwaitingSession {
		t.Fatalf("awaiting stale status = %#v, want awaiting session", awaiting.StaleRunningStatus)
	}
	if awaiting.ActiveRuntimeJob != nil {
		t.Fatalf("active runtime job = %#v, want nil while runtime visibility is unavailable", awaiting.ActiveRuntimeJob)
	}

	missingSessionID := flowquery.BuildPhase(flowstore.FlowRecord{}, flowstore.FlowPhase{
		PhaseID:   "review-loop",
		Status:    flowstore.PhaseNeedsAttention,
		LaunchIDs: []string{"launch-1"},
		Sessions:  []flowstore.Session{{LaunchID: "launch-1"}},
	})
	if missingSessionID.StaleRunningStatus == nil || *missingSessionID.StaleRunningStatus != flowquery.StaleMissingSessionID {
		t.Fatalf("missing-session stale status = %#v, want missing session ID", missingSessionID.StaleRunningStatus)
	}
}
