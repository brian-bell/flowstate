package daemonclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/daemoncoords"
	"github.com/brian-bell/flowstate/planstore"
	"github.com/brian-bell/flowstate/server"
)

func TestClientListFlowsMapsPersistedRecordsAndRepoFilter(t *testing.T) {
	store, url := newClientGraphQLServer(t, "test-token")
	repoA := t.TempDir()
	repoB := t.TempDir()
	flowA := createClientFlow(t, store, flowstore.FlowRecord{
		FlowID:       "flow-a",
		Title:        "Flow A",
		Instructions: "instructions a",
		RepoPath:     repoA,
	})
	flowB := createClientFlow(t, store, flowstore.FlowRecord{
		FlowID:       "flow-b",
		Title:        "Flow B",
		Instructions: "instructions b",
		RepoPath:     repoB,
	})
	flowA, err := store.AddPhaseLaunchID(flowstore.PhaseLaunchUpdate{
		FlowID:   flowA.FlowID,
		PhaseID:  "plan",
		LaunchID: "launch-a",
	})
	if err != nil {
		t.Fatalf("AddPhaseLaunchID: %v", err)
	}
	started := time.Date(2026, 6, 30, 9, 30, 0, 0, time.UTC)
	flowA, err = store.AttachSession(flowstore.SessionAttachUpdate{
		FlowID:  flowA.FlowID,
		PhaseID: "plan",
		Session: flowstore.Session{
			Provider:       "codex",
			SessionID:      "session-a",
			LaunchID:       "launch-a",
			Status:         "running",
			StartedAt:      started,
			TranscriptPath: "/tmp/session-a.jsonl",
		},
	})
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	_ = flowB

	client, err := New(Options{EndpointURL: url, Token: "test-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	records, err := client.ListFlows(context.Background(), flowstore.FlowFilter{RepoPath: repoA})
	if err != nil {
		t.Fatalf("ListFlows: %v", err)
	}
	if len(records) != 1 || records[0].FlowID != flowA.FlowID {
		t.Fatalf("records = %#v, want only %s", records, flowA.FlowID)
	}
	phase := records[0].Phases[0]
	if len(phase.LaunchIDs) != 1 || phase.LaunchIDs[0] != "launch-a" ||
		len(phase.Sessions) != 1 || phase.Sessions[0].SessionID != "session-a" ||
		!phase.Sessions[0].StartedAt.Equal(started) {
		t.Fatalf("mapped phase = %#v, want persisted launch/session data", phase)
	}
}

func TestClientReadFlowAndNotFound(t *testing.T) {
	store, url := newClientGraphQLServer(t, "test-token")
	created := createClientFlow(t, store, flowstore.FlowRecord{
		FlowID:       "read-flow",
		Title:        "Read Flow",
		Instructions: "read instructions",
		RepoPath:     t.TempDir(),
	})
	client, err := New(Options{EndpointURL: url, Token: "test-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := client.ReadFlow(context.Background(), created.FlowID)
	if err != nil {
		t.Fatalf("ReadFlow existing: %v", err)
	}
	if got.FlowID != created.FlowID || got.Title != created.Title {
		t.Fatalf("ReadFlow = %#v, want %#v", got, created)
	}
	_, err = client.ReadFlow(context.Background(), "missing-flow")
	if !flowstore.IsNotFound(err) {
		t.Fatalf("ReadFlow missing error = %v, want flowstore not found", err)
	}
}

func TestClientMutationMethodsRoundTripThroughGraphQL(t *testing.T) {
	store, url, root := newClientGraphQLServerWithRoot(t, "test-token")
	record := createClientFlow(t, store, flowstore.FlowRecord{
		FlowID:       "mutation-flow",
		Title:        "Mutation Flow",
		Instructions: "mutation instructions",
		RepoPath:     t.TempDir(),
		Branch:       "flow/client",
	})
	if _, err := store.SetPhase(flowstore.PhaseUpdate{
		FlowID:  record.FlowID,
		PhaseID: "plan",
		Status:  flowstore.PhaseBlocked,
		Notes:   "blocked",
	}); err != nil {
		t.Fatalf("SetPhase blocked: %v", err)
	}
	client, err := New(Options{EndpointURL: url, Token: "test-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, restarted, err := client.RestartFlowPhase(context.Background(), flowstore.PhaseRestartUpdate{
		FlowID:  record.FlowID,
		PhaseID: "PLAN",
		Notes:   "rerun",
	})
	if err != nil {
		t.Fatalf("RestartFlowPhase: %v", err)
	}
	if restarted.PhaseID != "plan" || restarted.Status != flowstore.PhaseRunning {
		t.Fatalf("restarted phase = %#v", restarted)
	}
	_, completed, err := client.SetPhase(context.Background(), flowstore.PhaseUpdate{
		FlowID:  record.FlowID,
		PhaseID: "plan",
		Status:  flowstore.PhaseCompleted,
		Summary: "done",
	})
	if err != nil {
		t.Fatalf("SetPhase: %v", err)
	}
	if completed.PhaseID != "plan" || completed.Status != flowstore.PhaseCompleted || completed.Summary != "done" {
		t.Fatalf("completed phase = %#v", completed)
	}
	_, child, err := client.AddFlowChildPhase(context.Background(), flowstore.ChildPhaseUpdate{
		FlowID:        record.FlowID,
		ParentPhaseID: "implementation",
		PhaseID:       "implementation-client",
		Title:         "Client",
		Order:         12,
	})
	if err != nil {
		t.Fatalf("AddFlowChildPhase: %v", err)
	}
	if child.PhaseID != "implementation-client" || child.ParentPhaseID != "implementation" {
		t.Fatalf("child phase = %#v", child)
	}
	planID, planPath := saveClientPlan(t, root)
	linked, err := client.SetFlowPlanLink(context.Background(), flowstore.PlanLinkUpdate{
		FlowID:   record.FlowID,
		PlanID:   planID,
		PlanPath: planPath,
	})
	if err != nil {
		t.Fatalf("SetFlowPlanLink: %v", err)
	}
	if linked.PlanID != planID || linked.PlanPath != planPath {
		t.Fatalf("linked flow = %#v", linked)
	}
	_, pr, err := client.SetFlowPR(context.Background(), flowstore.PRUpdate{
		FlowID:     record.FlowID,
		Provider:   "github",
		Number:     17,
		URL:        "https://github.com/brian-bell/flowstate/pull/17",
		HeadBranch: "flow/client",
		BaseBranch: "main",
		Status:     "open",
	})
	if err != nil {
		t.Fatalf("SetFlowPR: %v", err)
	}
	if pr.Number != 17 || pr.HeadBranch != "flow/client" || pr.Status != "open" {
		t.Fatalf("PR = %#v", pr)
	}
	mergeRecord := createClientFlow(t, store, flowstore.FlowRecord{
		FlowID:       "merge-mutation-flow",
		Title:        "Merge Mutation Flow",
		Instructions: "merge mutation instructions",
		RepoPath:     t.TempDir(),
		Phases: []flowstore.FlowPhase{{
			PhaseID:   "merge",
			Title:     "Merge",
			Status:    flowstore.PhaseBlocked,
			Notes:     "waiting on CI",
			Order:     7,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}},
	})
	_, merge, err := client.SetFlowMerge(context.Background(), flowstore.MergeUpdate{
		FlowID: mergeRecord.FlowID,
		Status: flowstore.MergeBlocked,
	})
	if err != nil {
		t.Fatalf("SetFlowMerge: %v", err)
	}
	if merge.Status != flowstore.MergeBlocked {
		t.Fatalf("merge = %#v", merge)
	}
	auto, err := client.SetFlowAutoMode(context.Background(), flowstore.AutoModeUpdate{FlowID: record.FlowID, Enabled: false})
	if err != nil {
		t.Fatalf("SetFlowAutoMode: %v", err)
	}
	if auto.AutoMode {
		t.Fatalf("auto mode = true, want false")
	}
	deleted, err := client.DeleteFlow(context.Background(), record.FlowID)
	if err != nil {
		t.Fatalf("DeleteFlow: %v", err)
	}
	if deleted != record.FlowID {
		t.Fatalf("deleted ID = %q, want %q", deleted, record.FlowID)
	}
	started, err := client.StartFlow(context.Background(), StartFlowInput{
		RepoPath:     t.TempDir(),
		Title:        "Started Flow",
		Instructions: "started instructions",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	if started.Flow.FlowID == "" || started.Flow.Title != "Started Flow" || started.LaunchError != "" {
		t.Fatalf("StartFlow result = %#v", started)
	}
}

func TestClientMutationNotFoundErrorsClassifyAsFlowstoreNotFound(t *testing.T) {
	_, url, root := newClientGraphQLServerWithRoot(t, "test-token")
	client, err := New(Options{EndpointURL: url, Token: "test-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	planID, planPath := saveClientPlan(t, root)

	for _, tt := range []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "SetPhase",
			run: func(ctx context.Context) error {
				_, _, err := client.SetPhase(ctx, flowstore.PhaseUpdate{
					FlowID:  "missing-flow",
					PhaseID: "plan",
					Status:  flowstore.PhaseCompleted,
				})
				return err
			},
		},
		{
			name: "RestartFlowPhase",
			run: func(ctx context.Context) error {
				_, _, err := client.RestartFlowPhase(ctx, flowstore.PhaseRestartUpdate{
					FlowID:  "missing-flow",
					PhaseID: "plan",
					Notes:   "rerun",
				})
				return err
			},
		},
		{
			name: "AddFlowChildPhase",
			run: func(ctx context.Context) error {
				_, _, err := client.AddFlowChildPhase(ctx, flowstore.ChildPhaseUpdate{
					FlowID:        "missing-flow",
					ParentPhaseID: "implementation",
					PhaseID:       "child",
					Title:         "Child",
					Order:         10,
				})
				return err
			},
		},
		{
			name: "SetFlowPlanLink",
			run: func(ctx context.Context) error {
				_, err := client.SetFlowPlanLink(ctx, flowstore.PlanLinkUpdate{
					FlowID:   "missing-flow",
					PlanID:   planID,
					PlanPath: planPath,
				})
				return err
			},
		},
		{
			name: "SetFlowPR",
			run: func(ctx context.Context) error {
				_, _, err := client.SetFlowPR(ctx, flowstore.PRUpdate{
					FlowID:     "missing-flow",
					Provider:   "github",
					Number:     17,
					URL:        "https://github.com/brian-bell/flowstate/pull/17",
					HeadBranch: "flow/client",
					BaseBranch: "main",
					Status:     "open",
				})
				return err
			},
		},
		{
			name: "SetFlowMerge",
			run: func(ctx context.Context) error {
				_, _, err := client.SetFlowMerge(ctx, flowstore.MergeUpdate{
					FlowID: "missing-flow",
					Status: flowstore.MergeBlocked,
				})
				return err
			},
		},
		{
			name: "SetFlowAutoMode",
			run: func(ctx context.Context) error {
				_, err := client.SetFlowAutoMode(ctx, flowstore.AutoModeUpdate{
					FlowID:  "missing-flow",
					Enabled: true,
				})
				return err
			},
		},
		{
			name: "DeleteFlow",
			run: func(ctx context.Context) error {
				_, err := client.DeleteFlow(ctx, "missing-flow")
				return err
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(context.Background()); !flowstore.IsNotFound(err) {
				t.Fatalf("%s missing error = %v, want flowstore not found", tt.name, err)
			}
		})
	}
}

func TestClientResolvesEnvironmentBeforeCoords(t *testing.T) {
	t.Setenv(EnvURL, "http://127.0.0.1:4321")
	t.Setenv(EnvToken, "env-token")
	client, err := New(Options{
		Coords: func() (daemoncoords.Coords, error) {
			return daemoncoords.Coords{URL: "http://127.0.0.1:9999", Token: "coords-token", PID: 1, Version: "test"}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.endpoint != "http://127.0.0.1:4321/graphql" || client.token != "env-token" {
		t.Fatalf("client resolved endpoint/token = %q/%q", client.endpoint, client.token)
	}
}

func TestClientResolvesCoordsWhenEnvironmentUnset(t *testing.T) {
	client, err := New(Options{
		Coords: func() (daemoncoords.Coords, error) {
			return daemoncoords.Coords{URL: "http://127.0.0.1:9999", Token: "coords-token", PID: 1, Version: "test"}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.endpoint != "http://127.0.0.1:9999/graphql" || client.token != "coords-token" {
		t.Fatalf("client resolved endpoint/token = %q/%q", client.endpoint, client.token)
	}
}

func TestClientUnauthorizedAndTransportErrors(t *testing.T) {
	_, url := newClientGraphQLServer(t, "test-token")
	client, err := New(Options{EndpointURL: url, Token: "bad-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.ListFlows(context.Background(), flowstore.FlowFilter{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ListFlows auth error = %v, want ErrUnauthorized", err)
	}

	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	t.Cleanup(malformed.Close)
	client, err = New(Options{EndpointURL: malformed.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("New malformed: %v", err)
	}
	_, err = client.ListFlows(context.Background(), flowstore.FlowFilter{})
	if err == nil || !strings.Contains(err.Error(), "decode GraphQL response") {
		t.Fatalf("malformed response error = %v", err)
	}

	client, err = New(Options{EndpointURL: "http://127.0.0.1:1", Token: "test-token"})
	if err != nil {
		t.Fatalf("New bad transport: %v", err)
	}
	_, err = client.ListFlows(context.Background(), flowstore.FlowFilter{})
	if err == nil {
		t.Fatal("ListFlows transport error = nil, want error")
	}
}

func newClientGraphQLServer(t *testing.T, token string) (*flowstore.Store, string) {
	t.Helper()
	store, url, _ := newClientGraphQLServerWithRoot(t, token)
	return store, url
}

func newClientGraphQLServerWithRoot(t *testing.T, token string) (*flowstore.Store, string, string) {
	t.Helper()
	root := t.TempDir()
	store, err := flowstore.NewStore(flowstore.StoreOptions{Root: root})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	handler, err := server.NewHandler(server.HandlerOptions{
		Token: token,
		AllowedEndpoints: []server.AllowedEndpoint{{
			Host: "127.0.0.1",
			Port: port,
		}},
		FlowStore: store,
		StateRoot: root,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return store, srv.URL, root
}

func createClientFlow(t *testing.T, store *flowstore.Store, record flowstore.FlowRecord) flowstore.FlowRecord {
	t.Helper()
	created, err := store.Create(record)
	if err != nil {
		t.Fatalf("Create(%s): %v", record.FlowID, err)
	}
	return created
}

func saveClientPlan(t *testing.T, root string) (string, string) {
	t.Helper()
	store, err := planstore.NewStore(planstore.StoreOptions{Root: root})
	if err != nil {
		t.Fatalf("New plan store: %v", err)
	}
	planID, err := store.Save(planstore.PlanRecord{
		PlanID:   "plan-1",
		Title:    "Plan 1",
		Status:   "approved",
		Markdown: "# Plan 1\n",
	})
	if err != nil {
		t.Fatalf("Save plan: %v", err)
	}
	planPath, err := planstore.MarkdownPath(root, planID)
	if err != nil {
		t.Fatalf("MarkdownPath: %v", err)
	}
	return planID, planPath
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "flowstate-daemonclient-test-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("FLOWSTATE_DAEMON_COORDS", filepath.Join(dir, "daemon.json"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
