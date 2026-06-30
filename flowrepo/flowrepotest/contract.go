package flowrepotest

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/brian-bell/flowstate/flowrepo"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/artifacts"
)

// Factory returns a fresh repository fixture for one contract subtest.
type Factory func(t testing.TB, clock *ScriptedClock) Fixture

// Fixture contains the repository under test and storage-specific plan seeding.
type Fixture struct {
	Repo               flowrepo.FlowRepository
	SeedPlan           func(planID string) string
	SeedUnreadablePlan func(planID string) string
	SeedPlanPhase      func(planID, phaseID string) string
	PlanPhaseStatus    func(planID, phaseID string) string
	BreakPlanMetadata  func(planID string)
}

// ScriptedClock is a deterministic clock for repository contract assertions.
type ScriptedClock struct {
	current time.Time
}

// NewScriptedClock starts a deterministic clock at a fixed UTC timestamp.
func NewScriptedClock() *ScriptedClock {
	return &ScriptedClock{current: time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)}
}

// Set moves the clock to a specific timestamp.
func (c *ScriptedClock) Set(t time.Time) {
	c.current = t.UTC()
}

// At moves the clock to base plus step seconds and returns that timestamp.
func (c *ScriptedClock) At(step int) time.Time {
	c.current = time.Date(2026, 6, 30, 12, 0, step, 0, time.UTC)
	return c.current
}

// Now returns the current scripted timestamp.
func (c *ScriptedClock) Now() time.Time {
	return c.current
}

