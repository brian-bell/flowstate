package graph

import (
	"fmt"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/artifacts"
	"github.com/brian-bell/flowstate/server/flowquery"
	"github.com/brian-bell/flowstate/server/graph/model"
)

func (r *mutationResolver) flowView(record flowstore.FlowRecord) flowquery.Flow {
	view, err := flowquery.BuildWithRuntime(record, r.RuntimeJobs)
	if err != nil {
		return flowquery.Build(record)
	}
	return view
}

func (r *mutationResolver) flowAndPhase(record flowstore.FlowRecord, phaseID string) (*model.Flow, *model.FlowPhase, error) {
	view := r.flowView(record)
	normalized := artifacts.NormalizePhaseID(phaseID)
	for _, phase := range view.Phases {
		if artifacts.NormalizePhaseID(phase.PhaseID) == normalized {
			return flowToGraphQL(view), phaseToGraphQL(phase), nil
		}
	}
	return nil, nil, fmt.Errorf("updated phase %q not found in flow %q", phaseID, record.FlowID)
}
