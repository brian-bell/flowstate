package flowlaunch

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/artifacts"
)

const PhaseDoneInstruction = "After completing this phase goal, mark this Flow phase done with flowstate."

type PromptTemplates struct {
	Plan           string
	PlanReview     string
	Implementation string
	ReviewLoop     string
	PRCreation     string
	Autoreview     string
	Merge          string
	Generic        string
}

func (templates PromptTemplates) templateForPhase(phaseID string) string {
	switch artifacts.NormalizePhaseID(phaseID) {
	case "plan":
		return templates.Plan
	case "plan-review":
		return templates.PlanReview
	case "implementation":
		return templates.Implementation
	case "review-loop":
		return templates.ReviewLoop
	case "pr-creation":
		return templates.PRCreation
	case "autoreview":
		return templates.Autoreview
	case "merge":
		return templates.Merge
	default:
		return templates.Generic
	}
}

func RenderPromptTemplate(template string, record flowstore.FlowRecord, phase flowstore.FlowPhase, planPath, planBody string) string {
	phaseTitle := phase.Title
	if phaseTitle == "" {
		phaseTitle = phase.PhaseID
	}
	replacer := strings.NewReplacer(
		"{flow_id}", record.FlowID,
		"{flow_title}", record.Title,
		"{instructions}", record.Instructions,
		"{phase_id}", phase.PhaseID,
		"{phase_title}", phaseTitle,
		"{plan_id}", record.PlanID,
		"{plan_path}", planPath,
		"{plan_body}", planBody,
		"{repo_path}", record.RepoPath,
		"{worktree_path}", record.WorktreePath,
		"{branch}", record.Branch,
		"{commit}", record.Commit,
		"{base_ref}", record.BaseRef,
		"{pr_provider}", record.PR.Provider,
		"{pr_number}", prNumberPlaceholder(record.PR.Number),
		"{pr_url}", record.PR.URL,
		"{pr_head}", record.PR.HeadBranch,
		"{pr_base}", record.PR.BaseBranch,
		"{pr_status}", record.PR.Status,
	)
	return replacer.Replace(template)
}

func prNumberPlaceholder(number int) string {
	if number == 0 {
		return ""
	}
	return strconv.Itoa(number)
}

func EnsurePhaseDoneInstruction(prompt, guardSource string) string {
	guard := guardSource
	if strings.TrimSpace(guard) == "" {
		guard = prompt
	}
	if lastNonEmptyPromptLine(guard) == PhaseDoneInstruction {
		return strings.TrimRight(prompt, " \t\r\n")
	}
	return strings.TrimRight(prompt, " \t\r\n") + "\n\n" + PhaseDoneInstruction
}

func lastNonEmptyPromptLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSuffix(lines[i], "\r")
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

func PhasePrompt(record flowstore.FlowRecord, phase flowstore.FlowPhase, planPath, planBody string, templates PromptTemplates) string {
	if template := templates.templateForPhase(phase.PhaseID); strings.TrimSpace(template) != "" {
		prompt := RenderPromptTemplate(template, record, phase, planPath, planBody)
		return EnsurePhaseDoneInstruction(prompt, template)
	}
	var prompt string
	switch artifacts.NormalizePhaseID(phase.PhaseID) {
	case "plan":
		prompt = PlanPrompt(record, templates)
	case "plan-review":
		prompt = planReviewPrompt(record, phase, planPath)
	case "implementation":
		prompt = implementationPrompt(record, phase, planPath)
	case "review-loop":
		prompt = reviewLoopPrompt(record, phase)
	case "pr-creation":
		prompt = prCreationPrompt(record, phase)
	case "autoreview":
		prompt = autoreviewPrompt(record, phase)
	case "merge":
		prompt = mergePrompt(record, phase)
	default:
		prompt = genericPhasePrompt(record, phase, planPath, planBody)
	}
	return EnsurePhaseDoneInstruction(prompt, "")
}

func PromptNeedsPlanBody(phaseID string) bool {
	switch artifacts.NormalizePhaseID(phaseID) {
	case "plan", "plan-review", "implementation", "review-loop", "pr-creation", "autoreview", "merge":
		return false
	default:
		return true
	}
}

func planReviewPrompt(record flowstore.FlowRecord, phase flowstore.FlowPhase, planPath string) string {
	return minimalArtifactPrompt("Use the review-loop skill to review the saved plan, max 6 loops.\nUse the flowstate skill to record the Plan Review verdict before finishing; the phase is not done until the verdict is persisted.", planPath, record, phase)
}

func implementationPrompt(record flowstore.FlowRecord, phase flowstore.FlowPhase, planPath string) string {
	if strings.TrimSpace(planPath) == "" {
		return implementationWithoutPlanPrompt(record, phase)
	}
	return minimalArtifactPrompt("Implement the approved plan.\nUse the commit skill before completing this phase.", planPath, record, phase)
}

