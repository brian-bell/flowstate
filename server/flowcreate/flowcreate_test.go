package flowcreate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/server/flowcreate"
	"github.com/brian-bell/flowstate/server/graph"
)

func TestCreatorReturnsBlockedFlowForWorktreeFailure(t *testing.T) {
	store := &fakeCreateStore{}
	creator := flowcreate.New(flowcreate.Options{
		Store: store,
		CreateWorktree: func(string, string, string) (actions.FlowWorktreeCreateResult, error) {
			return actions.FlowWorktreeCreateResult{}, errors.New("branch exists")
		},
	})

	record, err := creator.CreateFlow(context.Background(), graph.CreateFlowInput{
		RepoPath:     "/dev/alpha",
		Title:        "Blocked Flow",
		Instructions: "create through server",
	})
	if err != nil {
		t.Fatalf("CreateFlow() error = %v, want blocked flow", err)
	}
	if len(record.Phases) != 1 ||
		record.Phases[0].Status != flowstore.PhaseBlocked ||
		!strings.Contains(record.Phases[0].Notes, "Worktree creation failed: branch exists") {
		t.Fatalf("record = %#v, want blocked plan phase", record)
	}
}

func TestCreatorRunsConfiguredBootstrapHook(t *testing.T) {
	store := &fakeCreateStore{}
	var gotCtx actions.BootstrapContext
	var gotHook actions.BootstrapHook
	creator := flowcreate.New(flowcreate.Options{
		Store: store,
		CreateWorktree: func(string, string, string) (actions.FlowWorktreeCreateResult, error) {
			return actions.FlowWorktreeCreateResult{WorktreePath: "/dev/alpha-worktrees/flow-created", Branch: "flow/created"}, nil
		},
		ResolveCommit: func(string) string { return "abc123" },
		BootstrapHookForRepo: func(repoPath string) (actions.BootstrapHook, bool) {
			if repoPath != "/dev/alpha" {
				t.Fatalf("BootstrapHookForRepo(%q), want /dev/alpha", repoPath)
			}
			return actions.BootstrapHook{Script: ".flowstate/bootstrap", TimeoutSeconds: 7}, true
		},
		RunBootstrapHook: func(ctx actions.BootstrapContext, hook actions.BootstrapHook) error {
			gotCtx = ctx
			gotHook = hook
			return nil
		},
	})

	_, err := creator.CreateFlow(context.Background(), graph.CreateFlowInput{
		RepoPath:     " /dev/alpha ",
		Title:        "Created Flow",
		Instructions: "create through server",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreateFlow() error = %v", err)
	}
	if gotCtx.RepoPath != "/dev/alpha" ||
		gotCtx.WorktreePath != "/dev/alpha-worktrees/flow-created" ||
		gotCtx.Ref != "flow/created" ||
		gotCtx.Kind != actions.WorktreeCreateFlow ||
		gotHook.Script != ".flowstate/bootstrap" ||
		gotHook.TimeoutSeconds != 7 {
		t.Fatalf("bootstrap ctx = %#v hook = %#v", gotCtx, gotHook)
	}
}

type fakeCreateStore struct{}

func (s *fakeCreateStore) Create(record flowstore.FlowRecord) (flowstore.FlowRecord, error) {
	record.FlowID = "flow-1"
	record.Phases = []flowstore.FlowPhase{{PhaseID: "plan", Title: "Plan", Status: flowstore.PhaseReady}}
	return record, nil
}

func (s *fakeCreateStore) SetStartMetadata(update flowstore.StartMetadataUpdate) (flowstore.FlowRecord, error) {
	return flowstore.FlowRecord{
		FlowID:       update.FlowID,
		Title:        "Created Flow",
		Instructions: "create through server",
		RepoPath:     "/dev/alpha",
		WorktreePath: update.WorktreePath,
		Branch:       update.Branch,
		BaseRef:      update.BaseRef,
		Commit:       update.Commit,
		Phases:       []flowstore.FlowPhase{{PhaseID: "plan", Title: "Plan", Status: flowstore.PhaseReady}},
	}, nil
}

func (s *fakeCreateStore) SetPhase(update flowstore.PhaseUpdate) (flowstore.FlowRecord, error) {
	return flowstore.FlowRecord{
		FlowID: "flow-1",
		Phases: []flowstore.FlowPhase{{
			PhaseID: update.PhaseID,
			Status:  update.Status,
			Notes:   update.Notes,
		}},
	}, nil
}