// RunContract verifies shared FlowRepository behavior.
func RunContract(t testing.TB, factory Factory) {
	t.Helper()
	runner, ok := t.(interface {
		Run(string, func(*testing.T)) bool
	})
	if !ok {
		t.Fatalf("RunContract requires a test runner with Run")
	}

	runner.Run("create read list delete ordering", func(t *testing.T) {
		clock := NewScriptedClock()
		fixture := contractFixture(t, factory, clock)
		repo := fixture.Repo
		repoPath := filepath.Join(t.TempDir(), "repo")

		clock.At(1)
		first := createFlow(t, repo, flowstore.FlowRecord{
			FlowID:       "flow-a",
			Title:        "Flow A",
			Instructions: "first",
			RepoPath:     repoPath,
		})
		clock.At(2)
		second := createFlow(t, repo, flowstore.FlowRecord{
			FlowID:       "flow-b",
			Title:        "Flow B",
			Instructions: "second",
			RepoPath:     repoPath,
		})

		read := readFlow(t, repo, first.FlowID)
		if read.FlowID != first.FlowID {
			t.Fatalf("Read FlowID = %q, want %q", read.FlowID, first.FlowID)
		}
		records, err := repo.List(flowstore.FlowFilter{RepoPath: repoPath})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if got, want := flowIDs(records), []string{second.FlowID, first.FlowID}; !reflect.DeepEqual(got, want) {
			t.Fatalf("List flow IDs = %v, want %v", got, want)
		}
		if err := repo.Delete(first.FlowID); err != nil {
			t.Fatalf("Delete(%q) error = %v", first.FlowID, err)
		}
		if err := repo.Delete(first.FlowID); err == nil {
			t.Fatalf("second Delete(%q) error = nil, want not found", first.FlowID)
		}
		if _, err := repo.Read(first.FlowID); err == nil {
			t.Fatalf("Read(%q) error = nil, want not found", first.FlowID)
		}
		remaining, err := repo.List(flowstore.FlowFilter{RepoPath: repoPath})
		if err != nil {
			t.Fatalf("List() after delete error = %v", err)
		}
		if got, want := flowIDs(remaining), []string{second.FlowID}; !reflect.DeepEqual(got, want) {
			t.Fatalf("remaining flow IDs = %v, want %v", got, want)
		}
	})

	runner.Run("classified errors and generated id collisions", func(t *testing.T) {
		clock := NewScriptedClock()
		fixture := contractFixture(t, factory, clock)
		repo := fixture.Repo
		repoPath := filepath.Join(t.TempDir(), "repo")

		if _, err := repo.Read("missing-flow"); !flowstore.IsNotFound(err) {
			t.Fatalf("Read(missing) error = %v, want IsNotFound", err)
		}
		if err := repo.Delete("missing-flow"); !flowstore.IsNotFound(err) {
			t.Fatalf("Delete(missing) error = %v, want IsNotFound", err)
		}
		if _, err := repo.SetAutoMode(flowstore.AutoModeUpdate{FlowID: "missing-flow", Enabled: false}); !flowstore.IsNotFound(err) {
			t.Fatalf("SetAutoMode(missing) error = %v, want IsNotFound", err)
		}

		clock.At(30)
		first := createFlow(t, repo, flowstore.FlowRecord{
			Title:        "Collision Flow",
			Instructions: "first",
			RepoPath:     repoPath,
		})
		second := createFlow(t, repo, flowstore.FlowRecord{
			Title:        "Collision Flow",
			Instructions: "second",
			RepoPath:     repoPath,
		})
		if got, want := []string{first.FlowID, second.FlowID}, []string{"20260630T120030Z-collision-flow", "20260630T120030Z-collision-flow-2"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("generated flow IDs = %v, want %v", got, want)
		}

		flow := newDefaultFlow(t, repo, clock, "auto-launch-outdated")
		clock.At(31)
		flow = setAutoMode(t, repo, flowstore.AutoModeUpdate{FlowID: flow.FlowID, Enabled: false})
		if _, err := repo.AddPhaseLaunchID(flowstore.PhaseLaunchUpdate{
			FlowID:     flow.FlowID,
			PhaseID:    "plan",
			LaunchID:   "auto-launch-1",
			AutoLaunch: true,
		}); !flowstore.IsAutoLaunchOutdated(err) {
			t.Fatalf("AddPhaseLaunchID(stale auto launch) error = %v, want IsAutoLaunchOutdated", err)
		}
	})

	runner.Run("set phase validates transitions and plan review outcomes", func(t *testing.T) {
		clock := NewScriptedClock()
		fixture := contractFixture(t, factory, clock)
		repo := fixture.Repo
		record := newDefaultFlow(t, repo, clock, "phase-validation")

		before := readFlow(t, repo, record.FlowID)
		if _, err := repo.SetPhase(flowstore.PhaseUpdate{FlowID: record.FlowID, PhaseID: "plan", Status: flowstore.PhaseReady}); err == nil {
			t.Fatal("SetPhase(ready) error = nil, want derived readiness rejection")
		}
		assertPhaseStatusesEqual(t, readFlow(t, repo, record.FlowID), before)

		clock.At(2)
		record = setPhase(t, repo, flowstore.PhaseUpdate{FlowID: record.FlowID, PhaseID: "plan", Status: flowstore.PhaseCompleted})
		if got := phaseByID(t, record, "plan-review").Status; got != flowstore.PhaseReady {
			t.Fatalf("plan-review status = %q, want ready", got)
		}
		if _, err := repo.SetPhase(flowstore.PhaseUpdate{
			FlowID:  record.FlowID,
			PhaseID: "plan-review",
			Status:  flowstore.PhaseCompleted,
			Outcome: flowstore.OutcomeApprovedWithConcerns,
		}); err == nil {
			t.Fatal("approved_with_concerns without notes error = nil")
		}

		cases := []struct {
			name     string
			outcome  string
			status   string
			notes    string
			wantImpl string
			wantFlow string
		}{
			{"approved", flowstore.OutcomeApproved, flowstore.PhaseCompleted, "", flowstore.PhaseReady, flowstore.StatusInProgress},
			{"approved with concerns", flowstore.OutcomeApprovedWithConcerns, flowstore.PhaseCompleted, "minor concern", flowstore.PhaseReady, flowstore.StatusInProgress},
			{"changes requested", flowstore.OutcomeChangesRequested, flowstore.PhaseNeedsAttention, "revise plan", flowstore.PhasePending, flowstore.StatusNeedsAttention},
			{"blocked", flowstore.OutcomeBlocked, flowstore.PhaseBlocked, "waiting", flowstore.PhasePending, flowstore.StatusBlocked},
		}
		for i, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				clock := NewScriptedClock()
				fixture := contractFixture(t, factory, clock)
				repo := fixture.Repo
				record := newDefaultFlow(t, repo, clock, fmt.Sprintf("plan-review-%d", i))
				clock.At(2)
				record = setPhase(t, repo, flowstore.PhaseUpdate{FlowID: record.FlowID, PhaseID: "plan", Status: flowstore.PhaseCompleted})
				clock.At(3)
				record = setPhase(t, repo, flowstore.PhaseUpdate{
					FlowID:  record.FlowID,
					PhaseID: "plan-review",
					Status:  tc.status,
					Outcome: tc.outcome,
					Notes:   tc.notes,
				})
				if got := phaseByID(t, record, "plan-review").Outcome; got != tc.outcome {
					t.Fatalf("plan-review outcome = %q, want %q", got, tc.outcome)
				}
				if got := phaseByID(t, record, "implementation").Status; got != tc.wantImpl {
					t.Fatalf("implementation status = %q, want %q", got, tc.wantImpl)
				}
				if got := record.Status; got != tc.wantFlow {
					t.Fatalf("flow status = %q, want %q", got, tc.wantFlow)
				}
			})
		}
	})

	runner.Run("set phase syncs linked plan phases", func(t *testing.T) {
		clock := NewScriptedClock()
		fixture := contractFixture(t, factory, clock)
		repo := fixture.Repo
		if fixture.SeedPlanPhase == nil || fixture.PlanPhaseStatus == nil || fixture.BreakPlanMetadata == nil {
			t.Fatal("fixture must support linked plan phase sync assertions")
		}

		record := newDefaultFlow(t, repo, clock, "linked-plan-sync-success")
		planPath := fixture.SeedPlanPhase("linked-plan-success", "plan")
		clock.At(2)
		record = setPlanLink(t, repo, flowstore.PlanLinkUpdate{FlowID: record.FlowID, PlanID: "linked-plan-success", PlanPath: planPath})
		clock.At(3)
		record = setPhase(t, repo, flowstore.PhaseUpdate{FlowID: record.FlowID, PhaseID: "plan", Status: flowstore.PhaseCompleted})
		if got := phaseByID(t, record, "plan").Status; got != flowstore.PhaseCompleted {
			t.Fatalf("flow plan phase status = %q, want completed", got)
		}
		if got := fixture.PlanPhaseStatus("linked-plan-success", "plan"); got != flowstore.PhaseCompleted {
			t.Fatalf("linked plan phase status = %q, want completed", got)
		}

		failing := newDefaultFlow(t, repo, clock, "linked-plan-sync-failure")
		failingPlanPath := fixture.SeedPlanPhase("linked-plan-failure", "plan")
		clock.At(4)
		failing = setPlanLink(t, repo, flowstore.PlanLinkUpdate{FlowID: failing.FlowID, PlanID: "linked-plan-failure", PlanPath: failingPlanPath})
		fixture.BreakPlanMetadata("linked-plan-failure")
		clock.At(5)
		if _, err := repo.SetPhase(flowstore.PhaseUpdate{FlowID: failing.FlowID, PhaseID: "plan", Status: flowstore.PhaseCompleted}); err == nil {
			t.Fatal("SetPhase(completed with broken linked plan) error = nil")
		}
		failing = readFlow(t, repo, failing.FlowID)
		if phase := phaseByID(t, failing, "plan"); phase.Status != flowstore.PhaseNeedsAttention || phase.Outcome != "" {
			t.Fatalf("failed sync phase = (%s, %q), want (needs_attention, empty outcome): %#v", phase.Status, phase.Outcome, phase)
		}

		repeat := newDefaultFlow(t, repo, clock, "linked-plan-repeat-failure")
		repeatPlanPath := fixture.SeedPlanPhase("linked-plan-repeat", "plan")
		clock.At(6)
		repeat = setPlanLink(t, repo, flowstore.PlanLinkUpdate{FlowID: repeat.FlowID, PlanID: "linked-plan-repeat", PlanPath: repeatPlanPath})
		clock.At(7)
		repeat = setPhase(t, repo, flowstore.PhaseUpdate{FlowID: repeat.FlowID, PhaseID: "plan", Status: flowstore.PhaseCompleted})
		fixture.BreakPlanMetadata("linked-plan-repeat")
		clock.At(8)
		repeat = setPhase(t, repo, flowstore.PhaseUpdate{FlowID: repeat.FlowID, PhaseID: "plan", Status: flowstore.PhaseCompleted})
		if got := phaseByID(t, repeat, "plan").Status; got != flowstore.PhaseCompleted {
			t.Fatalf("repeat completed phase status = %q, want completed despite sync failure", got)
		}
	})

	runner.Run("derived status and readiness parity", func(t *testing.T) {
		clock := NewScriptedClock()
		fixture := contractFixture(t, factory, clock)
		repo := fixture.Repo
		record := newDefaultFlow(t, repo, clock, "derived")
		assertDerived(t, record)
		assertDerived(t, readFlow(t, repo, record.FlowID))

		clock.At(2)
		record = setPhase(t, repo, flowstore.PhaseUpdate{FlowID: record.FlowID, PhaseID: "plan", Status: flowstore.PhaseCompleted})
		assertDerived(t, record)
		if phase := phaseByID(t, record, "plan-review"); phase.Status != flowstore.PhaseReady || !phase.UpdatedAt.Equal(clock.Now()) {
			t.Fatalf("plan-review after plan completion = (%s, %s), want (ready, %s)", phase.Status, phase.UpdatedAt, clock.Now())
		}
		assertDerived(t, readFlow(t, repo, record.FlowID))
		listed, err := repo.List(flowstore.FlowFilter{RepoPath: record.RepoPath})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(listed) != 1 {
			t.Fatalf("List() len = %d, want 1", len(listed))
		}
		assertDerived(t, listed[0])

		clock.At(3)
		record = setPhase(t, repo, flowstore.PhaseUpdate{
			FlowID:  record.FlowID,
			PhaseID: "plan-review",
			Status:  flowstore.PhaseNeedsAttention,
			Outcome: flowstore.OutcomeChangesRequested,
			Notes:   "revise",
		})
		assertDerived(t, record)
		if got := phaseByID(t, record, "implementation").Status; got != flowstore.PhasePending {
			t.Fatalf("implementation status = %q, want pending after changes requested", got)
		}

		clock.At(4)
		record = restartPhase(t, repo, flowstore.PhaseRestartUpdate{FlowID: record.FlowID, PhaseID: "plan-review", Notes: "rerun"})
		assertDerived(t, record)
	})

	runner.Run("remaining mutator parity", func(t *testing.T) {
		clock := NewScriptedClock()
		fixture := contractFixture(t, factory, clock)
		repo := fixture.Repo

		record := newDefaultFlow(t, repo, clock, "mutators")
		if _, err := repo.RestartPhase(flowstore.PhaseRestartUpdate{FlowID: record.FlowID, PhaseID: "plan", Notes: "not recoverable"}); err == nil {
			t.Fatal("RestartPhase(ready) error = nil")
		}
		clock.At(2)
		record = setPhase(t, repo, flowstore.PhaseUpdate{FlowID: record.FlowID, PhaseID: "plan", Status: flowstore.PhaseNeedsAttention, Notes: "stuck"})
		clock.At(3)
		record = restartPhase(t, repo, flowstore.PhaseRestartUpdate{FlowID: record.FlowID, PhaseID: "plan", Notes: "try again"})
		if phase := phaseByID(t, record, "plan"); phase.Status != flowstore.PhaseRunning || phase.Notes != "try again" {
			t.Fatalf("RestartPhase plan = (%s, %q), want (running, try again)", phase.Status, phase.Notes)
		}

		clock.At(4)
		record = addChildPhase(t, repo, flowstore.ChildPhaseUpdate{FlowID: record.FlowID, ParentPhaseID: "implementation", PhaseID: "implementation-api", Title: "API", Order: 20})
		if _, err := repo.AddChildPhase(flowstore.ChildPhaseUpdate{FlowID: record.FlowID, ParentPhaseID: "implementation", PhaseID: "implementation-cli", Title: "CLI", Order: 0}); err == nil {
			t.Fatal("AddChildPhase(order 0) error = nil")
		}
		clock.At(5)
		record = addChildPhase(t, repo, flowstore.ChildPhaseUpdate{FlowID: record.FlowID, ParentPhaseID: "implementation", PhaseID: " Implementation-API ", Title: "API v2", Order: 10})
		children := phasesByID(record, "implementation-api")
		if len(children) != 1 || children[0].Title != "API v2" || children[0].Order != 10 {
			t.Fatalf("implementation child rows = %#v, want one updated child", children)
		}

		duplicateFlow := createFlow(t, repo, flowstore.FlowRecord{
			FlowID:       "duplicate-child",
			Title:        "Duplicate child",
			Instructions: "repair legacy duplicate phase rows",
			RepoPath:     filepath.Join(t.TempDir(), "repo"),
			Phases: []flowstore.FlowPhase{
				{PhaseID: "implementation", Title: "Implementation", Kind: "implementation", Status: flowstore.PhaseReady, Order: 1},
				{
					PhaseID:       "implementation-api",
					ParentPhaseID: "implementation",
					Title:         "API",
					Kind:          "implementation_child",
					Status:        flowstore.PhasePending,
					Order:         10,
					LaunchIDs:     []string{"launch-a"},
					Sessions:      []flowstore.Session{{Provider: "codex", SessionID: "session-a", LaunchID: "launch-a"}},
				},
				{
					PhaseID:       " Implementation-API ",
					ParentPhaseID: "implementation",
					Title:         "API stale",
					Kind:          "implementation_child",
					Status:        flowstore.PhasePending,
					Order:         20,
					LaunchIDs:     []string{"launch-b"},
					Sessions:      []flowstore.Session{{Provider: "codex", SessionID: "session-b", LaunchID: "launch-b"}},
					Notes:         "stale notes",
					Summary:       "stale summary",
				},
			},
		})
		clock.At(6)
		duplicateFlow = addChildPhase(t, repo, flowstore.ChildPhaseUpdate{
			FlowID:        duplicateFlow.FlowID,
			ParentPhaseID: "implementation",
			PhaseID:       "implementation-api",
			Title:         "API repaired",
			Order:         15,
		})
		duplicateChildren := phasesByNormalizedID(duplicateFlow, "implementation-api")
		if len(duplicateChildren) != 1 {
			t.Fatalf("normalized implementation-api rows = %d, want 1: %#v", len(duplicateChildren), duplicateChildren)
		}
		child := duplicateChildren[0]
		if child.Title != "API repaired" || child.Order != 15 {
			t.Fatalf("repaired child = (%q, %d), want (API repaired, 15)", child.Title, child.Order)
		}
		if got, want := child.LaunchIDs, []string{"launch-a", "launch-b"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("repaired child launch IDs = %v, want %v", got, want)
		}
		if got := len(child.Sessions); got != 2 {
			t.Fatalf("repaired child sessions len = %d, want 2: %#v", got, child.Sessions)
		}

		planPath := fixture.SeedPlan("plan-1")
		clock.At(7)
		record = setPlanLink(t, repo, flowstore.PlanLinkUpdate{FlowID: record.FlowID, PlanID: "plan-1", PlanPath: planPath})
		if record.PlanID != "plan-1" || record.PlanPath != planPath {
			t.Fatalf("plan link = (%q, %q), want (%q, %q)", record.PlanID, record.PlanPath, "plan-1", planPath)
		}
		if _, err := repo.SetPlanLink(flowstore.PlanLinkUpdate{FlowID: record.FlowID, PlanID: "../bad-plan"}); err == nil {
			t.Fatal("SetPlanLink(invalid plan id) error = nil")
		}
		if _, err := repo.SetPlanLink(flowstore.PlanLinkUpdate{FlowID: record.FlowID, PlanID: "missing-plan"}); err == nil {
			t.Fatal("SetPlanLink(missing) error = nil")
		}
		if fixture.SeedUnreadablePlan != nil {
			brokenPath := fixture.SeedUnreadablePlan("broken-plan")
			if _, err := repo.SetPlanLink(flowstore.PlanLinkUpdate{FlowID: record.FlowID, PlanID: "broken-plan", PlanPath: brokenPath}); err == nil {
				t.Fatal("SetPlanLink(unreadable seeded plan) error = nil")
			}
		}

		clock.At(8)
		record = setStartMetadata(t, repo, flowstore.StartMetadataUpdate{
			FlowID:       record.FlowID,
			WorktreePath: filepath.Join(t.TempDir(), "worktree"),
			Branch:       "flow/test-pr",
			BaseRef:      "main",
			Commit:       "abc123",
		})
		if record.Branch != "flow/test-pr" || record.BaseRef != "main" || record.Commit != "abc123" {
			t.Fatalf("start metadata = branch %q base %q commit %q", record.Branch, record.BaseRef, record.Commit)
		}
		if _, err := repo.SetStartMetadata(flowstore.StartMetadataUpdate{FlowID: record.FlowID, WorktreePath: "relative"}); err == nil {
			t.Fatal("SetStartMetadata(relative worktree) error = nil")
		}
		if _, err := repo.SetPR(flowstore.PRUpdate{
			FlowID:     record.FlowID,
			Provider:   "github",
			Number:     123,
			URL:        "https://github.com/brian-bell/flowstate/pull/123",
			HeadBranch: "wrong",
			BaseBranch: "main",
			Status:     "open",
		}); err == nil {
			t.Fatal("SetPR(mismatched head) error = nil")
		}
		clock.At(9)
		record = setPR(t, repo, flowstore.PRUpdate{
			FlowID:     record.FlowID,
			Provider:   "github",
			Number:     123,
			URL:        "https://github.com/brian-bell/flowstate/pull/123",
			HeadBranch: "flow/test-pr",
			BaseBranch: "main",
			Status:     "open",
		})
		if record.PR.Provider != "github" || record.PR.Number != 123 || record.PR.HeadBranch != "flow/test-pr" {
			t.Fatalf("PR metadata = %#v", record.PR)
		}

		clock.At(10)
		record = setAutoMode(t, repo, flowstore.AutoModeUpdate{FlowID: record.FlowID, Enabled: false})
		if record.AutoMode {
			t.Fatal("AutoMode = true, want false")
		}
		clock.At(11)
		record = setAutoMode(t, repo, flowstore.AutoModeUpdate{FlowID: record.FlowID, Enabled: true})
		if !record.AutoMode {
			t.Fatal("AutoMode = false, want true")
		}
		if _, err := repo.SetAutoMode(flowstore.AutoModeUpdate{FlowID: "missing-flow", Enabled: false}); err == nil {
			t.Fatal("SetAutoMode(missing flow) error = nil")
		}

		launchFlow := newDefaultFlow(t, repo, clock, "launch-reset")
		if _, err := repo.AddPhaseLaunchID(flowstore.PhaseLaunchUpdate{FlowID: launchFlow.FlowID, PhaseID: "plan"}); err == nil {
			t.Fatal("AddPhaseLaunchID(empty launch) error = nil")
		}
		if _, err := repo.ResetAwaitingSessionPhase(flowstore.PhaseResetUpdate{FlowID: launchFlow.FlowID, PhaseID: "plan"}); err == nil {
			t.Fatal("ResetAwaitingSessionPhase(ready phase) error = nil")
		}
		clock.At(12)
		launchFlow = addPhaseLaunchID(t, repo, flowstore.PhaseLaunchUpdate{FlowID: launchFlow.FlowID, PhaseID: "plan", LaunchID: "launch-1"})
		clock.At(13)
		launchFlow = addPhaseLaunchID(t, repo, flowstore.PhaseLaunchUpdate{FlowID: launchFlow.FlowID, PhaseID: "plan", LaunchID: "launch-1", Resume: true})
		clock.At(14)
		launchFlow = addPhaseLaunchID(t, repo, flowstore.PhaseLaunchUpdate{FlowID: launchFlow.FlowID, PhaseID: "plan", LaunchID: "launch-2", Resume: true})
		if got, want := phaseByID(t, launchFlow, "plan").LaunchIDs, []string{"launch-1", "launch-2"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("launch IDs = %v, want %v", got, want)
		}
		clock.At(15)
		launchFlow = resetAwaitingSessionPhase(t, repo, flowstore.PhaseResetUpdate{FlowID: launchFlow.FlowID, PhaseID: "plan"})
		planPhase := phaseByID(t, launchFlow, "plan")
		if planPhase.Status != flowstore.PhaseReady || len(planPhase.LaunchIDs) != 1 || planPhase.LaunchIDs[0] != "launch-1" {
			t.Fatalf("reset plan phase = %#v, want ready with launch-1 only", planPhase)
		}
	})

	runner.Run("set merge parity", func(t *testing.T) {
		clock := NewScriptedClock()
		fixture := contractFixture(t, factory, clock)
		repo := fixture.Repo
		repoPath := filepath.Join(t.TempDir(), "repo")
		mergedAt := clock.At(20)
		merged := createFlow(t, repo, flowstore.FlowRecord{
			FlowID:       "merge-completed",
			Title:        "Merge completed",
			Instructions: "merge",
			RepoPath:     repoPath,
			Branch:       "flow/test-pr",
			PR: flowstore.PullRequest{
				Provider:   "github",
				Number:     123,
				URL:        "https://github.com/brian-bell/flowstate/pull/123",
				HeadBranch: "flow/test-pr",
				BaseBranch: "main",
				Status:     "open",
			},
			Phases: []flowstore.FlowPhase{{PhaseID: "merge", Title: "Merge", Kind: "merge", Status: flowstore.PhaseCompleted, Order: 1}},
		})
		clock.At(21)
		merged = setMerge(t, repo, flowstore.MergeUpdate{FlowID: merged.FlowID, Status: flowstore.MergeMerged, Commit: "mergecommit", MergedAt: mergedAt})
		if merged.Merge.Status != flowstore.MergeMerged || merged.Merge.Commit != "mergecommit" || merged.Merge.MergedAt == nil || !merged.Merge.MergedAt.Equal(mergedAt) {
			t.Fatalf("merged metadata = %#v, want merged commit metadata", merged.Merge)
		}
		assertDerived(t, merged)

		blocked := createFlow(t, repo, flowstore.FlowRecord{
			FlowID:       "merge-blocked",
			Title:        "Merge blocked",
			Instructions: "merge blocked",
			RepoPath:     repoPath,
			Phases: []flowstore.FlowPhase{{
				PhaseID: "merge", Title: "Merge", Kind: "merge", Status: flowstore.PhaseBlocked, Notes: "conflict", Order: 1,
			}},
		})
		clock.At(22)
		blocked = setMerge(t, repo, flowstore.MergeUpdate{FlowID: blocked.FlowID, Status: flowstore.MergeBlocked})
		if blocked.Merge.Status != flowstore.MergeBlocked {
			t.Fatalf("blocked merge status = %q, want blocked", blocked.Merge.Status)
		}
		assertDerived(t, blocked)
		if _, err := repo.SetMerge(flowstore.MergeUpdate{FlowID: blocked.FlowID, Status: flowstore.MergeMerged, Commit: "x", MergedAt: clock.Now()}); err == nil {
			t.Fatal("SetMerge(merged without PR/completed merge) error = nil")
		}
	})

	runner.Run("attach session replaces by provider and session id", func(t *testing.T) {
		clock := NewScriptedClock()
		fixture := contractFixture(t, factory, clock)
		repo := fixture.Repo
		record := newDefaultFlow(t, repo, clock, "sessions")
		clock.At(2)
		record = addPhaseLaunchID(t, repo, flowstore.PhaseLaunchUpdate{FlowID: record.FlowID, PhaseID: "plan", LaunchID: "launch-1"})
		if _, err := repo.AttachSession(flowstore.SessionAttachUpdate{
			FlowID:  record.FlowID,
			PhaseID: "plan",
			Session: flowstore.Session{Provider: "codex"},
		}); err == nil {
			t.Fatal("AttachSession(missing session id) error = nil")
		}
		clock.At(3)
		record = attachSession(t, repo, flowstore.SessionAttachUpdate{
			FlowID:  record.FlowID,
			PhaseID: "plan",
			Session: flowstore.Session{Provider: "codex", SessionID: "session-1", LaunchID: "launch-1", Status: "running"},
		})
		clock.At(4)
		record = attachSession(t, repo, flowstore.SessionAttachUpdate{
			FlowID:  record.FlowID,
			PhaseID: "plan",
			Session: flowstore.Session{Provider: "codex", SessionID: "session-1", LaunchID: "launch-2", Status: "completed"},
		})
		phase := phaseByID(t, record, "plan")
		if len(phase.Sessions) != 1 || phase.Sessions[0].LaunchID != "launch-2" || phase.Sessions[0].Status != "completed" {
			t.Fatalf("sessions after replacement = %#v, want one overwritten session", phase.Sessions)
		}
		clock.At(5)
		record = attachSession(t, repo, flowstore.SessionAttachUpdate{
			FlowID:  record.FlowID,
			PhaseID: "plan",
			Session: flowstore.Session{Provider: "codex", SessionID: "session-2", LaunchID: "launch-2", Status: "running"},
		})
		phase = phaseByID(t, record, "plan")
		if len(phase.Sessions) != 2 {
			t.Fatalf("sessions len = %d, want 2: %#v", len(phase.Sessions), phase.Sessions)
		}
		if phase.Sessions[0].SessionID != "session-1" || phase.Sessions[1].SessionID != "session-2" {
			t.Fatalf("sessions order/identity = %#v", phase.Sessions)
		}
	})
}

