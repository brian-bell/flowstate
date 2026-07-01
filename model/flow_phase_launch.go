package model

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/brian-bell/flowstate/agent"
	"github.com/brian-bell/flowstate/flowlaunch"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/artifacts"
)

// flowLaunchCodexAppUnsupported is shown when a Flow phase launch is attempted
// with codex-app selected. codex-app opens the external macOS app and cannot
// run Flow phases, which execute headless as daemon runtime jobs.
const flowLaunchCodexAppUnsupported = "codex-app cannot run Flow phases; press A to choose codex or claude"

type FlowPhaseLaunchRoute = flowlaunch.Route

const (
	FlowPhaseLaunchExternal = flowlaunch.RouteExternal
	FlowPhaseLaunchEmbedded = flowlaunch.RouteEmbedded
)

type FlowPhaseLaunchRequest = flowlaunch.Request

type FlowPhaseLaunchPreparedRequest = flowlaunch.PreparedRequest

type FlowPhaseLaunchResult = flowlaunch.Result

type FlowPhaseLaunchValidationError = flowlaunch.ValidationError

type DaemonFlowPhaseLaunchRequest struct {
	FlowID          string
	PhaseID         string
	AgentCommand    string
	ReasoningEffort string
	Headless        bool
	AutoLaunch      bool
}

type DaemonFlowPhaseLaunchResult struct {
	FlowID   string
	PhaseID  string
	LaunchID string
	Skipped  bool
}

type FlowPhaseLauncher struct {
	CurrentRepoPath      func() (string, bool)
	PlanMarkdownPath     func(string) (string, error)
	ReadPlan             func(string) (string, error)
	AddFlowPhaseLaunchID func(flowstore.PhaseLaunchUpdate) (flowstore.FlowRecord, error)
	NewLaunchID          func() string
	SessionStateRoot     string
	AgentCommand         string
	ReasoningEffort      string
	PromptTemplates      FlowPromptTemplates
}

func (l FlowPhaseLauncher) launcher() flowlaunch.Launcher {
	return flowlaunch.Launcher{
		CurrentRepoPath:  l.CurrentRepoPath,
		PlanMarkdownPath: l.PlanMarkdownPath,
		ReadPlan:         l.ReadPlan,
		AddPhaseLaunchID: l.AddFlowPhaseLaunchID,
		NewLaunchID:      l.NewLaunchID,
		SessionStateRoot: l.SessionStateRoot,
		AgentCommand:     l.AgentCommand,
		ReasoningEffort:  l.ReasoningEffort,
		Templates:        l.PromptTemplates,
	}
}

func (m Model) flowPhaseLauncher() FlowPhaseLauncher {
	command, reasoningEffort := m.flowLaunchAgentSettings()
	return FlowPhaseLauncher{
		CurrentRepoPath:      m.currentRepoPath,
		PlanMarkdownPath:     m.planMarkdownPath,
		ReadPlan:             m.readPlan,
		AddFlowPhaseLaunchID: m.addFlowPhaseLaunchID,
		NewLaunchID:          newLaunchID,
		SessionStateRoot:     m.sessionStateRoot,
		AgentCommand:         command,
		ReasoningEffort:      reasoningEffort,
		PromptTemplates:      m.flowPromptTemplates,
	}
}

func (l FlowPhaseLauncher) Preflight(req FlowPhaseLaunchRequest) (FlowPhaseLaunchPreparedRequest, error) {
	return l.launcher().Preflight(req)
}

func (l FlowPhaseLauncher) Prepare(req FlowPhaseLaunchPreparedRequest) (FlowPhaseLaunchResult, error) {
	return l.launcher().Prepare(req)
}

func newlyCompletedFlowPhase(previous, current flowstore.FlowRecord) (flowstore.FlowPhase, bool) {
	previousByPhaseID := make(map[string]flowstore.FlowPhase, len(previous.Phases))
	for _, phase := range previous.Phases {
		if phaseID := artifacts.NormalizePhaseID(phase.PhaseID); phaseID != "" {
			previousByPhaseID[phaseID] = phase
		}
	}
	for _, phase := range flowstore.OrderedPhases(current.Phases) {
		phaseID := artifacts.NormalizePhaseID(phase.PhaseID)
		if phaseID == "" || phase.Status != flowstore.PhaseCompleted {
			continue
		}
		previousPhase, ok := previousByPhaseID[phaseID]
		if ok && previousPhase.Status != flowstore.PhaseCompleted {
			return phase, true
		}
	}
	return flowstore.FlowPhase{}, false
}