func implementationWithoutPlanPrompt(record flowstore.FlowRecord, phase flowstore.FlowPhase) string {
	var b strings.Builder
	b.WriteString("Implement the Flow instructions.\n\n")
	writeChangeMetadata(&b, record)
	writePromptHeader(&b, record, "")
	writePromptPlanContext(&b, record, "")
	writePromptPhaseSummary(&b, record, "Plan Review context", "plan-review")
	writeRestartPromptIfNeeded(&b, record, phase)
	b.WriteString("\nUse the commit skill before completing this phase.")
	b.WriteString("\nAdvance this phase with `flowstate flow phase set` only after the implementation is complete, blocked, or needs attention.")
	return b.String()
}

func reviewLoopPrompt(record flowstore.FlowRecord, phase flowstore.FlowPhase) string {
	return minimalChangePrompt("Use the review-loop workflow with goal: review-and-revise.\nUse the commit skill when revisions are made.\nUse the flowstate skill to record the Review Loop result before finishing; the phase is not done until the result is persisted.", record, phase)
}

func prCreationPrompt(record flowstore.FlowRecord, phase flowstore.FlowPhase) string {
	head := strings.TrimSpace(record.Branch)
	if head == "" {
		head = "<head>"
	}
	base := strings.TrimSpace(record.BaseRef)
	if base == "" {
		base = "<base>"
	}
	instruction := fmt.Sprintf("Use the ship skill to create a PR for the changes.\nAfter the PR exists, run `flowstate flow pr set --flow-id %s --provider github --number <number> --url <url> --head %s --base %s` before completing this phase.", record.FlowID, head, base)
	return minimalChangePrompt(instruction, record, phase)
}

func minimalArtifactPrompt(instruction, planPath string, record flowstore.FlowRecord, phase flowstore.FlowPhase) string {
	var b strings.Builder
	b.WriteString(instruction)
	b.WriteString("\n\nPlan: ")
	b.WriteString(planPath)
	b.WriteString("\n")
	writeChangeMetadata(&b, record)
	writeRestartPromptIfNeeded(&b, record, phase)
	return b.String()
}

func autoreviewPrompt(record flowstore.FlowRecord, phase flowstore.FlowPhase) string {
	var b strings.Builder
	b.WriteString("Use the autoreview skill for this second-level review.\n")
	b.WriteString("Use the ship skill when fixes require commits or pushes.\n")
	b.WriteString("Use the flowstate skill to record the Autoreview result before finishing; the phase is not done until the result is persisted.\n\n")
	writeChangeMetadata(&b, record)
	if flowstore.HasPRTarget(record.PR) {
		fmt.Fprintf(&b, "\nPR target:\n- PR: %s #%d\n- URL: %s\n- Head: %s\n- Base: %s", record.PR.Provider, record.PR.Number, record.PR.URL, record.PR.HeadBranch, record.PR.BaseBranch)
		if record.PR.Status != "" {
			fmt.Fprintf(&b, "\n- Status: %s", record.PR.Status)
		}
	} else {
		b.WriteString("\nPR target: missing. Do not run Autoreview until `flowstate flow pr set` records provider, number, URL, head, and base.\n")
	}
	return b.String()
}

func writeRestartPromptIfNeeded(b *strings.Builder, record flowstore.FlowRecord, phase flowstore.FlowPhase) {
	if phase.Status != flowstore.PhaseNeedsAttention && phase.Status != flowstore.PhaseBlocked {
		return
	}
	fmt.Fprintf(b, "\nRestart required: this phase is %s. Before marking it completed, record the rerun:\n", phase.Status)
	fmt.Fprintf(b, "flowstate flow phase restart --flow-id %s --phase-id %s --notes \"Rerunning %s after addressing prior findings.\"\n", record.FlowID, phase.PhaseID, phase.Title)
}

func mergePrompt(record flowstore.FlowRecord, phase flowstore.FlowPhase) string {
	var b strings.Builder
	b.WriteString("Merge the PR deliberately.\n\n")
	writeChangeMetadata(&b, record)
	if flowstore.HasPRTarget(record.PR) {
		fmt.Fprintf(&b, "\n\nPR target:\n- PR: %s #%d\n- URL: %s\n- Head: %s\n- Base: %s\n", record.PR.Provider, record.PR.Number, record.PR.URL, record.PR.HeadBranch, record.PR.BaseBranch)
		if record.PR.Status != "" {
			fmt.Fprintf(&b, "- Status: %s\n", record.PR.Status)
		}
	} else {
		b.WriteString("\n\nPR target: missing. Do not merge until `flowstate flow pr set` records provider, number, URL, head, and base.\n")
	}
	writeRestartPromptIfNeeded(&b, record, phase)
	fmt.Fprintf(&b, "\nmerged:\nflowstate flow phase set --flow-id %s --phase-id %s --status completed --outcome merged --summary \"...\"\n", record.FlowID, phase.PhaseID)
	fmt.Fprintf(&b, "flowstate flow merge set --flow-id %s --status merged --commit <merge-commit> --merged-at <rfc3339>\n\n", record.FlowID)
	fmt.Fprintf(&b, "blocked:\nflowstate flow phase set --flow-id %s --phase-id %s --status blocked --outcome blocked --notes \"...\"\n", record.FlowID, phase.PhaseID)
	fmt.Fprintf(&b, "flowstate flow merge set --flow-id %s --status blocked", record.FlowID)
	return b.String()
}