func contractFixture(t testing.TB, factory Factory, clock *ScriptedClock) Fixture {
	t.Helper()
	fixture := factory(t, clock)
	if fixture.Repo == nil {
		t.Fatal("FlowRepository contract fixture returned nil Repo")
	}
	if fixture.SeedPlan == nil {
		t.Fatal("FlowRepository contract fixture must provide SeedPlan")
	}
	if fixture.SeedUnreadablePlan == nil {
		t.Fatal("FlowRepository contract fixture must provide SeedUnreadablePlan")
	}
	if fixture.SeedPlanPhase == nil {
		t.Fatal("FlowRepository contract fixture must provide SeedPlanPhase")
	}
	if fixture.PlanPhaseStatus == nil {
		t.Fatal("FlowRepository contract fixture must provide PlanPhaseStatus")
	}
	if fixture.BreakPlanMetadata == nil {
		t.Fatal("FlowRepository contract fixture must provide BreakPlanMetadata")
	}
	return fixture
}

func newDefaultFlow(t testing.TB, repo flowrepo.FlowRepository, clock *ScriptedClock, id string) flowstore.FlowRecord {
	t.Helper()
	clock.At(1)
	return createFlow(t, repo, flowstore.FlowRecord{
		FlowID:       id,
		Title:        id,
		Instructions: "contract",
		RepoPath:     filepath.Join(t.TempDir(), "repo"),
	})
}

