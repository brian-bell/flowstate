package model

import (
	"fmt"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/flowstart"
)

const flowPlanPhaseID = "plan"

// FlowStartRequest contains the user operation inputs needed to create a Flow
// and optionally prepare the initial plan-phase agent launch.
type FlowStartRequest struct {
	RepoPath            string
	Title               string
	Instructions        string
	BaseRef             string
	AgentCommand        string
	ReasoningEffort     string
	SessionStateRoot    string
	FlowPromptTemplates FlowPromptTemplates
	// FlowPromptTemplatesProvided forces StartPlan to use FlowPromptTemplates
	// even when every template has been reset to the built-in default.
	FlowPromptTemplatesProvided bool
	PlanPhaseID                 string
	PlanPhaseTitle              string
	PlanPhaseStatus             string
}

// FlowStartResult is the prepared or launch-ready result of creating a new Flow.
type FlowStartResult struct {
	Flow           flowstore.FlowRecord
	Worktree       actions.FlowWorktreeCreateResult
	Commit         string
	LaunchID       string
	LaunchContext  actions.AgentLaunchContext
	DaemonLaunched bool
}

// FlowStarterOptions groups the deeper orchestration adapters for starting a
// Flow. Tests can replace these directly without widening Model.Options.
type FlowStarterOptions struct {
	CreateFlow           func(flowstore.FlowRecord) (flowstore.FlowRecord, error)
	CreateWorktree       func(repoPath, title, baseRef string) (actions.FlowWorktreeCreateResult, error)
	SetStartMetadata     func(flowstore.StartMetadataUpdate) (flowstore.FlowRecord, error)
	SetPhase             func(flowstore.PhaseUpdate) (flowstore.FlowRecord, error)
	AddPhaseLaunchID     func(flowstore.PhaseLaunchUpdate) (flowstore.FlowRecord, error)
	BootstrapHookForRepo func(string) (actions.BootstrapHook, bool)
	RunBootstrapHook     func(actions.BootstrapContext, actions.BootstrapHook) error
	ResolveCommit        func(string) string
	NewLaunchID          func() string
	FlowPromptTemplates  FlowPromptTemplates
}

// FlowStarter owns the persistence, worktree, bootstrap, and recovery sequence
// for the initial Flow plan phase.
type FlowStarter struct {
	createFlow           func(flowstore.FlowRecord) (flowstore.FlowRecord, error)
	createWorktree       func(repoPath, title, baseRef string) (actions.FlowWorktreeCreateResult, error)
	setStartMetadata     func(flowstore.StartMetadataUpdate) (flowstore.FlowRecord, error)
	setPhase             func(flowstore.PhaseUpdate) (flowstore.FlowRecord, error)
	addPhaseLaunchID     func(flowstore.PhaseLaunchUpdate) (flowstore.FlowRecord, error)
	bootstrapHookForRepo func(string) (actions.BootstrapHook, bool)
	runBootstrapHook     func(actions.BootstrapContext, actions.BootstrapHook) error
	resolveCommit        func(string) string
	newLaunchID          func() string
	flowPromptTemplates  FlowPromptTemplates
}

