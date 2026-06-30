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
	store, url := newClientGraphQLServer(t, "test-token")
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
	return store, srv.URL
}

func createClientFlow(t *testing.T, store *flowstore.Store, record flowstore.FlowRecord) flowstore.FlowRecord {
	t.Helper()
	created, err := store.Create(record)
	if err != nil {
		t.Fatalf("Create(%s): %v", record.FlowID, err)
	}
	return created
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