func createFlow(t testing.TB, repo flowrepo.FlowRepository, record flowstore.FlowRecord) flowstore.FlowRecord {
	t.Helper()
	created, err := repo.Create(record)
	if err != nil {
		t.Fatalf("Create(%q) error = %v", record.FlowID, err)
	}
	return created
}

func readFlow(t testing.TB, repo flowrepo.FlowRepository, flowID string) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.Read(flowID)
	if err != nil {
		t.Fatalf("Read(%q) error = %v", flowID, err)
	}
	return record
}

func setPhase(t testing.TB, repo flowrepo.FlowRepository, update flowstore.PhaseUpdate) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.SetPhase(update)
	if err != nil {
		t.Fatalf("SetPhase(%+v) error = %v", update, err)
	}
	return record
}

func restartPhase(t testing.TB, repo flowrepo.FlowRepository, update flowstore.PhaseRestartUpdate) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.RestartPhase(update)
	if err != nil {
		t.Fatalf("RestartPhase(%+v) error = %v", update, err)
	}
	return record
}

func addChildPhase(t testing.TB, repo flowrepo.FlowRepository, update flowstore.ChildPhaseUpdate) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.AddChildPhase(update)
	if err != nil {
		t.Fatalf("AddChildPhase(%+v) error = %v", update, err)
	}
	return record
}