func nextAutoLaunchPhase(record flowstore.FlowRecord) (flowstore.FlowPhase, bool) {
	for _, phase := range flowstore.OrderedPhases(record.Phases) {
		switch artifacts.NormalizePhaseID(phase.PhaseID) {
		case "", "merge":
			continue
		}
		if phase.Status == flowstore.PhaseReady {
			return phase, true
		}
	}
	return flowstore.FlowPhase{}, false
}

func (m Model) selectedFlowNextLaunchablePhase() (flowstore.FlowRecord, flowstore.FlowPhase, bool) {
	record, ok := m.selectedFlow()
	if !ok || record.FlowID == "" {
		return flowstore.FlowRecord{}, flowstore.FlowPhase{}, false
	}
	for _, phase := range flowstore.OrderedPhases(record.Phases) {
		if flowPhaseCanLaunch(record, phase) {
			return record, phase, true
		}
	}
	return flowstore.FlowRecord{}, flowstore.FlowPhase{}, false
}

type flowPhaseLaunchTarget struct {
	FlowPhaseLaunchPreparedRequest
}

func (m Model) selectedFlowNextLaunchTarget() (flowPhaseLaunchTarget, bool, Model) {
	record, phase, ok := m.selectedFlowNextLaunchablePhase()
	if !ok {
		m = m.setStatus(statusOther, "No launchable Flow phase")
		return flowPhaseLaunchTarget{}, false, m
	}
	return m.flowPhaseLaunchTarget(FlowPhaseLaunchRequest{
		Record:   record,
		Phase:    phase,
		Headless: m.flowHeadless,
	})
}

func (m Model) launchFlowPhaseTarget(target flowPhaseLaunchTarget) (tea.Model, tea.Cmd) {
	return m, m.prepareFlowPhaseLaunch(target)
}

func (m Model) flowPhaseLaunchTarget(req FlowPhaseLaunchRequest) (flowPhaseLaunchTarget, bool, Model) {
	prepared, err := m.flowPhaseLauncher().Preflight(req)
	if err != nil {
		m = m.setStatus(statusOther, err.Error())
		return flowPhaseLaunchTarget{}, false, m
	}
	return flowPhaseLaunchTarget{FlowPhaseLaunchPreparedRequest: prepared}, true, m
}

func (m Model) prepareFlowPhaseLaunch(target flowPhaseLaunchTarget) tea.Cmd {
	return func() tea.Msg {
		command, reasoningEffort := m.flowLaunchAgentSettings()
		if command == agent.CommandCodexApp {
			// codex-app runs as an external macOS app and cannot execute Flow
			// phases (which run headless as daemon runtime jobs). Skip auto
			// launches silently; surface a clear message for manual ones.
			if target.AutoLaunch {
				return nil
			}
			return ActionFailedMsg{RepoPath: target.RepoPath, Err: flowLaunchCodexAppUnsupported}
		}
		if m.launchFlowPhase != nil {
			result, err := m.launchFlowPhase(DaemonFlowPhaseLaunchRequest{
				FlowID:          target.Record.FlowID,
				PhaseID:         target.Phase.PhaseID,
				AgentCommand:    command,
				ReasoningEffort: reasoningEffort,
				Headless:        target.Headless,
				AutoLaunch:      target.AutoLaunch,
			})
			if err != nil {
				return ActionFailedMsg{RepoPath: target.RepoPath, Err: err.Error()}
			}
			if result.Skipped {
				return nil
			}
			return FlowPhaseLaunchedMsg{
				RepoPath:  target.RepoPath,
				FlowID:    result.FlowID,
				PhaseID:   result.PhaseID,
				LaunchID:  result.LaunchID,
				DaemonRun: true,
			}
		}
		result, err := m.flowPhaseLauncher().Prepare(target.FlowPhaseLaunchPreparedRequest)
		if err != nil {
			return ActionFailedMsg{RepoPath: target.RepoPath, Err: err.Error()}
		}
		if result.Skipped {
			return nil
		}
		return m.flowPhaseLaunchMessage(result)
	}
}

func (m Model) flowPhaseLaunchMessage(result FlowPhaseLaunchResult) tea.Msg {
	if result.Route == FlowPhaseLaunchEmbedded {
		return FlowEmbeddedLaunchRequestedMsg{LaunchContext: result.Context}
	}
	return PlanLaunchRequestedMsg{LaunchContext: result.Context}
}

