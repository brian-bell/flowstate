package graph

import (
	"fmt"

	"github.com/brian-bell/flowstate/agent"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/artifacts"
	"github.com/brian-bell/flowstate/server/graph/model"
)

func (r *mutationResolver) launchAgentCommand(input model.LaunchFlowPhaseInput) (string, error) {
	command := r.AgentCommand
	if input.AgentCommand != nil {
		command = *input.AgentCommand
	}
	command = agent.Normalize(command)
	if command == "" {
		return "", fmt.Errorf("agent command is required")
	}
	if err := agent.Validate(command); err != nil {
		return "", err
	}
	if command == agent.CommandCodexApp {
		return "", fmt.Errorf("codex-app cannot be launched as a server runtime job")
	}
	return command, nil
}

func (r *mutationResolver) launchReasoningEffort(command string, input model.LaunchFlowPhaseInput) (string, error) {
	effort := ""
	if input.ReasoningEffort != nil {
		effort = *input.ReasoningEffort
	} else {
		switch command {
		case agent.CommandCodex:
			effort = r.CodexReasoningEffort
		case agent.CommandClaude:
			effort = r.ClaudeReasoningEffort
		}
	}
	effort = agent.NormalizeReasoningEffort(effort)
	if err := agent.ValidateReasoningEffort(command, effort); err != nil {
		return "", err
	}
	return effort, nil
}

func launchStartFailureOutcome(phaseID string) string {
	if artifacts.NormalizePhaseID(phaseID) == "plan-review" {
		return flowstore.OutcomeChangesRequested
	}
	return "runtime_start_failed"
}
