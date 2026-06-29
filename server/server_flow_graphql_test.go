package server_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/brian-bell/flowstate/server"
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

func newFlowGraphQLHandler(t *testing.T, reader server.FlowReader) http.Handler {
	t.Helper()
	return newFlowGraphQLHandlerWithOptions(t, server.HandlerOptions{
		Token:        "test-token",
		ListenerHost: "127.0.0.1",
		ListenerPort: "4321",
		FlowReader:   reader,
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
