package model

import (
	"strings"
	"time"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/artifacts"
)

type FlowView struct {
	Record      flowstore.FlowRecord
	RuntimeJobs map[string]FlowRuntimeJob
}

type FlowRuntimeJob struct {
	ID               string
	LaunchID         string
	FlowID           string
	PhaseID          string
	Status           string
	CreatedAt        time.Time
	StartedAt        *time.Time
	EndedAt          *time.Time
	ExitCode         *int
	Error            string
	PhaseUpdateError string
	LogTail          string
	LogTruncated     bool
}

func flowViewsFromRecords(records []flowstore.FlowRecord) []FlowView {
	views := make([]FlowView, 0, len(records))
	for _, record := range records {
		views = append(views, FlowView{Record: record})
	}
	return views
}

func flowRecordsFromViews(views []FlowView) []flowstore.FlowRecord {
	records := make([]flowstore.FlowRecord, 0, len(views))
	for _, view := range views {
		records = append(records, view.Record)
	}
	return records
}

func flowRuntimeJobsFromViews(views []FlowView) map[string]map[string]FlowRuntimeJob {
	out := make(map[string]map[string]FlowRuntimeJob)
	for _, view := range views {
		flowID := strings.TrimSpace(view.Record.FlowID)
		if flowID == "" {
			continue
		}
		for phaseID, job := range view.RuntimeJobs {
			normalized := artifacts.NormalizePhaseID(phaseID)
			if normalized == "" {
				normalized = artifacts.NormalizePhaseID(job.PhaseID)
			}
			if normalized == "" || strings.TrimSpace(job.ID) == "" {
				continue
			}
			if out[flowID] == nil {
				out[flowID] = make(map[string]FlowRuntimeJob)
			}
			out[flowID][normalized] = job
		}
	}
	return out
}
