package graph

//go:generate go run github.com/99designs/gqlgen@v0.17.93 generate

import (
	"context"

	"github.com/brian-bell/flowstate/flowlaunch"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/server/flowquery"
	"github.com/brian-bell/flowstate/server/runtimejobs"
)

type FlowStore interface {
	List(flowstore.FlowFilter) ([]flowstore.FlowRecord, error)
	Read(string) (flowstore.FlowRecord, error)
	AddPhaseLaunchID(flowstore.PhaseLaunchUpdate) (flowstore.FlowRecord, error)
	ResetAwaitingSessionPhase(flowstore.PhaseResetUpdate) (flowstore.FlowRecord, error)
	SetPhase(flowstore.PhaseUpdate) (flowstore.FlowRecord, error)
}

type CreateFlowInput struct {
	RepoPath     string
	Title        string
	Instructions string
	BaseRef      string
}

type FlowCreator interface {
	CreateFlow(context.Context, CreateFlowInput) (flowstore.FlowRecord, error)
}

type RuntimeStarter interface {
	Start(context.Context, runtimejobs.StartRequest) (runtimejobs.Snapshot, error)
}

type RuntimeController interface {
	Cancel(string) runtimejobs.CancelResult
}

type Resolver struct {
	FlowStore             FlowStore
	FlowCreator           FlowCreator
	RuntimeJobs           flowquery.RuntimeJobLookup
	RuntimeStarter        RuntimeStarter
	RuntimeController     RuntimeController
	AgentCommand          string
	CodexReasoningEffort  string
	ClaudeReasoningEffort string
	FlowPromptTemplates   flowlaunch.PromptTemplates
	StateRoot             string
}
