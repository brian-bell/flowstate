package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/planstore"
	"github.com/brian-bell/flowstate/server"
	"github.com/brian-bell/flowstate/server/flowquery"
	"github.com/brian-bell/flowstate/server/graph"
	"github.com/brian-bell/flowstate/server/runtimejobs"
)

func TestHandlerGraphQLListsFlowsWithFilteringAndComputedFields(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	old := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "old-flow",
		Title:        "Old Flow",
		Instructions: "old instructions",
		RepoPath:     t.TempDir(),
	})
	blocked := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "blocked-flow",
		Title:        "Blocked Flow",
		Instructions: "blocked instructions",
		RepoPath:     t.TempDir(),
	})
	blocked, err := store.SetPhase(flowstore.PhaseUpdate{
		FlowID:  blocked.FlowID,
		PhaseID: "plan",
		Status:  flowstore.PhaseBlocked,
		Notes:   "waiting",
	})
	if err != nil {
		t.Fatalf("SetPhase blocked: %v", err)
	}
	latest := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "latest-flow",
		Title:        "Latest Flow",
		Instructions: "latest instructions",
		RepoPath:     t.TempDir(),
	})
	_ = old
	_ = blocked

	handler := newFlowGraphQLHandler(t, store)
	var all struct {
		Data struct {
			Flows []struct {
				ID                  string `json:"id"`
				Status              string `json:"status"`
				StatusRaw           string `json:"statusRaw"`
				NextLaunchablePhase *struct {
					PhaseID string `json:"phaseId"`
				} `json:"nextLaunchablePhase"`
				Phases []struct {
					PhaseID             string   `json:"phaseId"`
					Status              *string  `json:"status"`
					StatusRaw           string   `json:"statusRaw"`
					Launchable          bool     `json:"launchable"`
					AllowedNextStatuses []string `json:"allowedNextStatuses"`
				} `json:"phases"`
			} `json:"flows"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `query {
		flows {
			id
			status
			statusRaw
			nextLaunchablePhase { phaseId }
			phases {
				phaseId
				status
				statusRaw
				launchable
				allowedNextStatuses
			}
		}
	}`, nil, &all)

	if len(all.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", all.Errors)
	}
	if got := flowIDs(all.Data.Flows); !equalStrings(got, []string{latest.FlowID, blocked.FlowID, old.FlowID}) {
		t.Fatalf("flow order = %#v, want latest, blocked, old", got)
	}
	if all.Data.Flows[0].Status != "PENDING" || all.Data.Flows[0].StatusRaw != flowstore.StatusPending {
		t.Fatalf("latest status = %q raw %q, want pending/raw pending", all.Data.Flows[0].Status, all.Data.Flows[0].StatusRaw)
	}
	if all.Data.Flows[0].NextLaunchablePhase == nil || all.Data.Flows[0].NextLaunchablePhase.PhaseID != "plan" {
		t.Fatalf("next launchable phase = %#v, want plan", all.Data.Flows[0].NextLaunchablePhase)
	}
	if !all.Data.Flows[0].Phases[0].Launchable {
		t.Fatal("ready plan phase should be launchable")
	}
	if got := all.Data.Flows[0].Phases[0].AllowedNextStatuses; !equalStrings(got, []string{"RUNNING", "NEEDS_ATTENTION", "COMPLETED", "BLOCKED", "SKIPPED"}) {
		t.Fatalf("allowed next statuses = %#v", got)
	}

	var filtered struct {
		Data struct {
			Flows []struct {
				ID string `json:"id"`
			} `json:"flows"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `query($statuses: [FlowStatus!]) {
		flows(statuses: $statuses) { id }
	}`, map[string]any{"statuses": []string{"BLOCKED"}}, &filtered)
	if len(filtered.Errors) != 0 {
		t.Fatalf("GraphQL filtered errors: %#v", filtered.Errors)
	}
	if got := flowIDs(filtered.Data.Flows); !equalStrings(got, []string{"blocked-flow"}) {
		t.Fatalf("filtered flow IDs = %#v, want blocked-flow", got)
	}
}

func TestHandlerGraphQLCreateFlowMutationIsAvailable(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	creator := &staticFlowCreator{
		record: flowstore.FlowRecord{
			FlowID:       "created-flow",
			Title:        "Created Flow",
			Instructions: "create through graphql",
			RepoPath:     "/dev/alpha",
			BaseRef:      "main",
			Phases:       []flowstore.FlowPhase{{PhaseID: "plan", Title: "Plan", Status: flowstore.PhaseReady}},
		},
	}
	handler := newFlowGraphQLHandlerWithOptions(t, server.HandlerOptions{
		FlowStore:   store,
		FlowCreator: creator,
	})

	var out struct {
		Data struct {
			CreateFlow struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				BaseRef  string `json:"baseRef"`
				RepoPath string `json:"repoPath"`
				Phases   []struct {
					PhaseID string `json:"phaseId"`
				} `json:"phases"`
			} `json:"createFlow"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: CreateFlowInput!) {
		createFlow(input: $input) { id title baseRef repoPath phases { phaseId } }
	}`, map[string]any{"input": map[string]any{
		"repoPath":     "/dev/alpha",
		"title":        "Created Flow",
		"instructions": "create through graphql",
		"baseRef":      "main",
	}}, &out)
	if len(out.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", out.Errors)
	}
	if creator.input.RepoPath != "/dev/alpha" ||
		out.Data.CreateFlow.ID != "created-flow" ||
		out.Data.CreateFlow.BaseRef != "main" ||
		len(out.Data.CreateFlow.Phases) != 1 ||
		out.Data.CreateFlow.Phases[0].PhaseID != "plan" {
		t.Fatalf("createFlow response = %#v input = %#v", out.Data.CreateFlow, creator.input)
	}
}

