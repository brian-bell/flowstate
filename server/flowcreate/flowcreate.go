package flowcreate

import (
	"context"
	"fmt"
	"strings"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/model"
	"github.com/brian-bell/flowstate/server/graph"
)

type Store interface {
	Create(flowstore.FlowRecord) (flowstore.FlowRecord, error)
	SetStartMetadata(flowstore.StartMetadataUpdate) (flowstore.FlowRecord, error)
	SetPhase(flowstore.PhaseUpdate) (flowstore.FlowRecord, error)
}

type Options struct {
	Store                Store
	CreateWorktree       func(repoPath, title, baseRef string) (actions.FlowWorktreeCreateResult, error)
	BootstrapHookForRepo func(string) (actions.BootstrapHook, bool)
	RunBootstrapHook     func(actions.BootstrapContext, actions.BootstrapHook) error
	ResolveCommit        func(string) string
}

type Creator struct {
	store                Store
	createWorktree       func(repoPath, title, baseRef string) (actions.FlowWorktreeCreateResult, error)
	bootstrapHookForRepo func(string) (actions.BootstrapHook, bool)
	runBootstrapHook     func(actions.BootstrapContext, actions.BootstrapHook) error
	resolveCommit        func(string) string
}

func New(opts Options) Creator {
	return Creator{
		store:                opts.Store,
		createWorktree:       opts.CreateWorktree,
		bootstrapHookForRepo: opts.BootstrapHookForRepo,
		runBootstrapHook:     opts.RunBootstrapHook,
		resolveCommit:        opts.ResolveCommit,
	}
}

func (c Creator) CreateFlow(ctx context.Context, input graph.CreateFlowInput) (flowstore.FlowRecord, error) {
	_ = ctx
	if c.store == nil {
		return flowstore.FlowRecord{}, fmt.Errorf("flow creator missing Store")
	}
	starter := model.NewFlowStarter(model.FlowStarterOptions{
		CreateFlow:           c.store.Create,
		CreateWorktree:       c.createWorktree,
		SetStartMetadata:     c.store.SetStartMetadata,
		SetPhase:             c.store.SetPhase,
		BootstrapHookForRepo: c.bootstrapHookForRepo,
		RunBootstrapHook:     c.runBootstrapHook,
		ResolveCommit:        c.resolveCommit,
	})
	result, err := starter.PrepareFlow(model.FlowStartRequest{
		RepoPath:     strings.TrimSpace(input.RepoPath),
		Title:        strings.TrimSpace(input.Title),
		Instructions: strings.TrimSpace(input.Instructions),
		BaseRef:      strings.TrimSpace(input.BaseRef),
	})
	if err != nil {
		return flowstore.FlowRecord{}, err
	}
	return result.Flow, nil
}
