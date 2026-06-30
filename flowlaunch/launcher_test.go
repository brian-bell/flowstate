package flowlaunch_test

import (
	"strings"
	"testing"

	"github.com/brian-bell/flowstate/flowlaunch"
	"github.com/brian-bell/flowstate/flowstore"
)

func TestLauncherPreflightUsesReusableValidationMessages(t *testing.T) {
	launcher := flowlaunch.Launcher{}
	_, err := launcher.Preflight(flowlaunch.Request{
		Record: flowstore.FlowRecord{FlowID: "flow-1", RepoPath: "/dev/alpha"},
		Phase:  flowstore.FlowPhase{PhaseID: "plan", Status: flowstore.PhaseReady},
	})
	if err == nil || err.Error() != "agent command is required" {
		t.Fatalf("Preflight() error = %v, want reusable missing-agent validation", err)
	}

	launcher.AgentCommand = "codex"
	_, err = launcher.Preflight(flowlaunch.Request{
		Record: flowstore.FlowRecord{FlowID: "flow-1", RepoPath: "/dev/alpha"},
		Phase:  flowstore.FlowPhase{PhaseID: " Plan-Review ", Status: flowstore.PhaseReady},
	})
	if err == nil || err.Error() != "Plan Review needs a linked plan before launch" {
		t.Fatalf("Preflight() error = %v, want linked-plan guard", err)
	}
}

func TestLauncherPrepareGenericPhaseReadsLinkedPlanBody(t *testing.T) {
	phase := flowstore.FlowPhase{PhaseID: "qa-check", Title: "QA Check", Status: flowstore.PhaseReady}
	record := flowstore.FlowRecord{
		FlowID:       "flow-1",
		RepoPath:     "/dev/alpha",
		WorktreePath: "/dev/alpha-worktrees/flow-qa",
		PlanID:       "plan-1",
		PlanPath:     "/state/plans/plan-1/plan.md",
		Phases:       []flowstore.FlowPhase{phase},
	}
	readPlanCalled := false
	launcher := flowlaunch.Launcher{
		ReadPlan: func(string) (string, error) {
			readPlanCalled = true
			return "generic plan body", nil
		},
		AddPhaseLaunchID: func(update flowstore.PhaseLaunchUpdate) (flowstore.FlowRecord, error) {
			return flowstore.FlowRecord{FlowID: update.FlowID, Phases: []flowstore.FlowPhase{phase}}, nil
		},
		NewLaunchID:  func() string { return "launch-1" },
		AgentCommand: "codex",
		Templates:    flowlaunch.PromptTemplates{Generic: "Generic template: {plan_body}"},
	}

	prepared, err := launcher.Preflight(flowlaunch.Request{Record: record, Phase: phase, Headless: true})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	result, err := launcher.Prepare(prepared)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	if !readPlanCalled {
		t.Fatal("generic phase should read linked plan body")
	}
	if !strings.Contains(result.Context.InitialPrompt, "generic plan body") {
		t.Fatalf("prompt missing linked plan body: %q", result.Context.InitialPrompt)
	}
	if !result.Context.FlowLaunchTracked || !result.Context.Embedded || !result.Context.Headless {
		t.Fatalf("CLI launch context = %#v, want tracked embedded headless", result.Context)
	}
}

func TestPhaseCanLaunchUsesCanonicalFlowRules(t *testing.T) {
	record := flowstore.FlowRecord{
		PR: flowstore.PullRequest{
			Provider:   "github",
			Number:     42,
			URL:        "https://github.com/brian-bell/flowstate/pull/42",
			HeadBranch: "flow/review",
			BaseBranch: "main",
		},
		Phases: []flowstore.FlowPhase{
			{PhaseID: "plan", Status: flowstore.PhaseCompleted, Order: 1},
			{PhaseID: "plan-review", Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved, Order: 2},
			{PhaseID: "implementation", Status: flowstore.PhaseCompleted, Order: 3},
			{PhaseID: "review-loop", Status: flowstore.PhaseCompleted, Order: 4},
			{PhaseID: "pr-creation", Status: flowstore.PhaseCompleted, Order: 5},
			{PhaseID: " Autoreview ", Status: flowstore.PhaseNeedsAttention, Order: 6},
			{PhaseID: "merge", Status: flowstore.PhaseReady, Order: 7},
		},
	}

	if !flowlaunch.PhaseCanLaunch(record, record.Phases[5]) {
		t.Fatal("autoreview needs_attention with PR target should be launchable")
	}
	if !flowlaunch.PhaseCanLaunch(record, record.Phases[6]) {
		t.Fatal("ready merge phase should be launchable")
	}

	recovery := flowstore.FlowRecord{
		Phases: []flowstore.FlowPhase{
			{PhaseID: "plan", Status: flowstore.PhaseCompleted, Order: 1},
			{PhaseID: "plan-review", Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved, Order: 2},
			{PhaseID: "implementation", Status: flowstore.PhaseNeedsAttention, Outcome: "runtime_canceled", Order: 3},
		},
	}
	if !flowlaunch.PhaseCanLaunch(recovery, recovery.Phases[2]) {
		t.Fatal("runtime-canceled needs_attention phase should be launchable")
	}

	planReviewRecovery := flowstore.FlowRecord{
		Phases: []flowstore.FlowPhase{
			{PhaseID: "plan", Status: flowstore.PhaseCompleted, Order: 1},
			{PhaseID: "plan-review", Status: flowstore.PhaseNeedsAttention, Outcome: flowstore.OutcomeChangesRequested, Notes: "Runtime job failed to start: runtime unavailable", Order: 2},
		},
	}
	if !flowlaunch.PhaseCanLaunch(planReviewRecovery, planReviewRecovery.Phases[1]) {
		t.Fatal("plan-review runtime failure should be launchable")
	}
}

func TestPhaseByIDPrefersExactIDBeforeNormalizedFallback(t *testing.T) {
	record := flowstore.FlowRecord{
		Phases: []flowstore.FlowPhase{
			{PhaseID: " Implementation ", Title: "Legacy duplicate", Status: flowstore.PhaseCompleted},
			{PhaseID: "implementation", Title: "Exact requested phase", Status: flowstore.PhaseReady},
		},
	}

	phase, ok := flowlaunch.PhaseByID(record, "implementation")
	if !ok {
		t.Fatal("PhaseByID() ok = false, want exact phase")
	}
	if phase.Title != "Exact requested phase" {
		t.Fatalf("PhaseByID() = %#v, want exact phase before normalized duplicate", phase)
	}
}
