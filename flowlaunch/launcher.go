package flowlaunch

import (
	"fmt"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/agent"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/artifacts"
)

type Route int

const (
	RouteExternal Route = iota
	RouteEmbedded
)

type Request struct {
	Record        flowstore.FlowRecord
	Phase         flowstore.FlowPhase
	AutoLaunch    bool
	Headless      bool
	RejectRunning bool
}

type PreparedRequest struct {
	Request
	RepoPath     string
	WorktreePath string
	PlanPath     string
	LaunchID     string
}

type Result struct {
	Context actions.AgentLaunchContext
	Route   Route
	Skipped bool
}

type ValidationError struct {
	Message string
}

func (err ValidationError) Error() string {
	return err.Message
}

type Launcher struct {
	CurrentRepoPath  func() (string, bool)
	PlanMarkdownPath func(string) (string, error)
	ReadPlan         func(string) (string, error)
	AddPhaseLaunchID func(flowstore.PhaseLaunchUpdate) (flowstore.FlowRecord, error)
	NewLaunchID      func() string
	SessionStateRoot string
	AgentCommand     string
	ReasoningEffort  string
	Templates        PromptTemplates
}

func (l Launcher) Preflight(req Request) (PreparedRequest, error) {
	phaseID := artifacts.NormalizePhaseID(req.Phase.PhaseID)
	if agent.Normalize(l.AgentCommand) == "" {
		return PreparedRequest{}, ValidationError{Message: "agent command is required"}
	}
	repoPath := req.Record.RepoPath
	if repoPath == "" && l.CurrentRepoPath != nil {
		repoPath, _ = l.CurrentRepoPath()
	}
	worktreePath := req.Record.WorktreePath
	if worktreePath == "" {
		worktreePath = repoPath
	}
	if worktreePath == "" {
		return PreparedRequest{}, ValidationError{Message: "Cannot determine launch path for this flow"}
	}
	planPath := req.Record.PlanPath
	if req.Record.PlanID != "" && planPath == "" {
		if l.PlanMarkdownPath == nil {
			return PreparedRequest{}, ValidationError{Message: "Cannot determine linked plan path"}
		}
		var err error
		planPath, err = l.PlanMarkdownPath(req.Record.PlanID)
		if err != nil {
			return PreparedRequest{}, ValidationError{Message: err.Error()}
		}
	}
	if phaseID == "plan-review" && req.Record.PlanID == "" {
		return PreparedRequest{}, ValidationError{Message: "Plan Review needs a linked plan before launch"}
	}
	generateLaunchID := l.NewLaunchID
	if generateLaunchID == nil {
		generateLaunchID = newLaunchID
	}
	return PreparedRequest{
		Request:      req,
		RepoPath:     repoPath,
		WorktreePath: worktreePath,
		PlanPath:     planPath,
		LaunchID:     generateLaunchID(),
	}, nil
}

func (l Launcher) Prepare(req PreparedRequest) (Result, error) {
	planBody := ""
	if req.Record.PlanID != "" && PromptNeedsPlanBody(req.Phase.PhaseID) {
		body, err := l.readPlan(req.Record.PlanID)
		if err != nil {
			return Result{}, fmt.Errorf("failed to read linked plan %s: %w", req.Record.PlanID, err)
		}
		planBody = body
	}
	updated, err := l.addPhaseLaunchID(flowstore.PhaseLaunchUpdate{
		FlowID:        req.Record.FlowID,
		PhaseID:       req.Phase.PhaseID,
		LaunchID:      req.LaunchID,
		AutoLaunch:    req.AutoLaunch,
		RejectRunning: req.RejectRunning,
	})
	if err != nil {
		if req.AutoLaunch && flowstore.IsAutoLaunchOutdated(err) {
			return Result{Skipped: true}, nil
		}
		return Result{}, fmt.Errorf("failed to mark flow phase running: %w", err)
	}
	launchPhase := req.Phase
	if persistedPhase, ok := PhaseByID(updated, req.Phase.PhaseID); ok {
		launchPhase = persistedPhase
	}
	command := agent.Normalize(l.AgentCommand)
	ctx := actions.AgentLaunchContext{
		Command:           command,
		ReasoningEffort:   l.reasoningEffort(command),
		LaunchID:          req.LaunchID,
		RepoPath:          req.RepoPath,
		WorktreePath:      req.WorktreePath,
		Branch:            req.Record.Branch,
		Commit:            req.Record.Commit,
		SessionStateRoot:  l.SessionStateRoot,
		PlanID:            req.Record.PlanID,
		PlanPath:          req.PlanPath,
		FlowID:            req.Record.FlowID,
		FlowPhaseID:       launchPhase.PhaseID,
		FlowPhaseTerminal: flowstore.PhaseStatusTerminal(launchPhase.Status),
		InitialPrompt:     PhasePrompt(req.Record, launchPhase, req.PlanPath, planBody, l.Templates),
	}
	route := RouteExternal
	switch command {
	case agent.CommandCodex, agent.CommandClaude:
		route = RouteEmbedded
		ctx.FlowLaunchTracked = true
		ctx.Embedded = true
		ctx.Headless = req.Headless
	}
	return Result{Context: ctx, Route: route}, nil
}

func (l Launcher) readPlan(planID string) (string, error) {
	if l.ReadPlan == nil {
		return "", nil
	}
	return l.ReadPlan(planID)
}

func (l Launcher) addPhaseLaunchID(update flowstore.PhaseLaunchUpdate) (flowstore.FlowRecord, error) {
	if l.AddPhaseLaunchID == nil {
		return flowstore.FlowRecord{}, nil
	}
	return l.AddPhaseLaunchID(update)
}

func (l Launcher) reasoningEffort(command string) string {
	switch command {
	case agent.CommandCodex, agent.CommandClaude:
		return l.ReasoningEffort
	default:
		return ""
	}
}

func PhaseCanLaunch(record flowstore.FlowRecord, phase flowstore.FlowPhase) bool {
	if phase.Status == flowstore.PhaseReady {
		return true
	}
	return artifacts.NormalizePhaseID(phase.PhaseID) == "autoreview" &&
		(phase.Status == flowstore.PhaseNeedsAttention || phase.Status == flowstore.PhaseBlocked) &&
		flowstore.HasPRTarget(record.PR) &&
		flowstore.PhasePredecessorsSatisfied(record, phase.PhaseID)
}

func PhaseByID(record flowstore.FlowRecord, phaseID string) (flowstore.FlowPhase, bool) {
	normalized := artifacts.NormalizePhaseID(phaseID)
	if normalized == "" {
		return flowstore.FlowPhase{}, false
	}
	for _, phase := range record.Phases {
		if phase.PhaseID == phaseID {
			return phase, true
		}
	}
	for _, phase := range record.Phases {
		if artifacts.NormalizePhaseID(phase.PhaseID) == normalized {
			return phase, true
		}
	}
	return flowstore.FlowPhase{}, false
}
