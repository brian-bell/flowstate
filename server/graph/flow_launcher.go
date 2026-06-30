package graph

import (
	"os"
	"strings"

	"github.com/brian-bell/flowstate/flowlaunch"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/planstore"
)

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
