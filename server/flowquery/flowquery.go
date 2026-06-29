package flowquery

import (
	"strings"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/artifacts"
)

type StaleRunningStatus string

const (
	StaleAwaitingSession  StaleRunningStatus = "AWAITING_SESSION"
	StaleSessionMismatch  StaleRunningStatus = "SESSION_MISMATCH"
	StaleMissingSessionID StaleRunningStatus = "MISSING_SESSION_ID"
)

type RuntimeJob struct {
	ID      string
	PhaseID string
	Status  string
}

type RuntimeJobLookup interface {
	ActiveRuntimeJob(flowstore.FlowRecord, flowstore.FlowPhase) (*RuntimeJob, error)
	RuntimeStateKnown() bool
}

type Flow struct {
	Record              flowstore.FlowRecord
	Phases              []Phase
	NextLaunchablePhase *Phase
}

type Phase struct {
	flowstore.FlowPhase
	AllowedNextStatuses []string
	Launchable          bool
	LatestLaunchID      string
	StaleRunningStatus  *StaleRunningStatus
	ActiveRuntimeJob    *RuntimeJob
}

func Build(record flowstore.FlowRecord) Flow {
	view, _ := BuildWithRuntime(record, nil)
	return view
}

func BuildWithRuntime(record flowstore.FlowRecord, runtimeJobs RuntimeJobLookup) (Flow, error) {
	ordered := flowstore.OrderedPhases(record.Phases)
	phases := make([]Phase, 0, len(ordered))
	nextIndex := -1
	for _, phase := range ordered {
		view, err := BuildPhaseWithRuntime(record, phase, runtimeJobs)
		if err != nil {
			return Flow{}, err
		}
		if nextIndex < 0 && view.Launchable {
			nextIndex = len(phases)
		}
		phases = append(phases, view)
	}
	var next *Phase
	if nextIndex >= 0 {
		next = &phases[nextIndex]
	}
	return Flow{Record: record, Phases: phases, NextLaunchablePhase: next}, nil
}

func BuildPhase(record flowstore.FlowRecord, phase flowstore.FlowPhase) Phase {
	view, _ := BuildPhaseWithRuntime(record, phase, nil)
	return view
}

func BuildPhaseWithRuntime(record flowstore.FlowRecord, phase flowstore.FlowPhase, runtimeJobs RuntimeJobLookup) (Phase, error) {
	var activeRuntimeJob *RuntimeJob
	if runtimeJobs != nil && runtimeJobs.RuntimeStateKnown() {
		job, err := runtimeJobs.ActiveRuntimeJob(record, phase)
		if err != nil {
			return Phase{}, err
		}
		activeRuntimeJob = job
	}
	return Phase{
		FlowPhase:           phase,
		AllowedNextStatuses: flowstore.AllowedNextPhaseStatuses(phase.Status),
		Launchable:          PhaseCanLaunch(record, phase),
		LatestLaunchID:      flowstore.LatestPhaseLaunchID(phase),
		StaleRunningStatus:  StaleRunningStatusForPhase(phase),
		ActiveRuntimeJob:    activeRuntimeJob,
	}, nil
}

func PhaseCanLaunch(record flowstore.FlowRecord, phase flowstore.FlowPhase) bool {
	if phase.Status == flowstore.PhaseReady {
		return true
	}
	return artifacts.NormalizePhaseID(phase.PhaseID) == "autoreview" &&
		(phase.Status == flowstore.PhaseNeedsAttention || phase.Status == flowstore.PhaseBlocked) &&
		flowstore.HasPRTarget(record.PR) &&
		flowstore.PhasePredecessorsSatisfied(record, phase.PhaseID)
}

func StaleRunningStatusForPhase(phase flowstore.FlowPhase) *StaleRunningStatus {
	if flowstore.PhaseSessionLaunchMismatch(phase) {
		status := StaleSessionMismatch
		return &status
	}
	if phase.Status == flowstore.PhaseRunning && flowstore.PhaseAwaitingSession(phase) {
		status := StaleAwaitingSession
		return &status
	}
	if session, ok := flowstore.LatestPhaseSession(phase, false); ok && strings.TrimSpace(session.SessionID) == "" {
		status := StaleMissingSessionID
		return &status
	}
	return nil
}