func TestHandlerGraphQLReadsOneFlowAndHandlesMissingOrInvalidID(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "one-flow",
		Title:        "One Flow",
		Instructions: "read one",
		RepoPath:     t.TempDir(),
	})
	handler := newFlowGraphQLHandler(t, store)

	var found struct {
		Data struct {
			Flow *struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"flow"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `query($id: ID!) { flow(id: $id) { id title } }`, map[string]any{"id": record.FlowID}, &found)
	if len(found.Errors) != 0 || found.Data.Flow == nil || found.Data.Flow.ID != record.FlowID {
		t.Fatalf("found response = %#v errors %#v", found.Data.Flow, found.Errors)
	}

	var missing struct {
		Data struct {
			Flow *struct {
				ID string `json:"id"`
			} `json:"flow"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `query($id: ID!) { flow(id: $id) { id } }`, map[string]any{"id": "missing-flow"}, &missing)
	if len(missing.Errors) != 0 || missing.Data.Flow != nil {
		t.Fatalf("missing response = %#v errors %#v, want null without errors", missing.Data.Flow, missing.Errors)
	}

	var invalid struct {
		Data   any   `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `query($id: ID!) { flow(id: $id) { id } }`, map[string]any{"id": "../bad"}, &invalid)
	if len(invalid.Errors) == 0 {
		t.Fatalf("invalid ID response had no GraphQL errors: %#v", invalid)
	}
}

func TestHandlerGraphQLRejectsInvalidFlowStatusEnumBeforeResolver(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	handler := newFlowGraphQLHandler(t, store)
	var out struct {
		Data   any   `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQLWithStatus(t, handler, http.StatusUnprocessableEntity, `query($statuses: [FlowStatus!]) { flows(statuses: $statuses) { id } }`, map[string]any{
		"statuses": []string{"NOT_A_STATUS"},
	}, &out)
	if len(out.Errors) == 0 {
		t.Fatalf("invalid enum response had no errors: %#v", out)
	}
}

func TestHandlerGraphQLSetFlowPhaseStatusCompletesPhaseAndReturnsUpdatedFlow(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "mutation-flow",
		Title:        "Mutation Flow",
		Instructions: "complete the plan phase",
		RepoPath:     t.TempDir(),
	})
	handler := newFlowGraphQLHandler(t, store)

	var out setPhaseResponse
	postGraphQL(t, handler, setFlowPhaseStatusMutation, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "plan",
		"status":  "COMPLETED",
		"summary": "Plan saved.",
	}}, &out)

	if len(out.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", out.Errors)
	}
	payload := out.Data.SetFlowPhaseStatus
	if payload.Phase.PhaseID != "plan" || payload.Phase.StatusRaw != flowstore.PhaseCompleted || payload.Phase.Summary != "Plan saved." {
		t.Fatalf("payload phase = %#v, want completed plan with summary", payload.Phase)
	}
	if payload.Flow.StatusRaw != flowstore.StatusInProgress {
		t.Fatalf("flow status = %q, want in_progress", payload.Flow.StatusRaw)
	}
	if payload.Flow.NextLaunchablePhase == nil || payload.Flow.NextLaunchablePhase.PhaseID != "plan-review" {
		t.Fatalf("next launchable phase = %#v, want plan-review", payload.Flow.NextLaunchablePhase)
	}
	read, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	plan := graphQLPhaseByID(t, read, "plan")
	review := graphQLPhaseByID(t, read, "plan-review")
	if plan.Status != flowstore.PhaseCompleted || plan.Summary != "Plan saved." || review.Status != flowstore.PhaseReady {
		t.Fatalf("persisted phases = plan %#v review %#v, want completed plan and ready review", plan, review)
	}
}

func TestHandlerGraphQLSetFlowPhaseStatusPreservesStoreValidationErrors(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "invalid-transition-flow",
		Title:        "Invalid Transition Flow",
		Instructions: "reject impossible phase transitions",
		RepoPath:     t.TempDir(),
	})
	before, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	handler := newFlowGraphQLHandler(t, store)

	var out setPhaseResponse
	postGraphQL(t, handler, setFlowPhaseStatusMutation, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "plan-review",
		"status":  "COMPLETED",
		"outcome": "approved",
	}}, &out)

	if !graphQLErrorsContain(out.Errors, "invalid phase transition pending -> completed") ||
		!graphQLErrorsContain(out.Errors, "allowed from pending: skipped") ||
		!graphQLErrorsContain(out.Errors, `set flow phase status "invalid-transition-flow"/"plan-review"`) {
		t.Fatalf("GraphQL errors = %#v, want store transition detail with context", out.Errors)
	}
	after, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read() after error = %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("record changed after rejected mutation\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestHandlerGraphQLSetFlowPhaseStatusUsesPlanReviewOutcomeRules(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "plan-review-flow",
		Title:        "Plan Review Flow",
		Instructions: "gate implementation by review outcome",
		RepoPath:     t.TempDir(),
	})
	record = mustSetGraphQLPhase(t, store, record, flowstore.PhaseUpdate{
		PhaseID: "plan",
		Status:  flowstore.PhaseCompleted,
	})
	before, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	handler := newFlowGraphQLHandler(t, store)

	var rejected setPhaseResponse
	postGraphQL(t, handler, setFlowPhaseStatusMutation, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "plan-review",
		"status":  "COMPLETED",
		"outcome": "approved_with_concerns",
	}}, &rejected)
	if !graphQLErrorsContain(rejected.Errors, "approved_with_concerns requires notes") {
		t.Fatalf("GraphQL errors = %#v, want plan-review notes validation", rejected.Errors)
	}
	afterRejected, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read() after rejection = %v", err)
	}
	if !reflect.DeepEqual(before, afterRejected) {
		t.Fatalf("record changed after rejected plan-review outcome\nbefore: %#v\nafter:  %#v", before, afterRejected)
	}

	var accepted setPhaseResponse
	postGraphQL(t, handler, setFlowPhaseStatusMutation, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "plan-review",
		"status":  "COMPLETED",
		"outcome": "approved_with_concerns",
		"notes":   "Implementation can proceed if rollout is staged.",
	}}, &accepted)
	if len(accepted.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", accepted.Errors)
	}
	payload := accepted.Data.SetFlowPhaseStatus
	if payload.Phase.Outcome != flowstore.OutcomeApprovedWithConcerns || payload.Phase.Notes == "" {
		t.Fatalf("plan-review payload = %#v, want approved_with_concerns with notes", payload.Phase)
	}
	implementation := graphQLResponsePhaseByID(t, payload.Flow.Phases, "implementation")
	if implementation.StatusRaw != flowstore.PhaseReady {
		t.Fatalf("implementation status = %q, want ready after approved review", implementation.StatusRaw)
	}
}

func TestHandlerGraphQLSetFlowPhaseStatusHandlesPlanReviewOutcomeBranches(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         string
		outcome        string
		notes          string
		wantFlowStatus string
		wantImplStatus string
	}{
		{
			name:           "approved",
			status:         "COMPLETED",
			outcome:        flowstore.OutcomeApproved,
			wantFlowStatus: flowstore.StatusInProgress,
			wantImplStatus: flowstore.PhaseReady,
		},
		{
			name:           "changes requested",
			status:         "NEEDS_ATTENTION",
			outcome:        flowstore.OutcomeChangesRequested,
			notes:          "Revise the plan before implementation.",
			wantFlowStatus: flowstore.StatusNeedsAttention,
			wantImplStatus: flowstore.PhasePending,
		},
		{
			name:           "blocked",
			status:         "BLOCKED",
			outcome:        flowstore.OutcomeBlocked,
			notes:          "Waiting on product input.",
			wantFlowStatus: flowstore.StatusBlocked,
			wantImplStatus: flowstore.PhasePending,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newFlowGraphQLStore(t)
			record := createGraphQLFlow(t, store, flowstore.FlowRecord{
				FlowID:       strings.ReplaceAll(tc.name, " ", "-") + "-review-flow",
				Title:        "Plan Review Outcome Flow",
				Instructions: "gate implementation by review outcome",
				RepoPath:     t.TempDir(),
			})
			record = mustSetGraphQLPhase(t, store, record, flowstore.PhaseUpdate{
				PhaseID: "plan",
				Status:  flowstore.PhaseCompleted,
			})
			handler := newFlowGraphQLHandler(t, store)

			var out setPhaseResponse
			postGraphQL(t, handler, setFlowPhaseStatusMutation, map[string]any{"input": map[string]any{
				"flowId":  record.FlowID,
				"phaseId": "plan-review",
				"status":  tc.status,
				"outcome": tc.outcome,
				"notes":   tc.notes,
			}}, &out)

			if len(out.Errors) != 0 {
				t.Fatalf("GraphQL errors: %#v", out.Errors)
			}
			payload := out.Data.SetFlowPhaseStatus
			if payload.Flow.StatusRaw != tc.wantFlowStatus || payload.Phase.Outcome != tc.outcome {
				t.Fatalf("payload flow status %q outcome %q, want %q/%q", payload.Flow.StatusRaw, payload.Phase.Outcome, tc.wantFlowStatus, tc.outcome)
			}
			implementation := graphQLResponsePhaseByID(t, payload.Flow.Phases, "implementation")
			if implementation.StatusRaw != tc.wantImplStatus {
				t.Fatalf("implementation status = %q, want %q", implementation.StatusRaw, tc.wantImplStatus)
			}
		})
	}
}

func TestHandlerGraphQLSetFlowPhaseStatusUsesRecoveryRestartRules(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "restart-flow",
		Title:        "Restart Flow",
		Instructions: "restart recovery states before completion",
		RepoPath:     t.TempDir(),
	})
	record = mustSetGraphQLPhase(t, store, record, flowstore.PhaseUpdate{
		PhaseID: "plan",
		Status:  flowstore.PhaseNeedsAttention,
		Outcome: "needs_attention",
		Notes:   "Needs another planning pass.",
	})
	beforeRejectedComplete, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read() before rejected complete = %v", err)
	}
	handler := newFlowGraphQLHandler(t, store)

	var completeOut setPhaseResponse
	postGraphQL(t, handler, setFlowPhaseStatusMutation, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "plan",
		"status":  "COMPLETED",
	}}, &completeOut)
	if !graphQLErrorsContain(completeOut.Errors, "invalid phase transition needs_attention -> completed") ||
		!graphQLErrorsContain(completeOut.Errors, "restart with --status running --notes before completing") {
		t.Fatalf("GraphQL errors = %#v, want recovery completion guidance", completeOut.Errors)
	}
	afterRejectedComplete, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read() after rejected complete = %v", err)
	}
	if !reflect.DeepEqual(beforeRejectedComplete, afterRejectedComplete) {
		t.Fatalf("record changed after rejected recovery completion\nbefore: %#v\nafter:  %#v", beforeRejectedComplete, afterRejectedComplete)
	}

	beforeRejectedRestart, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read() before rejected restart = %v", err)
	}
	var restartWithoutNotes setPhaseResponse
	postGraphQL(t, handler, setFlowPhaseStatusMutation, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "plan",
		"status":  "RUNNING",
	}}, &restartWithoutNotes)
	if !graphQLErrorsContain(restartWithoutNotes.Errors, "restarting needs_attention phase requires notes") {
		t.Fatalf("GraphQL errors = %#v, want restart notes validation", restartWithoutNotes.Errors)
	}
	afterRejectedRestart, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read() after rejected restart = %v", err)
	}
	if !reflect.DeepEqual(beforeRejectedRestart, afterRejectedRestart) {
		t.Fatalf("record changed after rejected recovery restart\nbefore: %#v\nafter:  %#v", beforeRejectedRestart, afterRejectedRestart)
	}

	var restart setPhaseResponse
	postGraphQL(t, handler, setFlowPhaseStatusMutation, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "plan",
		"status":  "RUNNING",
		"notes":   "Rerunning planning after feedback.",
	}}, &restart)
	if len(restart.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", restart.Errors)
	}
	if restart.Data.SetFlowPhaseStatus.Phase.StatusRaw != flowstore.PhaseRunning ||
		restart.Data.SetFlowPhaseStatus.Phase.Outcome != "" ||
		restart.Data.SetFlowPhaseStatus.Phase.Notes != "Rerunning planning after feedback." {
		t.Fatalf("restart payload phase = %#v, want running with cleared outcome and notes", restart.Data.SetFlowPhaseStatus.Phase)
	}

	var emptyMetadata setPhaseResponse
	postGraphQL(t, handler, setFlowPhaseStatusMutation, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "plan",
		"status":  "RUNNING",
		"outcome": "",
		"notes":   "",
		"summary": "",
	}}, &emptyMetadata)
	if len(emptyMetadata.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", emptyMetadata.Errors)
	}
	if emptyMetadata.Data.SetFlowPhaseStatus.Phase.Notes != "Rerunning planning after feedback." {
		t.Fatalf("empty optional notes cleared existing notes: %#v", emptyMetadata.Data.SetFlowPhaseStatus.Phase)
	}
}

func TestHandlerGraphQLSetFlowPhaseStatusHandlesInputEnumValidation(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "enum-flow",
		Title:        "Enum Flow",
		Instructions: "distinguish GraphQL enum validation from store validation",
		RepoPath:     t.TempDir(),
	})
	handler := newFlowGraphQLHandler(t, store)

	for _, status := range []string{"NOT_A_STATUS", "READY", "PENDING"} {
		t.Run(status, func(t *testing.T) {
			before, err := store.Read(record.FlowID)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			var out struct {
				Data   any   `json:"data"`
				Errors []any `json:"errors"`
			}
			postGraphQLWithStatus(t, handler, http.StatusUnprocessableEntity, setFlowPhaseStatusMutation, map[string]any{"input": map[string]any{
				"flowId":  record.FlowID,
				"phaseId": "plan",
				"status":  status,
			}}, &out)
			if len(out.Errors) == 0 {
				t.Fatalf("invalid enum response had no errors: %#v", out)
			}
			after, err := store.Read(record.FlowID)
			if err != nil {
				t.Fatalf("Read() after %s = %v", status, err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("record changed after rejected %s mutation", status)
			}
		})
	}
}

func TestHandlerGraphQLSetFlowPhaseStatusReturnsRuntimeJobWhenAvailable(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "runtime-flow",
		Title:        "Runtime Flow",
		Instructions: "return runtime job details in mutation payloads",
		RepoPath:     t.TempDir(),
	})
	handler := newFlowGraphQLHandlerWithRuntime(t, store, staticRuntimeJobLookup{
		job: &flowquery.RuntimeJob{ID: "job-plan", PhaseID: "plan", Status: "running"},
	})

	var out struct {
		Data struct {
			SetFlowPhaseStatus struct {
				Flow struct {
					Phases []struct {
						PhaseID          string             `json:"phaseId"`
						ActiveRuntimeJob *graphQLRuntimeJob `json:"activeRuntimeJob"`
					} `json:"phases"`
				} `json:"flow"`
				Phase struct {
					PhaseID          string             `json:"phaseId"`
					ActiveRuntimeJob *graphQLRuntimeJob `json:"activeRuntimeJob"`
				} `json:"phase"`
			} `json:"setFlowPhaseStatus"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: SetFlowPhaseStatusInput!) {
		setFlowPhaseStatus(input: $input) {
			flow { phases { phaseId activeRuntimeJob { id phaseId status } } }
			phase { phaseId activeRuntimeJob { id phaseId status } }
		}
	}`, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "plan",
		"status":  "RUNNING",
	}}, &out)
	if len(out.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", out.Errors)
	}
	assertGraphQLRuntimeJob(t, out.Data.SetFlowPhaseStatus.Phase.ActiveRuntimeJob, graphQLRuntimeJob{
		ID:      "job-plan",
		PhaseID: "plan",
		Status:  "running",
	})
	var flowPhaseRuntimeJob *graphQLRuntimeJob
	for _, phase := range out.Data.SetFlowPhaseStatus.Flow.Phases {
		if phase.PhaseID == "plan" {
			flowPhaseRuntimeJob = phase.ActiveRuntimeJob
		}
	}
	assertGraphQLRuntimeJob(t, flowPhaseRuntimeJob, graphQLRuntimeJob{
		ID:      "job-plan",
		PhaseID: "plan",
		Status:  "running",
	})
}

func TestHandlerGraphQLSetFlowPhaseStatusIgnoresRuntimeLookupFailureAfterPersist(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "runtime-failure-flow",
		Title:        "Runtime Failure Flow",
		Instructions: "return persisted mutation result even if runtime visibility fails",
		RepoPath:     t.TempDir(),
	})
	handler := newFlowGraphQLHandlerWithRuntime(t, store, failingRuntimeJobLookup{})

	var out struct {
		Data struct {
			SetFlowPhaseStatus struct {
				Flow struct {
					Phases []struct {
						PhaseID          string `json:"phaseId"`
						StatusRaw        string `json:"statusRaw"`
						ActiveRuntimeJob any    `json:"activeRuntimeJob"`
					} `json:"phases"`
				} `json:"flow"`
				Phase graphQLPhase `json:"phase"`
			} `json:"setFlowPhaseStatus"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: SetFlowPhaseStatusInput!) {
		setFlowPhaseStatus(input: $input) {
			flow { phases { phaseId statusRaw activeRuntimeJob { id } } }
			phase { phaseId statusRaw outcome notes summary }
		}
	}`, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "plan",
		"status":  "COMPLETED",
	}}, &out)
	if len(out.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", out.Errors)
	}
	if out.Data.SetFlowPhaseStatus.Phase.StatusRaw != flowstore.PhaseCompleted {
		t.Fatalf("payload phase = %#v, want completed", out.Data.SetFlowPhaseStatus.Phase)
	}
	read, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if phase := graphQLPhaseByID(t, read, "plan"); phase.Status != flowstore.PhaseCompleted {
		t.Fatalf("persisted plan phase = %#v, want completed", phase)
	}
}

func TestHandlerGraphQLSetFlowPhaseStatusSyncsLinkedPlanPhase(t *testing.T) {
	store, root := newFlowGraphQLStore(t)
	planStore, err := planstore.NewStore(planstore.StoreOptions{Root: root})
	if err != nil {
		t.Fatalf("plan NewStore() error = %v", err)
	}
	repoPath := filepath.Join(root, "repo")
	if _, err := planStore.Save(planstore.PlanRecord{
		PlanID:   "plan-1",
		Title:    "Linked Plan",
		Markdown: "Build the thing.",
		Status:   "in_progress",
		RepoPath: repoPath,
		Phases: []planstore.PlanPhase{{
			PhaseID: "implementation",
			Title:   "Implementation",
			Status:  "in_progress",
			Order:   3,
		}},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "linked-plan-flow",
		Title:        "Linked Plan Flow",
		Instructions: "sync the linked plan phase",
		RepoPath:     repoPath,
	})
	record, err = store.SetPlanLink(flowstore.PlanLinkUpdate{FlowID: record.FlowID, PlanID: "plan-1"})
	if err != nil {
		t.Fatalf("SetPlanLink() error = %v", err)
	}
	record = mustSetGraphQLPhase(t, store, record, flowstore.PhaseUpdate{PhaseID: "plan", Status: flowstore.PhaseCompleted})
	record = mustSetGraphQLPhase(t, store, record, flowstore.PhaseUpdate{PhaseID: "plan-review", Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved})
	record = mustSetGraphQLPhase(t, store, record, flowstore.PhaseUpdate{PhaseID: "implementation", Status: flowstore.PhaseRunning})
	handler := newFlowGraphQLHandler(t, store)

	var out setPhaseResponse
	postGraphQL(t, handler, setFlowPhaseStatusMutation, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "implementation",
		"status":  "COMPLETED",
		"summary": "Implementation finished.",
	}}, &out)
	if len(out.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", out.Errors)
	}
	plan, err := planStore.ReadMetadata("plan-1")
	if err != nil {
		t.Fatalf("ReadMetadata() error = %v", err)
	}
	phase := graphQLPlanPhaseByID(t, plan, "implementation")
	if phase.Status != "completed" {
		t.Fatalf("linked plan phase status = %q, want completed", phase.Status)
	}
}

func TestHandlerGraphQLReturnsNullEnumForUnknownPersistedPhaseStatus(t *testing.T) {
	store, root := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "legacy-flow",
		Title:        "Legacy Flow",
		Instructions: "legacy phase",
		RepoPath:     t.TempDir(),
	})
	mutateFlowMeta(t, root, record.FlowID, func(meta map[string]any) {
		phases := meta["phases"].([]any)
		phase := phases[0].(map[string]any)
		phase["status"] = "legacy_waiting"
	})

	handler := newFlowGraphQLHandler(t, store)
	var out struct {
		Data struct {
			Flow *struct {
				Phases []struct {
					PhaseID   string  `json:"phaseId"`
					Status    *string `json:"status"`
					StatusRaw string  `json:"statusRaw"`
				} `json:"phases"`
			} `json:"flow"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `query($id: ID!) {
		flow(id: $id) { phases { phaseId status statusRaw } }
	}`, map[string]any{"id": record.FlowID}, &out)
	if len(out.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", out.Errors)
	}
	if out.Data.Flow == nil || len(out.Data.Flow.Phases) == 0 {
		t.Fatalf("missing flow phases in response: %#v", out.Data.Flow)
	}
	if out.Data.Flow.Phases[0].Status != nil || out.Data.Flow.Phases[0].StatusRaw != "legacy_waiting" {
		t.Fatalf("legacy phase status = %#v raw %q, want nil/raw legacy_waiting", out.Data.Flow.Phases[0].Status, out.Data.Flow.Phases[0].StatusRaw)
	}
}

