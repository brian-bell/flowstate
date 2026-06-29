package graph

//go:generate go run github.com/99designs/gqlgen@v0.17.93 generate

import (
	"context"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/server/flowquery"
)

type FlowStore interface {
	List(flowstore.FlowFilter) ([]flowstore.FlowRecord, error)
	Read(string) (flowstore.FlowRecord, error)
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

type Resolver struct {
	FlowStore   FlowStore
	FlowCreator FlowCreator
	RuntimeJobs flowquery.RuntimeJobLookup
}
