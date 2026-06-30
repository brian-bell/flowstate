package model

import (
	"github.com/brian-bell/flowstate/flowlaunch"
	"github.com/brian-bell/flowstate/flowstore"
)

const flowPhaseDoneInstruction = flowlaunch.PhaseDoneInstruction

type FlowPromptTemplates = flowlaunch.PromptTemplates

func renderFlowPromptTemplate(template string, record flowstore.FlowRecord, phase flowstore.FlowPhase, planPath, planBody string) string {
	return flowlaunch.RenderPromptTemplate(template, record, phase, planPath, planBody)
}

func ensureFlowPhaseDoneInstruction(prompt, guardSource string) string {
	return flowlaunch.EnsurePhaseDoneInstruction(prompt, guardSource)
}

func flowPhasePrompt(record flowstore.FlowRecord, phase flowstore.FlowPhase, planPath, planBody string, templates FlowPromptTemplates) string {
	return flowlaunch.PhasePrompt(record, phase, planPath, planBody, templates)
}

func flowPhasePromptNeedsPlanBody(phaseID string) bool {
	return flowlaunch.PromptNeedsPlanBody(phaseID)
}

func flowPlanPrompt(record flowstore.FlowRecord, templates FlowPromptTemplates) string {
	return flowlaunch.PlanPrompt(record, templates)
}
