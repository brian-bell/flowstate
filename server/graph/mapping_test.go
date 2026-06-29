package graph

import (
	"testing"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/server/graph/model"
)

func TestFlowPhaseStatusInputToStoreMapsEveryGeneratedEnum(t *testing.T) {
	for _, status := range model.AllFlowPhaseStatusInput {
		if got := flowPhaseStatusInputToStore(status); got == "" {
			t.Fatalf("flowPhaseStatusInputToStore(%s) = empty string", status)
		}
	}
	if len(model.AllFlowPhaseStatusInput) != len(flowstore.AgentSettablePhaseStatuses()) {
		t.Fatalf("FlowPhaseStatusInput enum count = %d, want %d", len(model.AllFlowPhaseStatusInput), len(flowstore.AgentSettablePhaseStatuses()))
	}
}
