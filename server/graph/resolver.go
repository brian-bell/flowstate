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
	SetPhase(flowstore.PhaseUpdate) (flowstore.FlowRecord, error)
}

type RuntimeStarter interface {
	Start(context.Context, runtimejobs.StartRequest) (runtimejobs.Snapshot, error)
}

type Resolver struct {
	FlowStore             FlowStore
	RuntimeJobs           flowquery.RuntimeJobLookup
	RuntimeStarter        RuntimeStarter
	AgentCommand          string
	CodexReasoningEffort  string
	ClaudeReasoningEffort string
	FlowPromptTemplates   flowlaunch.PromptTemplates
	StateRoot             string
}