func setPlanLink(t testing.TB, repo flowrepo.FlowRepository, update flowstore.PlanLinkUpdate) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.SetPlanLink(update)
	if err != nil {
		t.Fatalf("SetPlanLink(%+v) error = %v", update, err)
	}
	return record
}

func setPR(t testing.TB, repo flowrepo.FlowRepository, update flowstore.PRUpdate) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.SetPR(update)
	if err != nil {
		t.Fatalf("SetPR(%+v) error = %v", update, err)
	}
	return record
}

func setMerge(t testing.TB, repo flowrepo.FlowRepository, update flowstore.MergeUpdate) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.SetMerge(update)
	if err != nil {
		t.Fatalf("SetMerge(%+v) error = %v", update, err)
	}
	return record
}

func setAutoMode(t testing.TB, repo flowrepo.FlowRepository, update flowstore.AutoModeUpdate) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.SetAutoMode(update)
	if err != nil {
		t.Fatalf("SetAutoMode(%+v) error = %v", update, err)
	}
	return record
}

func setStartMetadata(t testing.TB, repo flowrepo.FlowRepository, update flowstore.StartMetadataUpdate) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.SetStartMetadata(update)
	if err != nil {
		t.Fatalf("SetStartMetadata(%+v) error = %v", update, err)
	}
	return record
}