func NewFlowStarter(opts FlowStarterOptions) FlowStarter {
	starter := FlowStarter{
		createFlow:           opts.CreateFlow,
		createWorktree:       opts.CreateWorktree,
		setStartMetadata:     opts.SetStartMetadata,
		setPhase:             opts.SetPhase,
		addPhaseLaunchID:     opts.AddPhaseLaunchID,
		bootstrapHookForRepo: opts.BootstrapHookForRepo,
		runBootstrapHook:     opts.RunBootstrapHook,
		resolveCommit:        opts.ResolveCommit,
		newLaunchID:          opts.NewLaunchID,
		flowPromptTemplates:  opts.FlowPromptTemplates,
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
	if starter.addPhaseLaunchID == nil {
		starter.addPhaseLaunchID = func(update flowstore.PhaseLaunchUpdate) (flowstore.FlowRecord, error) {
			return flowstore.FlowRecord{FlowID: update.FlowID}, nil
		}
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
	if starter.newLaunchID == nil {
		starter.newLaunchID = newLaunchID
	}
	return starter
}

func (s FlowStarter) StartPlan(req FlowStartRequest) (FlowStartResult, error) {
	phaseID := req.PlanPhaseID
	if phaseID == "" {
		phaseID = flowPlanPhaseID
	}

	result, err := s.PrepareFlow(req)
	if err != nil {
		return result, err
	}
	flow := result.Flow
	worktree := result.Worktree
	commit := result.Commit

	launchID := s.newLaunchID()
	launchedFlow, err := s.addPhaseLaunchID(flowstore.PhaseLaunchUpdate{
		FlowID:   flow.FlowID,
		PhaseID:  phaseID,
		LaunchID: launchID,
	})
	if err != nil {
		return result, err
	}
	flow = launchedFlow
	result.Flow = flow
	result.LaunchID = launchID

	phaseTitle := req.PlanPhaseTitle
	if phaseTitle == "" {
		phaseTitle = "Plan"
	}
	phaseStatus := req.PlanPhaseStatus
	if phaseStatus == "" {
		phaseStatus = flowstore.PhaseRunning
	}
	result.LaunchContext = actions.AgentLaunchContext{
		Command:          req.AgentCommand,
		ReasoningEffort:  req.ReasoningEffort,
		LaunchID:         launchID,
		RepoPath:         req.RepoPath,
		WorktreePath:     worktree.WorktreePath,
		Branch:           worktree.Branch,
		Commit:           commit,
		SessionStateRoot: req.SessionStateRoot,
		PlanPhaseID:      phaseID,
		PlanPhaseTitle:   phaseTitle,
		PlanPhaseStatus:  phaseStatus,
		FlowID:           flow.FlowID,
		FlowPhaseID:      phaseID,
		InitialPrompt:    flowPlanPrompt(flowStartPromptRecord(flow, req, worktree, commit), s.promptTemplatesForRequest(req)),
	}
	return result, nil
}

func (s FlowStarter) promptTemplatesForRequest(req FlowStartRequest) FlowPromptTemplates {
	if req.FlowPromptTemplatesProvided || req.FlowPromptTemplates != (FlowPromptTemplates{}) {
		return req.FlowPromptTemplates
	}
	return s.flowPromptTemplates
}

func (s FlowStarter) PrepareFlow(req FlowStartRequest) (FlowStartResult, error) {
	prepared, err := s.prepareParkedFlow(req)
	result := FlowStartResult{
		Flow:     prepared.Flow,
		Worktree: prepared.Worktree,
		Commit:   prepared.Commit,
	}
	if err != nil {
		return result, err
	}
	if prepared.Blocked {
		return result, fmt.Errorf("%s", prepared.BlockedMessage)
	}
	return result, nil
}

func (s FlowStarter) prepareParkedFlow(req FlowStartRequest) (flowstart.Result, error) {
	return flowstart.NewStarter(flowstart.Options{
		CreateFlow:           s.createFlow,
		CreateWorktree:       s.createWorktree,
		SetStartMetadata:     s.setStartMetadata,
		SetPhase:             s.setPhase,
		BootstrapHookForRepo: s.bootstrapHookForRepo,
		RunBootstrapHook:     s.runBootstrapHook,
		ResolveCommit:        s.resolveCommit,
	}).Prepare(flowstart.Request{
		RepoPath:     req.RepoPath,
		Title:        req.Title,
		Instructions: req.Instructions,
		BaseRef:      req.BaseRef,
		PlanPhaseID:  req.PlanPhaseID,
	})
}

func flowStartPromptRecord(flow flowstore.FlowRecord, req FlowStartRequest, worktree actions.FlowWorktreeCreateResult, commit string) flowstore.FlowRecord {
	if flow.Title == "" {
		flow.Title = req.Title
	}
	if flow.Instructions == "" {
		flow.Instructions = req.Instructions
	}
	if flow.RepoPath == "" {
		flow.RepoPath = req.RepoPath
	}
	if flow.WorktreePath == "" {
		flow.WorktreePath = worktree.WorktreePath
	}
	if flow.Branch == "" {
		flow.Branch = worktree.Branch
	}
	if flow.BaseRef == "" {
		flow.BaseRef = req.BaseRef
	}
	if flow.Commit == "" {
		flow.Commit = commit
	}
	return flow
}
