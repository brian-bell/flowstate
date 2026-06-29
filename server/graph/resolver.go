package graph

//go:generate go run github.com/99designs/gqlgen@v0.17.93 generate

import (
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/server/flowquery"
)

type FlowReader interface {
	List(flowstore.FlowFilter) ([]flowstore.FlowRecord, error)
	Read(string) (flowstore.FlowRecord, error)
}

type Resolver struct {
	FlowReader  FlowReader
	RuntimeJobs flowquery.RuntimeJobLookup
}