func (m Model) prepareAutoFlowPhaseLaunch(previousFlows, currentFlows []flowstore.FlowRecord) (Model, tea.Cmd) {
	previousByFlowID := make(map[string]flowstore.FlowRecord, len(previousFlows))
	for _, record := range previousFlows {
		if record.FlowID != "" {
			previousByFlowID[record.FlowID] = record
		}
	}
	var cmds []tea.Cmd
	for _, record := range currentFlows {
		if !record.AutoMode || record.FlowID == "" {
			continue
		}
		previous, ok := previousByFlowID[record.FlowID]
		if !ok {
			continue
		}
		completedPhase, ok := newlyCompletedFlowPhase(previous, record)
		if !ok {
			m = m.clearResolvedSuppressedAutoFlowLaunches(record)
			continue
		}
		if artifacts.NormalizePhaseID(completedPhase.PhaseID) == "autoreview" {
			continue
		}
		if m.isAutoFlowLaunchSuppressed(record.FlowID, completedPhase) {
			m = m.clearSuppressedAutoFlowLaunch(record.FlowID, completedPhase)
			continue
		}
		sourceLaunchID := flowstore.LatestPhaseLaunchID(completedPhase)
		if m.hasRunningFlowEmbeddedTerminalForPhaseLaunch(record.FlowID, completedPhase.PhaseID, sourceLaunchID) ||
			m.hasAutoClosingFlowEmbeddedTerminalForPhaseLaunch(record.FlowID, completedPhase.PhaseID, sourceLaunchID) {
			m = m.deferAutoFlowPhaseLaunch(record.FlowID, completedPhase.PhaseID)
			continue
		}
		if m.hasFlowEmbeddedTerminalForPhaseLaunch(record.FlowID, completedPhase.PhaseID, sourceLaunchID) {
			m = m.suppressAutoFlowPhaseLaunch(record.FlowID, completedPhase.PhaseID, sourceLaunchID)
			continue
		}
		phase, ok := nextAutoLaunchPhase(record)
		if !ok {
			continue
		}
		var cmd tea.Cmd
		m, cmd = m.prepareAutoFlowPhaseLaunchForRecord(record, phase)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return m, batchNonNil(cmds...)
}

type deferredAutoFlowLaunchKey struct {
	FlowID  string
	PhaseID string
}

type suppressedAutoFlowLaunchKey struct {
	FlowID   string
	PhaseID  string
	LaunchID string
}

func (m Model) deferAutoFlowPhaseLaunch(flowID, phaseID string) Model {
	key, ok := newDeferredAutoFlowLaunchKey(flowID, phaseID)
	if !ok {
		return m
	}
	if m.deferredAutoFlowLaunches == nil {
		m.deferredAutoFlowLaunches = make(map[deferredAutoFlowLaunchKey]struct{})
	}
	m.deferredAutoFlowLaunches[key] = struct{}{}
	return m
}

func (m Model) suppressAutoFlowPhaseLaunch(flowID, phaseID, launchID string) Model {
	key, ok := newSuppressedAutoFlowLaunchKey(flowID, phaseID, launchID)
	if !ok {
		return m
	}
	if m.suppressedAutoFlowLaunches == nil {
		m.suppressedAutoFlowLaunches = make(map[suppressedAutoFlowLaunchKey]struct{})
	}
	m.suppressedAutoFlowLaunches[key] = struct{}{}
	deferredKey, ok := newDeferredAutoFlowLaunchKey(flowID, phaseID)
	if ok {
		m = m.clearDeferredAutoFlowLaunch(deferredKey)
	}
	return m
}

func newSuppressedAutoFlowLaunchKey(flowID, phaseID, launchID string) (suppressedAutoFlowLaunchKey, bool) {
	phaseID = artifacts.NormalizePhaseID(phaseID)
	launchID = strings.TrimSpace(launchID)
	if flowID == "" || phaseID == "" {
		return suppressedAutoFlowLaunchKey{}, false
	}
	return suppressedAutoFlowLaunchKey{FlowID: flowID, PhaseID: phaseID, LaunchID: launchID}, true
}

func suppressedAutoFlowLaunchKeyForPhase(flowID string, phase flowstore.FlowPhase) (suppressedAutoFlowLaunchKey, bool) {
	return newSuppressedAutoFlowLaunchKey(flowID, phase.PhaseID, flowstore.LatestPhaseLaunchID(phase))
}

func (m Model) isAutoFlowLaunchSuppressed(flowID string, phase flowstore.FlowPhase) bool {
	key, ok := suppressedAutoFlowLaunchKeyForPhase(flowID, phase)
	if !ok {
		return false
	}
	_, suppressed := m.suppressedAutoFlowLaunches[key]
	return suppressed
}

func (m Model) clearSuppressedAutoFlowLaunch(flowID string, phase flowstore.FlowPhase) Model {
	key, ok := suppressedAutoFlowLaunchKeyForPhase(flowID, phase)
	if !ok || len(m.suppressedAutoFlowLaunches) == 0 {
		return m
	}
	delete(m.suppressedAutoFlowLaunches, key)
	if len(m.suppressedAutoFlowLaunches) == 0 {
		m.suppressedAutoFlowLaunches = nil
	}
	return m
}

func (m Model) clearResolvedSuppressedAutoFlowLaunches(record flowstore.FlowRecord) Model {
	if len(m.suppressedAutoFlowLaunches) == 0 || record.FlowID == "" {
		return m
	}
	for _, phase := range record.Phases {
		if phase.Status == flowstore.PhaseRunning || phase.Status == flowstore.PhaseCompleted {
			continue
		}
		m = m.clearSuppressedAutoFlowLaunch(record.FlowID, phase)
	}
	return m
}

func (m Model) clearDeferredAutoFlowLaunch(key deferredAutoFlowLaunchKey) Model {
	if len(m.deferredAutoFlowLaunches) == 0 {
		return m
	}
	delete(m.deferredAutoFlowLaunches, key)
	if len(m.deferredAutoFlowLaunches) == 0 {
		m.deferredAutoFlowLaunches = nil
	}
	return m
}

func newDeferredAutoFlowLaunchKey(flowID, phaseID string) (deferredAutoFlowLaunchKey, bool) {
	phaseID = artifacts.NormalizePhaseID(phaseID)
	if flowID == "" || phaseID == "" {
		return deferredAutoFlowLaunchKey{}, false
	}
	return deferredAutoFlowLaunchKey{FlowID: flowID, PhaseID: phaseID}, true
}

func (m Model) prepareAutoFlowPhaseLaunchForRecord(record flowstore.FlowRecord, phase flowstore.FlowPhase) (Model, tea.Cmd) {
	target, ok, next := m.flowPhaseLaunchTarget(FlowPhaseLaunchRequest{
		Record:     record,
		Phase:      phase,
		AutoLaunch: true,
		Headless:   m.flowHeadless,
	})
	m = next
	if !ok {
		return m, nil
	}
	return m, next.prepareFlowPhaseLaunch(target)
}

func (m Model) prepareDeferredAutoFlowPhaseLaunches() (Model, tea.Cmd) {
	var cmds []tea.Cmd
	for key := range m.deferredAutoFlowLaunches {
		delete(m.deferredAutoFlowLaunches, key)
		record, ok := m.flowByID(key.FlowID)
		if !ok {
			continue
		}
		if !record.AutoMode {
			continue
		}
		sourcePhase, sourcePhaseOK := flowRecordPhaseByID(record, key.PhaseID)
		sourceLaunchID := flowstore.LatestPhaseLaunchID(sourcePhase)
		if sourcePhaseOK && m.hasRunningFlowEmbeddedTerminalForPhaseLaunch(key.FlowID, key.PhaseID, sourceLaunchID) {
			m.deferredAutoFlowLaunches[key] = struct{}{}
			continue
		}
		if sourcePhaseOK && m.hasAutoClosingFlowEmbeddedTerminalForPhaseLaunch(key.FlowID, key.PhaseID, sourceLaunchID) {
			m.deferredAutoFlowLaunches[key] = struct{}{}
			continue
		}
		if sourcePhaseOK && m.hasFlowEmbeddedTerminalForPhaseLaunch(key.FlowID, key.PhaseID, sourceLaunchID) {
			continue
		}
		if phase, ok := nextAutoLaunchPhase(record); ok {
			var cmd tea.Cmd
			m, cmd = m.prepareAutoFlowPhaseLaunchForRecord(record, phase)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	if len(m.deferredAutoFlowLaunches) == 0 {
		m.deferredAutoFlowLaunches = nil
	}
	return m, batchNonNil(cmds...)
}

func flowPhaseCanLaunch(record flowstore.FlowRecord, phase flowstore.FlowPhase) bool {
	return flowlaunch.PhaseCanLaunch(record, phase)
}

func flowPhaseStatusDetail(phase flowstore.FlowPhase) string {
	detail := strings.TrimSpace(phase.Status)
	if detail == "" {
		detail = "unknown"
	}
	if phase.Outcome != "" {
		detail += " / " + phase.Outcome
	}
	if phase.Notes != "" {
		detail += ": " + phase.Notes
	} else if phase.Summary != "" {
		detail += ": " + phase.Summary
	}
	return detail
}

func flowAutoreviewMissingPRTarget(record flowstore.FlowRecord) bool {
	if flowstore.HasPRTarget(record.PR) {
		return false
	}
	prCreation, hasPRCreation := flowPhaseByID(record, "pr-creation")
	autoreview, hasAutoreview := flowPhaseByID(record, "autoreview")
	if !hasPRCreation || !hasAutoreview || prCreation.Status != flowstore.PhaseCompleted {
		return false
	}
	return autoreview.Status == flowstore.PhasePending ||
		autoreview.Status == flowstore.PhaseNeedsAttention ||
		autoreview.Status == flowstore.PhaseBlocked
}
