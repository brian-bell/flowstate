package flowcreate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/server/flowcreate"
	"github.com/brian-bell/flowstate/server/graph"
)

func TestCreatorRunsBootstrapHookWithFlowWorktreeContext(t *testing.T) {
	store := newStore(t)
	var gotCtx actions.BootstrapContext
	var gotHook actions.BootstrapHook

	creator := flowcreate.New(flowcreate.Options{
		Store: store,
		CreateWorktree: func(repoPath, title, baseRef string) (actions.FlowWorktreeCreateResult, error) {
			if repoPath != "/dev/alpha" || title != "Parked Flow" || baseRef != "main" {
				t.Fatalf("CreateWorktree(%q, %q, %q)", repoPath, title, baseRef)
			}
			return actions.FlowWorktreeCreateResult{WorktreePath: "/dev/alpha-worktrees/flow-parked-flow", Branch: "flow/parked-flow"}, nil
		},
		ResolveCommit: func(path string) string {
			if path != "/dev/alpha-worktrees/flow-parked-flow" {
				t.Fatalf("ResolveCommit(%q)", path)
			}
			return "abc123"
		},
		BootstrapHookForRepo: func(repoPath string) (actions.BootstrapHook, bool) {
			if repoPath != "/dev/alpha" {
				t.Fatalf("BootstrapHookForRepo(%q)", repoPath)
			}
			return actions.BootstrapHook{Script: ".flowstate/bootstrap", TimeoutSeconds: 11}, true
		},
		RunBootstrapHook: func(ctx actions.BootstrapContext, hook actions.BootstrapHook) error {
			gotCtx = ctx
			gotHook = hook
			return nil
		},
	})

	record, err := creator.CreateFlow(context.Background(), graph.CreateFlowInput{
		RepoPath:     " /dev/alpha ",
		Title:        " Parked Flow ",
		Instructions: " Build it ",
		BaseRef:      " main ",
	})
	if err != nil {
		t.Fatalf("CreateFlow returned error: %v", err)
	}
	if record.RepoPath != "/dev/alpha" ||
		record.Title != "Parked Flow" ||
		record.Instructions != "Build it" ||
		record.WorktreePath != "/dev/alpha-worktrees/flow-parked-flow" ||
		record.Branch != "flow/parked-flow" ||
		record.BaseRef != "main" ||
		record.Commit != "abc123" {
		t.Fatalf("record = %#v", record)
	}
	if gotCtx.RepoPath != "/dev/alpha" ||
		gotCtx.WorktreePath != "/dev/alpha-worktrees/flow-parked-flow" ||
		gotCtx.Ref != "flow/parked-flow" ||
		gotCtx.Kind != actions.WorktreeCreateFlow {
		t.Fatalf("bootstrap context = %#v", gotCtx)
	}
	if gotHook.Script != ".flowstate/bootstrap" || gotHook.TimeoutSeconds != 11 {
		t.Fatalf("bootstrap hook = %#v", gotHook)
	}
}

func TestCreatorReturnsBlockedFlowForBootstrapFailure(t *testing.T) {
	store := newStore(t)
	creator := flowcreate.New(flowcreate.Options{
		Store: store,
		CreateWorktree: func(string, string, string) (actions.FlowWorktreeCreateResult, error) {
			return actions.FlowWorktreeCreateResult{WorktreePath: "/dev/alpha-worktrees/flow-parked-flow", Branch: "flow/parked-flow"}, nil
		},
		ResolveCommit: func(string) string {
			return "abc123"
		},
		BootstrapHookForRepo: func(string) (actions.BootstrapHook, bool) {
			return actions.BootstrapHook{Script: ".flowstate/bootstrap"}, true
		},
		RunBootstrapHook: func(actions.BootstrapContext, actions.BootstrapHook) error {
			return errors.New("missing env file")
		},
	})

	record, err := creator.CreateFlow(context.Background(), graph.CreateFlowInput{
		RepoPath:     "/dev/alpha",
		Title:        "Parked Flow",
		Instructions: "Build it",
	})
	if err != nil {
		t.Fatalf("CreateFlow returned error: %v", err)
	}
	if len(record.Phases) == 0 ||
		record.Phases[0].PhaseID != "plan" ||
		record.Phases[0].Status != flowstore.PhaseBlocked ||
		!strings.Contains(record.Phases[0].Notes, "Bootstrap hook failed: missing env file") {
		t.Fatalf("record phases = %#v", record.Phases)
	}
	reread, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read blocked flow: %v", err)
	}
	if reread.Phases[0].Status != flowstore.PhaseBlocked {
		t.Fatalf("reread plan status = %q, want blocked", reread.Phases[0].Status)
	}
}

func TestCreatorReturnsErrorWhenBlockedPhasePersistenceFails(t *testing.T) {
	store := failingSetPhaseStore{Store: newStore(t)}
	creator := flowcreate.New(flowcreate.Options{
		Store: store,
		CreateWorktree: func(string, string, string) (actions.FlowWorktreeCreateResult, error) {
			return actions.FlowWorktreeCreateResult{}, errors.New("branch exists")
		},
	})

	_, err := creator.CreateFlow(context.Background(), graph.CreateFlowInput{
		RepoPath:     "/dev/alpha",
		Title:        "Parked Flow",
		Instructions: "Build it",
	})
	if err == nil {
		t.Fatal("CreateFlow returned nil error, want blocked phase persistence failure")
	}
	if !strings.Contains(err.Error(), "branch exists") || !strings.Contains(err.Error(), "mark flow blocked: disk full") {
		t.Fatalf("error = %q", err)
	}
}

type failingSetPhaseStore struct {
	*flowstore.Store
}

func (s failingSetPhaseStore) SetPhase(flowstore.PhaseUpdate) (flowstore.FlowRecord, error) {
	return flowstore.FlowRecord{}, errors.New("disk full")
}

func newStore(t *testing.T) *flowstore.Store {
	t.Helper()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	store, err := flowstore.NewStore(flowstore.StoreOptions{
		Root: t.TempDir(),
		Now: func() time.Time {
			now = now.Add(time.Minute)
			return now
		},
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}