func addPhaseLaunchID(t testing.TB, repo flowrepo.FlowRepository, update flowstore.PhaseLaunchUpdate) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.AddPhaseLaunchID(update)
	if err != nil {
		t.Fatalf("AddPhaseLaunchID(%+v) error = %v", update, err)
	}
	return record
}

func resetAwaitingSessionPhase(t testing.TB, repo flowrepo.FlowRepository, update flowstore.PhaseResetUpdate) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.ResetAwaitingSessionPhase(update)
	if err != nil {
		t.Fatalf("ResetAwaitingSessionPhase(%+v) error = %v", update, err)
	}
	return record
}

func attachSession(t testing.TB, repo flowrepo.FlowRepository, update flowstore.SessionAttachUpdate) flowstore.FlowRecord {
	t.Helper()
	record, err := repo.AttachSession(update)
	if err != nil {
		t.Fatalf("AttachSession(%+v) error = %v", update, err)
	}
	return record
}

func assertDerived(t testing.TB, record flowstore.FlowRecord) {
	t.Helper()
	if got, want := record.Status, flowstore.DeriveStatus(record); got != want {
		t.Fatalf("record.Status = %q, want DeriveStatus %q", got, want)
	}
	expected := flowstore.RefreshPhaseReadiness(record, record.UpdatedAt)
	for i, phase := range record.Phases {
		if i >= len(expected.Phases) {
			t.Fatalf("record has extra phase at %d: %#v", i, phase)
		}
		want := expected.Phases[i]
		if phase.PhaseID != want.PhaseID || phase.Status != want.Status || !phase.UpdatedAt.Equal(want.UpdatedAt) {
			t.Fatalf("phase[%d] = (%q, %q, %s), want (%q, %q, %s)", i, phase.PhaseID, phase.Status, phase.UpdatedAt, want.PhaseID, want.Status, want.UpdatedAt)
		}
	}
}