func TestHandlerGraphQLLaunchFlowPhaseStartsRuntimeJobAndMarksPhaseRunning(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "launch-flow",
		Title:        "Launch Flow",
		Instructions: "launch implementation",
		RepoPath:     t.TempDir(),
		WorktreePath: t.TempDir(),
		Branch:       "flow/launch",
		Commit:       "abc123",
	})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan-review", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved})
	if phase := phaseByIDForTest(record, "implementation"); phase.Status != flowstore.PhaseReady {
		t.Fatalf("implementation status = %q, want ready", phase.Status)
	}
	var launched []actions.AgentLaunchContext
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			launched = append(launched, launch)
			return exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'done\\n'"), nil
		},
		UpdatePhase: store.SetPhase,
	})
	handler := newFlowGraphQLHandlerWithOptions(t, server.HandlerOptions{
		FlowStore:            store,
		RuntimeJobs:          registry,
		RuntimeStarter:       registry,
		AgentCommand:         "codex",
		CodexReasoningEffort: "high",
		StateRoot:            t.TempDir(),
	})

	var out struct {
		Data struct {
			LaunchFlowPhase struct {
				FlowID   string `json:"flowId"`
				PhaseID  string `json:"phaseId"`
				LaunchID string `json:"launchId"`
				Job      struct {
					ID        string `json:"id"`
					LaunchID  string `json:"launchId"`
					FlowID    string `json:"flowId"`
					PhaseID   string `json:"phaseId"`
					Status    string `json:"status"`
					CreatedAt string `json:"createdAt"`
				} `json:"job"`
			} `json:"launchFlowPhase"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: LaunchFlowPhaseInput!) {
		launchFlowPhase(input: $input) {
			flowId
			phaseId
			launchId
			job { id launchId flowId phaseId status createdAt }
		}
	}`, map[string]any{"input": map[string]any{"flowId": record.FlowID, "phaseId": "implementation"}}, &out)
	if len(out.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", out.Errors)
	}
	payload := out.Data.LaunchFlowPhase
	if payload.FlowID != record.FlowID || payload.PhaseID != "implementation" || payload.LaunchID == "" {
		t.Fatalf("payload = %#v, want launch metadata", payload)
	}
	if payload.Job.ID == "" || payload.Job.LaunchID != payload.LaunchID || payload.Job.Status != string(runtimejobs.StatusQueued) {
		t.Fatalf("job payload = %#v, want queued runtime job", payload.Job)
	}
	updated, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read updated flow: %v", err)
	}
	phase := phaseByIDForTest(updated, "implementation")
	if phase.Status != flowstore.PhaseRunning || flowstore.LatestPhaseLaunchID(phase) != payload.LaunchID {
		t.Fatalf("implementation phase = %#v, want running with launch ID %q", phase, payload.LaunchID)
	}
	var duplicate struct {
		Data   any   `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: LaunchFlowPhaseInput!) {
		launchFlowPhase(input: $input) { launchId }
	}`, map[string]any{"input": map[string]any{"flowId": record.FlowID, "phaseId": "implementation"}}, &duplicate)
	if len(duplicate.Errors) == 0 {
		t.Fatalf("duplicate launch response had no errors: %#v", duplicate)
	}
	final := waitForRuntimeJobStatus(t, registry, payload.Job.ID, runtimejobs.StatusSucceeded)
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("final job = %#v, want zero exit", final)
	}
	updated, err = store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read after job: %v", err)
	}
	if phase := phaseByIDForTest(updated, "implementation"); phase.Status != flowstore.PhaseRunning {
		t.Fatalf("zero-exit job changed phase to %q, want running", phase.Status)
	}
	if len(launched) != 1 ||
		launched[0].Command != "codex" ||
		launched[0].ReasoningEffort != "high" ||
		!launched[0].Headless ||
		launched[0].Embedded ||
		!strings.Contains(launched[0].InitialPrompt, "Use the commit skill before completing this phase.") {
		t.Fatalf("launch context = %#v, want headless codex implementation launch", launched)
	}

	var query struct {
		Data struct {
			Flow *struct {
				Phases []struct {
					PhaseID          string `json:"phaseId"`
					ActiveRuntimeJob *struct {
						ID       string `json:"id"`
						LaunchID string `json:"launchId"`
						Status   string `json:"status"`
						LogTail  string `json:"logTail"`
					} `json:"activeRuntimeJob"`
				} `json:"phases"`
			} `json:"flow"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `query($id: ID!) {
		flow(id: $id) {
			phases {
				phaseId
				activeRuntimeJob { id launchId status logTail }
			}
		}
	}`, map[string]any{"id": record.FlowID}, &query)
	if len(query.Errors) != 0 {
		t.Fatalf("GraphQL query errors: %#v", query.Errors)
	}
	job := activeRuntimeJobForTest(query.Data.Flow.Phases, "implementation")
	if job == nil || job.ID != payload.Job.ID || job.LaunchID != payload.LaunchID || job.Status != string(runtimejobs.StatusSucceeded) || !strings.Contains(job.LogTail, "done") {
		t.Fatalf("active runtime job = %#v, want completed visible job", job)
	}
}

func TestHandlerGraphQLLaunchFlowPhaseRejectsInvalidReasoningEffortBeforeStateChange(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "invalid-effort-flow",
		Title:        "Invalid Effort Flow",
		Instructions: "launch implementation",
		RepoPath:     t.TempDir(),
		WorktreePath: t.TempDir(),
	})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan-review", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved})
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			t.Fatalf("runtime command should not be built for invalid reasoning effort")
			return nil, nil
		},
		UpdatePhase: store.SetPhase,
	})
	handler := newFlowGraphQLHandlerWithOptions(t, server.HandlerOptions{
		FlowStore:      store,
		RuntimeJobs:    registry,
		RuntimeStarter: registry,
		AgentCommand:   "codex",
		StateRoot:      t.TempDir(),
	})

	var out struct {
		Data   any   `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: LaunchFlowPhaseInput!) {
		launchFlowPhase(input: $input) { launchId }
	}`, map[string]any{"input": map[string]any{
		"flowId":          record.FlowID,
		"phaseId":         "implementation",
		"reasoningEffort": "turbo",
	}}, &out)
	if !graphQLErrorsContain(out.Errors, `unsupported reasoning effort "turbo" for codex`) {
		t.Fatalf("GraphQL errors = %#v, want unsupported reasoning effort", out.Errors)
	}
	updated, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read updated flow: %v", err)
	}
	phase := phaseByIDForTest(updated, "implementation")
	if phase.Status != flowstore.PhaseReady || len(phase.LaunchIDs) != 0 {
		t.Fatalf("implementation phase = %#v, want ready with no launch IDs", phase)
	}
}

func TestHandlerGraphQLLaunchFlowPhaseStartErrorMarksPhaseNeedsAttention(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "start-error-flow",
		Title:        "Start Error Flow",
		Instructions: "launch implementation",
		RepoPath:     t.TempDir(),
		WorktreePath: t.TempDir(),
	})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan-review", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved})
	runtime := &startErrorRuntimeProvider{err: errors.New("runtime unavailable")}
	handler := newFlowGraphQLHandlerWithOptions(t, server.HandlerOptions{
		FlowStore:         store,
		RuntimeJobs:       runtime,
		RuntimeStarter:    runtime,
		RuntimeController: runtime,
		AgentCommand:      "codex",
		StateRoot:         t.TempDir(),
	})

	var out struct {
		Data   any   `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: LaunchFlowPhaseInput!) {
		launchFlowPhase(input: $input) { launchId }
	}`, map[string]any{"input": map[string]any{"flowId": record.FlowID, "phaseId": "implementation"}}, &out)
	if !graphQLErrorsContain(out.Errors, "runtime unavailable") {
		t.Fatalf("GraphQL errors = %#v, want runtime start failure", out.Errors)
	}
	updated, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read updated flow: %v", err)
	}
	phase := phaseByIDForTest(updated, "implementation")
	if phase.Status != flowstore.PhaseNeedsAttention ||
		phase.Outcome != "runtime_start_failed" ||
		!strings.Contains(phase.Notes, "runtime unavailable") ||
		len(phase.LaunchIDs) != 1 {
		t.Fatalf("implementation phase after start failure = %#v, want needs_attention with orphan launch noted", phase)
	}
}

func TestHandlerGraphQLCancelRuntimeJobStopsJobWithoutPhaseFailure(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "cancel-flow",
		Title:        "Cancel Flow",
		Instructions: "cancel implementation",
		RepoPath:     t.TempDir(),
		WorktreePath: t.TempDir(),
	})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan-review", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved})
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5"), nil
		},
		UpdatePhase: store.SetPhase,
	})
	defer registry.CancelAll()
	handler := newFlowGraphQLHandlerWithOptions(t, server.HandlerOptions{
		FlowStore:      store,
		RuntimeJobs:    registry,
		RuntimeStarter: registry,
		AgentCommand:   "codex",
		StateRoot:      t.TempDir(),
	})

	var launch struct {
		Data struct {
			LaunchFlowPhase struct {
				Job struct {
					ID string `json:"id"`
				} `json:"job"`
			} `json:"launchFlowPhase"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: LaunchFlowPhaseInput!) {
		launchFlowPhase(input: $input) { job { id } }
	}`, map[string]any{"input": map[string]any{"flowId": record.FlowID, "phaseId": "implementation"}}, &launch)
	if len(launch.Errors) != 0 || launch.Data.LaunchFlowPhase.Job.ID == "" {
		t.Fatalf("launch response = %#v errors %#v", launch.Data.LaunchFlowPhase, launch.Errors)
	}

	var cancel struct {
		Data struct {
			CancelRuntimeJob struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  string `json:"error"`
			} `json:"cancelRuntimeJob"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($id: ID!) {
		cancelRuntimeJob(id: $id) { id status error }
	}`, map[string]any{"id": launch.Data.LaunchFlowPhase.Job.ID}, &cancel)
	if len(cancel.Errors) != 0 {
		t.Fatalf("cancel errors: %#v", cancel.Errors)
	}
	if cancel.Data.CancelRuntimeJob.Status != string(runtimejobs.StatusCanceled) ||
		cancel.Data.CancelRuntimeJob.Error != "runtime job canceled" {
		t.Fatalf("cancel payload = %#v, want canceled", cancel.Data.CancelRuntimeJob)
	}
	final := waitForRuntimeJobStatus(t, registry, launch.Data.LaunchFlowPhase.Job.ID, runtimejobs.StatusCanceled)
	if final.Status != runtimejobs.StatusCanceled {
		t.Fatalf("final job = %#v, want canceled", final)
	}
	updated, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read after cancel: %v", err)
	}
	if phase := phaseByIDForTest(updated, "implementation"); phase.Status != flowstore.PhaseReady {
		t.Fatalf("cancel changed phase to %q, want ready for retry", phase.Status)
	}

	var retry struct {
		Data struct {
			LaunchFlowPhase struct {
				Job struct {
					ID string `json:"id"`
				} `json:"job"`
			} `json:"launchFlowPhase"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: LaunchFlowPhaseInput!) {
		launchFlowPhase(input: $input) { job { id } }
	}`, map[string]any{"input": map[string]any{"flowId": record.FlowID, "phaseId": "implementation"}}, &retry)
	if len(retry.Errors) != 0 || retry.Data.LaunchFlowPhase.Job.ID == "" {
		t.Fatalf("retry launch response = %#v errors %#v", retry.Data.LaunchFlowPhase, retry.Errors)
	}

	var repeatedCancel struct {
		Data struct {
			CancelRuntimeJob struct {
				Status string `json:"status"`
			} `json:"cancelRuntimeJob"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($id: ID!) {
		cancelRuntimeJob(id: $id) { status }
	}`, map[string]any{"id": launch.Data.LaunchFlowPhase.Job.ID}, &repeatedCancel)
	if len(repeatedCancel.Errors) != 0 || repeatedCancel.Data.CancelRuntimeJob.Status != string(runtimejobs.StatusCanceled) {
		t.Fatalf("repeated cancel response = %#v errors %#v", repeatedCancel.Data.CancelRuntimeJob, repeatedCancel.Errors)
	}
	updated, err = store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read after repeated cancel: %v", err)
	}
	if phase := phaseByIDForTest(updated, "implementation"); phase.Status != flowstore.PhaseRunning {
		t.Fatalf("repeated old-job cancel changed phase to %q, want retry still running", phase.Status)
	}
}

func TestHandlerGraphQLCancelRuntimeJobWithAttachedSessionDoesNotReportResetError(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "cancel-attached-flow",
		Title:        "Cancel Attached Flow",
		Instructions: "cancel implementation after hook attach",
		RepoPath:     t.TempDir(),
		WorktreePath: t.TempDir(),
	})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan-review", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved})
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5"), nil
		},
		UpdatePhase: store.SetPhase,
	})
	defer registry.CancelAll()
	handler := newFlowGraphQLHandlerWithOptions(t, server.HandlerOptions{
		FlowStore:      store,
		RuntimeJobs:    registry,
		RuntimeStarter: registry,
		AgentCommand:   "codex",
		StateRoot:      t.TempDir(),
	})

	var launch struct {
		Data struct {
			LaunchFlowPhase struct {
				LaunchID string `json:"launchId"`
				Job      struct {
					ID string `json:"id"`
				} `json:"job"`
			} `json:"launchFlowPhase"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: LaunchFlowPhaseInput!) {
		launchFlowPhase(input: $input) { launchId job { id } }
	}`, map[string]any{"input": map[string]any{"flowId": record.FlowID, "phaseId": "implementation"}}, &launch)
	if len(launch.Errors) != 0 || launch.Data.LaunchFlowPhase.Job.ID == "" || launch.Data.LaunchFlowPhase.LaunchID == "" {
		t.Fatalf("launch response = %#v errors %#v", launch.Data.LaunchFlowPhase, launch.Errors)
	}
	_, err := store.AttachSession(flowstore.SessionAttachUpdate{
		FlowID:  record.FlowID,
		PhaseID: "implementation",
		Session: flowstore.Session{
			Provider:  "codex",
			SessionID: "session-1",
			LaunchID:  launch.Data.LaunchFlowPhase.LaunchID,
		},
	})
	if err != nil {
		t.Fatalf("AttachSession() error = %v", err)
	}

	var cancel struct {
		Data struct {
			CancelRuntimeJob struct {
				Status string `json:"status"`
			} `json:"cancelRuntimeJob"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($id: ID!) {
		cancelRuntimeJob(id: $id) { status }
	}`, map[string]any{"id": launch.Data.LaunchFlowPhase.Job.ID}, &cancel)
	if len(cancel.Errors) != 0 || cancel.Data.CancelRuntimeJob.Status != string(runtimejobs.StatusCanceled) {
		t.Fatalf("cancel response = %#v errors %#v", cancel.Data.CancelRuntimeJob, cancel.Errors)
	}
	updated, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read after cancel: %v", err)
	}
	if phase := phaseByIDForTest(updated, "implementation"); phase.Status != flowstore.PhaseRunning || flowstore.PhaseAwaitingSession(phase) {
		t.Fatalf("attached cancel phase = %#v, want running with attached session", phase)
	}
}

func TestHandlerGraphQLCancelRuntimeJobAfterPhaseAdvancedSkipsReset(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "cancel-advanced-flow",
		Title:        "Cancel Advanced Flow",
		Instructions: "cancel after agent updated phase",
		RepoPath:     t.TempDir(),
		WorktreePath: t.TempDir(),
	})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted})
	record = completeGraphQLPhase(t, store, record.FlowID, "plan-review", flowstore.PhaseUpdate{Status: flowstore.PhaseCompleted, Outcome: flowstore.OutcomeApproved})
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5"), nil
		},
		UpdatePhase: store.SetPhase,
	})
	defer registry.CancelAll()
	handler := newFlowGraphQLHandlerWithOptions(t, server.HandlerOptions{
		FlowStore:      store,
		RuntimeJobs:    registry,
		RuntimeStarter: registry,
		AgentCommand:   "codex",
		StateRoot:      t.TempDir(),
	})

	var launch struct {
		Data struct {
			LaunchFlowPhase struct {
				Job struct {
					ID string `json:"id"`
				} `json:"job"`
			} `json:"launchFlowPhase"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: LaunchFlowPhaseInput!) {
		launchFlowPhase(input: $input) { job { id } }
	}`, map[string]any{"input": map[string]any{"flowId": record.FlowID, "phaseId": "implementation"}}, &launch)
	if len(launch.Errors) != 0 || launch.Data.LaunchFlowPhase.Job.ID == "" {
		t.Fatalf("launch response = %#v errors %#v", launch.Data.LaunchFlowPhase, launch.Errors)
	}
	record, err := store.SetPhase(flowstore.PhaseUpdate{
		FlowID:  record.FlowID,
		PhaseID: "implementation",
		Status:  flowstore.PhaseCompleted,
		Summary: "Agent finished before cancel.",
	})
	if err != nil {
		t.Fatalf("SetPhase(implementation completed) error = %v", err)
	}

	var cancel struct {
		Data struct {
			CancelRuntimeJob struct {
				Status string `json:"status"`
			} `json:"cancelRuntimeJob"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($id: ID!) {
		cancelRuntimeJob(id: $id) { status }
	}`, map[string]any{"id": launch.Data.LaunchFlowPhase.Job.ID}, &cancel)
	if len(cancel.Errors) != 0 || cancel.Data.CancelRuntimeJob.Status != string(runtimejobs.StatusCanceled) {
		t.Fatalf("cancel response = %#v errors %#v", cancel.Data.CancelRuntimeJob, cancel.Errors)
	}
	updated, err := store.Read(record.FlowID)
	if err != nil {
		t.Fatalf("Read after cancel: %v", err)
	}
	if phase := phaseByIDForTest(updated, "implementation"); phase.Status != flowstore.PhaseCompleted || phase.Summary != "Agent finished before cancel." {
		t.Fatalf("implementation after cancel = %#v, want completed state preserved", phase)
	}
}

func TestHandlerGraphQLLaunchFlowPhaseRejectsCodexAppAndNonLaunchablePhase(t *testing.T) {
	store, _ := newFlowGraphQLStore(t)
	record := createGraphQLFlow(t, store, flowstore.FlowRecord{
		FlowID:       "reject-flow",
		Title:        "Reject Flow",
		Instructions: "reject launch",
		RepoPath:     t.TempDir(),
	})
	handler := newFlowGraphQLHandlerWithOptions(t, server.HandlerOptions{
		FlowStore:    store,
		AgentCommand: "codex",
	})

	var codexApp struct {
		Data   any   `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: LaunchFlowPhaseInput!) {
		launchFlowPhase(input: $input) { launchId }
	}`, map[string]any{"input": map[string]any{
		"flowId":       record.FlowID,
		"phaseId":      "plan",
		"agentCommand": "codex-app",
	}}, &codexApp)
	if len(codexApp.Errors) == 0 {
		t.Fatalf("codex-app launch response had no errors: %#v", codexApp)
	}

	var pending struct {
		Data   any   `json:"data"`
		Errors []any `json:"errors"`
	}
	postGraphQL(t, handler, `mutation($input: LaunchFlowPhaseInput!) {
		launchFlowPhase(input: $input) { launchId }
	}`, map[string]any{"input": map[string]any{
		"flowId":  record.FlowID,
		"phaseId": "implementation",
	}}, &pending)
	if len(pending.Errors) == 0 {
		t.Fatalf("pending implementation launch response had no errors: %#v", pending)
	}
}

func newFlowGraphQLStore(t *testing.T) (*flowstore.Store, string) {
	t.Helper()
	root := t.TempDir()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	store, err := flowstore.NewStore(flowstore.StoreOptions{
		Root: root,
		Now: func() time.Time {
			now = now.Add(time.Minute)
			return now
		},
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store, root
}

func newFlowGraphQLHandler(t *testing.T, reader server.FlowStore) http.Handler {
	t.Helper()
	return newFlowGraphQLHandlerWithRuntime(t, reader, nil)
}

func newFlowGraphQLHandlerWithRuntime(t *testing.T, reader server.FlowStore, runtimeJobs flowquery.RuntimeJobLookup) http.Handler {
	t.Helper()
	return newFlowGraphQLHandlerWithOptions(t, server.HandlerOptions{
		Token:        "test-token",
		ListenerHost: "127.0.0.1",
		ListenerPort: "4321",
		FlowStore:    reader,
		RuntimeJobs:  runtimeJobs,
	})
}

func newFlowGraphQLHandlerWithOptions(t *testing.T, opts server.HandlerOptions) http.Handler {
	t.Helper()
	opts.Token = "test-token"
	opts.ListenerHost = "127.0.0.1"
	opts.ListenerPort = "4321"
	handler, err := server.NewHandler(opts)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler
}

type staticRuntimeJobLookup struct {
	job *flowquery.RuntimeJob
}

type staticFlowCreator struct {
	input  graph.CreateFlowInput
	record flowstore.FlowRecord
}

func (c *staticFlowCreator) CreateFlow(_ context.Context, input graph.CreateFlowInput) (flowstore.FlowRecord, error) {
	c.input = input
	return c.record, nil
}

func (lookup staticRuntimeJobLookup) RuntimeStateKnown() bool {
	return true
}

func (lookup staticRuntimeJobLookup) ActiveRuntimeJob(_ flowstore.FlowRecord, phase flowstore.FlowPhase) (*flowquery.RuntimeJob, error) {
	if lookup.job != nil && lookup.job.PhaseID == phase.PhaseID {
		return lookup.job, nil
	}
	return nil, nil
}

type failingRuntimeJobLookup struct{}

func (failingRuntimeJobLookup) RuntimeStateKnown() bool {
	return true
}

func (failingRuntimeJobLookup) ActiveRuntimeJob(flowstore.FlowRecord, flowstore.FlowPhase) (*flowquery.RuntimeJob, error) {
	return nil, errors.New("runtime lookup failed")
}

type startErrorRuntimeProvider struct {
	err error
}

func (p *startErrorRuntimeProvider) RuntimeStateKnown() bool {
	return true
}

func (p *startErrorRuntimeProvider) ActiveRuntimeJob(flowstore.FlowRecord, flowstore.FlowPhase) (*flowquery.RuntimeJob, error) {
	return nil, nil
}

func (p *startErrorRuntimeProvider) Start(context.Context, runtimejobs.StartRequest) (runtimejobs.Snapshot, error) {
	return runtimejobs.Snapshot{}, p.err
}

func (p *startErrorRuntimeProvider) Cancel(string) runtimejobs.CancelResult {
	return runtimejobs.CancelResult{}
}

const setFlowPhaseStatusMutation = `mutation($input: SetFlowPhaseStatusInput!) {
	setFlowPhaseStatus(input: $input) {
		flow {
			id
			statusRaw
			nextLaunchablePhase { phaseId statusRaw }
			phases { phaseId statusRaw outcome notes summary }
		}
		phase { phaseId statusRaw outcome notes summary }
	}
}`

type setPhaseResponse struct {
	Data struct {
		SetFlowPhaseStatus struct {
			Flow struct {
				ID                  string `json:"id"`
				StatusRaw           string `json:"statusRaw"`
				NextLaunchablePhase *struct {
					PhaseID   string `json:"phaseId"`
					StatusRaw string `json:"statusRaw"`
				} `json:"nextLaunchablePhase"`
				Phases []graphQLPhase `json:"phases"`
			} `json:"flow"`
			Phase graphQLPhase `json:"phase"`
		} `json:"setFlowPhaseStatus"`
	} `json:"data"`
	Errors []any `json:"errors"`
}

type graphQLPhase struct {
	PhaseID   string `json:"phaseId"`
	StatusRaw string `json:"statusRaw"`
	Outcome   string `json:"outcome"`
	Notes     string `json:"notes"`
	Summary   string `json:"summary"`
}

type graphQLRuntimeJob struct {
	ID      string `json:"id"`
	PhaseID string `json:"phaseId"`
	Status  string `json:"status"`
}

func assertGraphQLRuntimeJob(t *testing.T, got *graphQLRuntimeJob, want graphQLRuntimeJob) {
	t.Helper()
	if got == nil {
		t.Fatalf("runtime job is nil, want %#v", want)
	}
	if *got != want {
		t.Fatalf("runtime job = %#v, want %#v", *got, want)
	}
}

func mustSetGraphQLPhase(t *testing.T, store *flowstore.Store, record flowstore.FlowRecord, update flowstore.PhaseUpdate) flowstore.FlowRecord {
	t.Helper()
	update.FlowID = record.FlowID
	updated, err := store.SetPhase(update)
	if err != nil {
		t.Fatalf("SetPhase(%s %s) error = %v", update.PhaseID, update.Status, err)
	}
	return updated
}

func graphQLPhaseByID(t *testing.T, record flowstore.FlowRecord, phaseID string) flowstore.FlowPhase {
	t.Helper()
	for _, phase := range record.Phases {
		if phase.PhaseID == phaseID {
			return phase
		}
	}
	t.Fatalf("phase %q not found in %#v", phaseID, record.Phases)
	return flowstore.FlowPhase{}
}

func graphQLResponsePhaseByID(t *testing.T, phases []graphQLPhase, phaseID string) graphQLPhase {
	t.Helper()
	for _, phase := range phases {
		if phase.PhaseID == phaseID {
			return phase
		}
	}
	t.Fatalf("phase %q not found in %#v", phaseID, phases)
	return graphQLPhase{}
}

func graphQLPlanPhaseByID(t *testing.T, record planstore.PlanRecord, phaseID string) planstore.PlanPhase {
	t.Helper()
	for _, phase := range record.Phases {
		if phase.PhaseID == phaseID {
			return phase
		}
	}
	t.Fatalf("plan phase %q not found in %#v", phaseID, record.Phases)
	return planstore.PlanPhase{}
}

func graphQLErrorsContain(errors []any, want string) bool {
	for _, entry := range errors {
		if strings.Contains(graphQLErrorText(entry), want) {
			return true
		}
	}
	return false
}

func graphQLErrorText(entry any) string {
	switch value := entry.(type) {
	case map[string]any:
		message, _ := value["message"].(string)
		return message
	default:
		return ""
	}
}

func createGraphQLFlow(t *testing.T, store *flowstore.Store, record flowstore.FlowRecord) flowstore.FlowRecord {
	t.Helper()
	created, err := store.Create(record)
	if err != nil {
		t.Fatalf("Create flow %q: %v", record.FlowID, err)
	}
	return created
}

func postGraphQL(t *testing.T, handler http.Handler, query string, variables map[string]any, out any) {
	t.Helper()
	postGraphQLWithStatus(t, handler, http.StatusOK, query, variables, out)
}

func postGraphQLWithStatus(t *testing.T, handler http.Handler, wantStatus int, query string, variables map[string]any, out any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		t.Fatalf("marshal GraphQL request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4321/graphql", bytes.NewReader(body))
	req.Host = "127.0.0.1:4321"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != wantStatus {
		t.Fatalf("GraphQL status = %d, want %d; body:\n%s", res.Code, wantStatus, res.Body.String())
	}
	if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
		t.Fatalf("decode GraphQL response: %v\n%s", err, res.Body.String())
	}
}

func mutateFlowMeta(t *testing.T, root, flowID string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(root, "flows", flowID, "meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read flow meta: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("decode flow meta: %v", err)
	}
	mutate(meta)
	data, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("encode flow meta: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write flow meta: %v", err)
	}
}

func flowIDs(flows any) []string {
	value := reflect.ValueOf(flows)
	out := make([]string, 0, value.Len())
	for i := 0; i < value.Len(); i++ {
		out = append(out, value.Index(i).FieldByName("ID").String())
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func completeGraphQLPhase(t *testing.T, store *flowstore.Store, flowID, phaseID string, update flowstore.PhaseUpdate) flowstore.FlowRecord {
	t.Helper()
	update.FlowID = flowID
	update.PhaseID = phaseID
	record, err := store.SetPhase(update)
	if err != nil {
		t.Fatalf("SetPhase(%s): %v", phaseID, err)
	}
	return record
}

func phaseByIDForTest(record flowstore.FlowRecord, phaseID string) flowstore.FlowPhase {
	for _, phase := range record.Phases {
		if phase.PhaseID == phaseID {
			return phase
		}
	}
	return flowstore.FlowPhase{}
}

func waitForRuntimeJobStatus(t *testing.T, registry *runtimejobs.Registry, id string, status runtimejobs.Status) runtimejobs.Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := registry.Lookup(id)
		if ok && snapshot.Status == status {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, _ := registry.Lookup(id)
	t.Fatalf("job %s did not reach %s; latest = %#v", id, status, snapshot)
	return runtimejobs.Snapshot{}
}

func activeRuntimeJobForTest(phases []struct {
	PhaseID          string `json:"phaseId"`
	ActiveRuntimeJob *struct {
		ID       string `json:"id"`
		LaunchID string `json:"launchId"`
		Status   string `json:"status"`
		LogTail  string `json:"logTail"`
	} `json:"activeRuntimeJob"`
}, phaseID string) *struct {
	ID       string `json:"id"`
	LaunchID string `json:"launchId"`
	Status   string `json:"status"`
	LogTail  string `json:"logTail"`
} {
	for _, phase := range phases {
		if phase.PhaseID == phaseID {
			return phase.ActiveRuntimeJob
		}
	}
	return nil
}
