package graph

import (
	"testing"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/server/graph/model"
)

func TestFlowPhaseStatusInputToStoreMapsEveryGeneratedEnum(t *testing.T) {
	for _, status := range model.AllFlowPhaseStatus {
		if got := flowPhaseStatusInputToStore(status); got == "" {
			t.Fatalf("flowPhaseStatusInputToStore(%s) = empty string", status)
		}
	}
	if got := flowPhaseStatusInputToStore(model.FlowPhaseStatusReady); got != flowstore.PhaseReady {
		t.Fatalf("READY maps to %q, want store ready so SetPhase rejects it", got)
	}
	if got := flowPhaseStatusInputToStore(model.FlowPhaseStatusPending); got != flowstore.PhasePending {
		t.Fatalf("PENDING maps to %q, want store pending so SetPhase rejects it", got)
	}
}
