package graph

import (
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/server/flowquery"
	"github.com/brian-bell/flowstate/server/graph/model"
)

func flowToGraphQL(view flowquery.Flow) *model.Flow {
	record := view.Record
	phases := make([]*model.FlowPhase, 0, len(view.Phases))
	for i := range view.Phases {
		phases = append(phases, phaseToGraphQL(view.Phases[i]))
	}
	var next *model.FlowPhase
	if view.NextLaunchablePhase != nil {
		next = phaseToGraphQL(*view.NextLaunchablePhase)
	}
	return &model.Flow{
		ID:                  record.FlowID,
		Title:               record.Title,
		Instructions:        record.Instructions,
		Status:              flowStatusToGraphQL(record.Status),
		StatusRaw:           record.Status,
		RepoPath:            record.RepoPath,
		WorktreePath:        record.WorktreePath,
		Branch:              record.Branch,
		BaseRef:             record.BaseRef,
		Commit:              record.Commit,
		PlanID:              record.PlanID,
		PlanPath:            record.PlanPath,
		AutoMode:            record.AutoMode,
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
		Pr:                  pullRequestToGraphQL(record.PR),
		Merge:               mergeToGraphQL(record.Merge),
		Phases:              phases,
		NextLaunchablePhase: next,
	}
}

func phaseToGraphQL(phase flowquery.Phase) *model.FlowPhase {
	allowed := make([]model.FlowPhaseStatus, 0, len(phase.AllowedNextStatuses))
	for _, status := range phase.AllowedNextStatuses {
		if mapped := flowPhaseStatusToGraphQL(status); mapped != nil {
			allowed = append(allowed, *mapped)
		}
	}
	var latestLaunchID *string
	if phase.LatestLaunchID != "" {
		latestLaunchID = &phase.LatestLaunchID
	}
	return &model.FlowPhase{
		PhaseID:             phase.PhaseID,
		ParentPhaseID:       phase.ParentPhaseID,
		Title:               phase.Title,
		Kind:                phase.Kind,
		Status:              flowPhaseStatusToGraphQL(phase.Status),
		StatusRaw:           phase.Status,
		Order:               phase.Order,
		Outcome:             phase.Outcome,
		Notes:               phase.Notes,
		Summary:             phase.Summary,
		LatestLaunchID:      latestLaunchID,
		CreatedAt:           phase.CreatedAt,
		UpdatedAt:           phase.UpdatedAt,
		AllowedNextStatuses: allowed,
		Launchable:          phase.Launchable,
		StaleRunningStatus:  staleRunningStatusToGraphQL(phase.StaleRunningStatus),
		ActiveRuntimeJob:    runtimeJobToGraphQL(phase.ActiveRuntimeJob),
	}
}

func pullRequestToGraphQL(pr flowstore.PullRequest) *model.PullRequest {
	return &model.PullRequest{
		Provider:   pr.Provider,
		Number:     pr.Number,
		URL:        pr.URL,
		HeadBranch: pr.HeadBranch,
		BaseBranch: pr.BaseBranch,
		Status:     pr.Status,
	}
}

func mergeToGraphQL(merge flowstore.Merge) *model.Merge {
	return &model.Merge{
		Status:   merge.Status,
		Commit:   merge.Commit,
		MergedAt: merge.MergedAt,
	}
}

func runtimeJobToGraphQL(job *flowquery.RuntimeJob) *model.RuntimeJob {
	if job == nil {
		return nil
	}
	return &model.RuntimeJob{ID: job.ID, PhaseID: job.PhaseID, Status: job.Status}
}

func flowStatusInputToStore(status model.FlowStatus) string {
	switch status {
	case model.FlowStatusPending:
		return flowstore.StatusPending
	case model.FlowStatusInProgress:
		return flowstore.StatusInProgress
	case model.FlowStatusNeedsAttention:
		return flowstore.StatusNeedsAttention
	case model.FlowStatusBlocked:
		return flowstore.StatusBlocked
	case model.FlowStatusCompleted:
		return flowstore.StatusCompleted
	case model.FlowStatusMerged:
		return flowstore.StatusMerged
	case model.FlowStatusAbandoned:
		return flowstore.StatusAbandoned
	default:
		return ""
	}
}

func flowStatusToGraphQL(status string) *model.FlowStatus {
	var mapped model.FlowStatus
	switch status {
	case flowstore.StatusPending:
		mapped = model.FlowStatusPending
	case flowstore.StatusInProgress:
		mapped = model.FlowStatusInProgress
	case flowstore.StatusNeedsAttention:
		mapped = model.FlowStatusNeedsAttention
	case flowstore.StatusBlocked:
		mapped = model.FlowStatusBlocked
	case flowstore.StatusCompleted:
		mapped = model.FlowStatusCompleted
	case flowstore.StatusMerged:
		mapped = model.FlowStatusMerged
	case flowstore.StatusAbandoned:
		mapped = model.FlowStatusAbandoned
	default:
		return nil
	}
	return &mapped
}

func flowPhaseStatusInputToStore(status model.FlowPhaseStatusInput) string {
	switch status {
	case model.FlowPhaseStatusInputRunning:
		return flowstore.PhaseRunning
	case model.FlowPhaseStatusInputNeedsAttention:
		return flowstore.PhaseNeedsAttention
	case model.FlowPhaseStatusInputCompleted:
		return flowstore.PhaseCompleted
	case model.FlowPhaseStatusInputBlocked:
		return flowstore.PhaseBlocked
	case model.FlowPhaseStatusInputSkipped:
		return flowstore.PhaseSkipped
	default:
		return ""
	}
}

func flowPhaseStatusToGraphQL(status string) *model.FlowPhaseStatus {
	var mapped model.FlowPhaseStatus
	switch status {
	case flowstore.PhasePending:
		mapped = model.FlowPhaseStatusPending
	case flowstore.PhaseReady:
		mapped = model.FlowPhaseStatusReady
	case flowstore.PhaseRunning:
		mapped = model.FlowPhaseStatusRunning
	case flowstore.PhaseNeedsAttention:
		mapped = model.FlowPhaseStatusNeedsAttention
	case flowstore.PhaseCompleted:
		mapped = model.FlowPhaseStatusCompleted
	case flowstore.PhaseBlocked:
		mapped = model.FlowPhaseStatusBlocked
	case flowstore.PhaseSkipped:
		mapped = model.FlowPhaseStatusSkipped
	default:
		return nil
	}
	return &mapped
}

func staleRunningStatusToGraphQL(status *flowquery.StaleRunningStatus) *model.FlowPhaseStaleRunningStatus {
	if status == nil {
		return nil
	}
	var mapped model.FlowPhaseStaleRunningStatus
	switch *status {
	case flowquery.StaleAwaitingSession:
		mapped = model.FlowPhaseStaleRunningStatusAwaitingSession
	case flowquery.StaleSessionMismatch:
		mapped = model.FlowPhaseStaleRunningStatusSessionMismatch
	case flowquery.StaleMissingSessionID:
		mapped = model.FlowPhaseStaleRunningStatusMissingSessionID
	default:
		return nil
	}
	return &mapped
}
