package graph

import (
	"fmt"
	"strings"

	"github.com/brian-bell/flowstate/agent"
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

func (r *mutationResolver) launchReasoningEffort(command string, input model.LaunchFlowPhaseInput) string {
	if input.ReasoningEffort != nil {
		return strings.TrimSpace(*input.ReasoningEffort)
	}
	switch command {
	case agent.CommandCodex:
		return r.CodexReasoningEffort
	case agent.CommandClaude:
		return r.ClaudeReasoningEffort
	default:
		return ""
	}
}
