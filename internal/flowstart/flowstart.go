package flowstart

import (
	"fmt"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/flowstore"
)

const defaultPlanPhaseID = "plan"

type Request struct {
	RepoPath     string
	Title        string
	Instructions string
	BaseRef      string
	PlanPhaseID  string
}

type Result struct {
	Flow           flowstore.FlowRecord
	Worktree       actions.FlowWorktreeCreateResult
	Commit         string
	Blocked        bool
	BlockedMessage string
}

type Options struct {
	CreateFlow           func(flowstore.FlowRecord) (flowstore.FlowRecord, error)
	CreateWorktree       func(repoPath, title, baseRef string) (actions.FlowWorktreeCreateResult, error)
	SetStartMetadata     func(flowstore.StartMetadataUpdate) (flowstore.FlowRecord, error)
	SetPhase             func(flowstore.PhaseUpdate) (flowstore.FlowRecord, error)
	BootstrapHookForRepo func(string) (actions.BootstrapHook, bool)
	RunBootstrapHook     func(actions.BootstrapContext, actions.BootstrapHook) error
	ResolveCommit        func(string) string
}

type Starter struct {
	createFlow           func(flowstore.FlowRecord) (flowstore.FlowRecord, error)
	createWorktree       func(repoPath, title, baseRef string) (actions.FlowWorktreeCreateResult, error)
	setStartMetadata     func(flowstore.StartMetadataUpdate) (flowstore.FlowRecord, error)
	setPhase             func(flowstore.PhaseUpdate) (flowstore.FlowRecord, error)
	bootstrapHookForRepo func(string) (actions.BootstrapHook, bool)
	runBootstrapHook     func(actions.BootstrapContext, actions.BootstrapHook) error
	resolveCommit        func(string) string
}

func NewStarter(opts Options) Starter {
	starter := Starter{
		createFlow:           opts.CreateFlow,
		createWorktree:       opts.CreateWorktree,
		setStartMetadata:     opts.SetStartMetadata,
		setPhase:             opts.SetPhase,
		bootstrapHookForRepo: opts.BootstrapHookForRepo,
		runBootstrapHook:     opts.RunBootstrapHook,
		resolveCommit:        opts.ResolveCommit,
	}
	if starter.createFlow == nil {
		starter.createFlow = func(flowstore.FlowRecord) (flowstore.FlowRecord, error) {
			return flowstore.FlowRecord{}, fmt.Errorf("flow starter missing CreateFlow")
		}
	}
	if starter.createWorktree == nil {
		starter.createWorktree = actions.CreateFlowWorktree
	}
	if starter.setStartMetadata == nil {
		starter.setStartMetadata = func(update flowstore.StartMetadataUpdate) (flowstore.FlowRecord, error) {
			return flowstore.FlowRecord{FlowID: update.FlowID}, nil
		}
	}
	if starter.setPhase == nil {
		starter.setPhase = func(flowstore.PhaseUpdate) (flowstore.FlowRecord, error) { return flowstore.FlowRecord{}, nil }
	}
	if starter.bootstrapHookForRepo == nil {
		starter.bootstrapHookForRepo = func(string) (actions.BootstrapHook, bool) { return actions.BootstrapHook{}, false }
	}
	if starter.runBootstrapHook == nil {
		starter.runBootstrapHook = actions.RunBootstrapHook
	}
	if starter.resolveCommit == nil {
		starter.resolveCommit = actions.ResolveWorktreeCommit
	}
	return starter
}

func (s Starter) Prepare(req Request) (Result, error) {
	phaseID := req.PlanPhaseID
	if phaseID == "" {
		phaseID = defaultPlanPhaseID
	}

	flow, err := s.createFlow(flowstore.FlowRecord{
		Title:        req.Title,
		Instructions: req.Instructions,
		RepoPath:     req.RepoPath,
		BaseRef:      req.BaseRef,
	})
	if err != nil {
		return Result{}, err
	}
	result := Result{Flow: flow}

	worktree, err := s.createWorktree(req.RepoPath, req.Title, req.BaseRef)
	if err != nil {
		return s.blockPlanPhase(result, phaseID, "Worktree creation failed: "+err.Error(), err.Error())
	}
	result.Worktree = worktree

	commit := s.resolveCommit(worktree.WorktreePath)
	result.Commit = commit
	startedFlow, err := s.setStartMetadata(flowstore.StartMetadataUpdate{
		FlowID:       flow.FlowID,
		WorktreePath: worktree.WorktreePath,
		Branch:       worktree.Branch,
		BaseRef:      req.BaseRef,
		Commit:       commit,
	})
	if err != nil {
		return result, err
	}
	result.Flow = startedFlow

	if err := s.runBootstrap(req.RepoPath, worktree); err != nil {
		errText := "Bootstrap hook failed: " + err.Error()
		return s.blockPlanPhase(result, phaseID, errText, errText)
	}

	return result, nil
}

func (s Starter) runBootstrap(repoPath string, worktree actions.FlowWorktreeCreateResult) error {
	hook, ok := s.bootstrapHookForRepo(repoPath)
	if !ok {
		return nil
	}
	return s.runBootstrapHook(actions.BootstrapContext{
		RepoPath:     repoPath,
		WorktreePath: worktree.WorktreePath,
		Ref:          worktree.Branch,
		Kind:         actions.WorktreeCreateFlow,
	}, hook)
}

func (s Starter) blockPlanPhase(result Result, phaseID, notes, resultErr string) (Result, error) {
	blockedFlow, err := s.setPhase(flowstore.PhaseUpdate{
		FlowID:  result.Flow.FlowID,
		PhaseID: phaseID,
		Status:  flowstore.PhaseBlocked,
		Notes:   notes,
	})
	if err != nil {
		return result, fmt.Errorf("%s; mark flow blocked: %v", resultErr, err)
	}
	result.Flow = blockedFlow
	result.Blocked = true
	result.BlockedMessage = resultErr
	return result, nil
}