func assertPhaseStatusesEqual(t testing.TB, got, want flowstore.FlowRecord) {
	t.Helper()
	if len(got.Phases) != len(want.Phases) {
		t.Fatalf("phase count = %d, want %d", len(got.Phases), len(want.Phases))
	}
	for i := range got.Phases {
		if got.Phases[i].PhaseID != want.Phases[i].PhaseID || got.Phases[i].Status != want.Phases[i].Status {
			t.Fatalf("phase[%d] = (%q, %q), want (%q, %q)", i, got.Phases[i].PhaseID, got.Phases[i].Status, want.Phases[i].PhaseID, want.Phases[i].Status)
		}
	}
}

func phaseByID(t testing.TB, record flowstore.FlowRecord, phaseID string) flowstore.FlowPhase {
	t.Helper()
	for _, phase := range record.Phases {
		if phase.PhaseID == phaseID {
			return phase
		}
	}
	t.Fatalf("phase %q not found in %#v", phaseID, record.Phases)
	return flowstore.FlowPhase{}
}

func phasesByID(record flowstore.FlowRecord, phaseID string) []flowstore.FlowPhase {
	var phases []flowstore.FlowPhase
	for _, phase := range record.Phases {
		if phase.PhaseID == phaseID {
			phases = append(phases, phase)
		}
	}
	return phases
}

func phasesByNormalizedID(record flowstore.FlowRecord, phaseID string) []flowstore.FlowPhase {
	var phases []flowstore.FlowPhase
	want := artifacts.NormalizePhaseID(phaseID)
	for _, phase := range record.Phases {
		if artifacts.NormalizePhaseID(phase.PhaseID) == want {
			phases = append(phases, phase)
		}
	}
	return phases
}

func flowIDs(records []flowstore.FlowRecord) []string {
	ids := make([]string, len(records))
	for i, record := range records {
		ids[i] = record.FlowID
	}
	return ids
}
