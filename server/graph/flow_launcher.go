package graph

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/flowlaunch"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/planstore"
	"github.com/brian-bell/flowstate/server/runtimejobs"
)

type flowRuntimeLaunch struct {
	Context  actions.AgentLaunchContext
	Snapshot runtimejobs.Snapshot
}

type flowRuntimeStartError struct {
	err error
}

func (err *flowRuntimeStartError) Error() string {
	return err.err.Error()
}

func (err *flowRuntimeStartError) Unwrap() error {
	return err.err
}

func (r *mutationResolver) newFlowLauncher(record flowstore.FlowRecord, command, reasoningEffort string) flowlaunch.Launcher {
	return flowlaunch.Launcher{
		PlanMarkdownPath: func(planID string) (string, error) {
			return planstore.MarkdownPath(r.StateRoot, planID)
		},
		ReadPlan: func(planID string) (string, error) {
			planPath := record.PlanPath
			if strings.TrimSpace(planPath) == "" {
				var err error
				planPath, err = planstore.MarkdownPath(r.StateRoot, planID)
				if err != nil {
					return "", err
				}
			}
			data, err := os.ReadFile(planPath)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
		AddPhaseLaunchID: r.FlowStore.AddPhaseLaunchID,
		SessionStateRoot: r.StateRoot,
		AgentCommand:     command,
		ReasoningEffort:  reasoningEffort,
		Templates:        r.FlowPromptTemplates,
	}
}

func (r *mutationResolver) startFlowRuntimeJob(ctx context.Context, record flowstore.FlowRecord, phase flowstore.FlowPhase, command, reasoningEffort string) (flowRuntimeLaunch, error) {
	launcher := r.newFlowLauncher(record, command, reasoningEffort)
	prepared, err := launcher.Preflight(flowlaunch.Request{
		Record:        record,
		Phase:         phase,
		Headless:      true,
		RejectRunning: true,
	})
	if err != nil {
		return flowRuntimeLaunch{}, err
	}
	result, err := launcher.Prepare(prepared)
	if err != nil {
		return flowRuntimeLaunch{}, err
	}
	launchContext := result.Context
	launchContext.Embedded = false
	launchContext.Headless = true
	launchContext.FlowLaunchTracked = true
	snapshot, err := r.RuntimeStarter.Start(ctx, runtimejobs.StartRequest{
		FlowID:   record.FlowID,
		PhaseID:  launchContext.FlowPhaseID,
		LaunchID: launchContext.LaunchID,
		Context:  launchContext,
	})
	if err != nil {
		if _, updateErr := r.FlowStore.SetPhase(flowstore.PhaseUpdate{
			FlowID:  record.FlowID,
			PhaseID: launchContext.FlowPhaseID,
			Status:  flowstore.PhaseNeedsAttention,
			Outcome: launchStartFailureOutcome(launchContext.FlowPhaseID),
			Notes:   "Runtime job failed to start: " + err.Error(),
		}); updateErr != nil {
			return flowRuntimeLaunch{}, fmt.Errorf("runtime job failed to start: %w; additionally failed to mark phase needs_attention: %v", err, updateErr)
		}
		return flowRuntimeLaunch{
			Context: launchContext,
		}, &flowRuntimeStartError{err: err}
	}
	return flowRuntimeLaunch{
		Context:  launchContext,
		Snapshot: snapshot,
	}, nil
}