func minimalChangePrompt(instruction string, record flowstore.FlowRecord, phase flowstore.FlowPhase) string {
	var b strings.Builder
	b.WriteString(instruction)
	b.WriteString("\n\n")
	writeChangeMetadata(&b, record)
	writeRestartPromptIfNeeded(&b, record, phase)
	return b.String()
}

func writeChangeMetadata(b *strings.Builder, record flowstore.FlowRecord) {
	b.WriteString("Worktree: ")
	b.WriteString(record.WorktreePath)
	b.WriteString("\nBranch: ")
	b.WriteString(record.Branch)
	b.WriteString("\nStart commit: ")
	b.WriteString(record.Commit)
}

func genericPhasePrompt(record flowstore.FlowRecord, phase flowstore.FlowPhase, planPath, planBody string) string {
	var b strings.Builder
	b.WriteString("Use the flowstate skill for this launch.\n\n")
	b.WriteString("Flow phase: ")
	if phase.Title != "" {
		b.WriteString(phase.Title)
	} else {
		b.WriteString(phase.PhaseID)
	}
	b.WriteString(" (")
	b.WriteString(phase.PhaseID)
	b.WriteString(").\n")
	writePromptHeader(&b, record, planPath)
	writePromptPlanContext(&b, record, planBody)
	writeRestartPromptIfNeeded(&b, record, phase)
	b.WriteString("\nAdvance this phase with `flowstate flow phase set` only after the corresponding work is complete, blocked, or needs attention.")
	return b.String()
}

func writePromptPhaseSummary(b *strings.Builder, record flowstore.FlowRecord, title, phaseID string) {
	b.WriteString("\n")
	b.WriteString(title)
	b.WriteString(":\n")
	if phase, ok := PhaseByID(record, phaseID); ok {
		writePhaseContext(b, phase)
		return
	}
	b.WriteString("- Phase: ")
	b.WriteString(phaseID)
	b.WriteString("\n")
}

func writePromptHeader(b *strings.Builder, record flowstore.FlowRecord, planPath string) {
	if record.Instructions != "" {
		b.WriteString("\nCustom instructions:\n")
		b.WriteString(record.Instructions)
		b.WriteString("\n")
	}
	if record.PlanID != "" {
		b.WriteString("\nLinked plan: ")
		b.WriteString(record.PlanID)
		if planPath != "" {
			b.WriteString(" at ")
			b.WriteString(planPath)
		}
		b.WriteString("\n")
	}
}

func writePromptPlanContext(b *strings.Builder, record flowstore.FlowRecord, planBody string) {
	if plan, ok := PhaseByID(record, "plan"); ok {
		b.WriteString("\nPrior Plan context:\n")
		writePhaseContext(b, plan)
	}
	if planBody != "" {
		b.WriteString("\nSaved plan body:\n")
		b.WriteString(planBody)
		if !strings.HasSuffix(planBody, "\n") {
			b.WriteString("\n")
		}
	}
}

func writePhaseContext(b *strings.Builder, phase flowstore.FlowPhase) {
	if phase.PhaseID != "" {
		b.WriteString("- Phase: ")
		b.WriteString(phase.PhaseID)
		b.WriteString("\n")
	}
	if phase.Title != "" {
		b.WriteString("- Title: ")
		b.WriteString(phase.Title)
		b.WriteString("\n")
	}
	b.WriteString("- Status: ")
	b.WriteString(phase.Status)
	b.WriteString("\n")
	if phase.Outcome != "" {
		b.WriteString("- Outcome: ")
		b.WriteString(phase.Outcome)
		b.WriteString("\n")
	}
	if phase.Summary != "" {
		b.WriteString("- Summary: ")
		b.WriteString(phase.Summary)
		b.WriteString("\n")
	}
	if phase.Notes != "" {
		b.WriteString("- Notes: ")
		b.WriteString(phase.Notes)
		b.WriteString("\n")
	}
}

func PlanPrompt(record flowstore.FlowRecord, templates PromptTemplates) string {
	if template := templates.templateForPhase("plan"); strings.TrimSpace(template) != "" {
		prompt := RenderPromptTemplate(template, record, flowstore.FlowPhase{PhaseID: "plan", Title: "Plan"}, record.PlanPath, "")
		return EnsurePhaseDoneInstruction(prompt, template)
	}
	var b strings.Builder
	b.WriteString("Use the flowstate skill for this launch.\n\n")
	b.WriteString(record.Instructions)
	b.WriteString("\n\nProduce a plan only; do not start coding in this phase.")
	b.WriteString("\nCreate and persist the plan with flowstate plan save, link it back with flowstate flow plan set, then report Flow persistence failures explicitly before ending.")
	return EnsurePhaseDoneInstruction(b.String(), "")
}

func NewLaunchIDForTest() string {
	return newLaunchID()
}
