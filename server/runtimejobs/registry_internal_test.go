package runtimejobs

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/flowstore"
)

func TestRegistryCancelSkipsPhaseUpdateWhenTerminationNotConfirmed(t *testing.T) {
	originalTerminator := terminateRuntimeCommandFunc
	terminateRuntimeCommandFunc = func(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) error {
		return errors.New("still running after forced kill")
	}
	t.Cleanup(func() {
		terminateRuntimeCommandFunc = originalTerminator
	})

	var mu sync.Mutex
	var phaseUpdates []flowstore.PhaseUpdate
	registry := NewRegistry(Options{
		CancelGrace: time.Millisecond,
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5"), nil
		},
		ReadFlow: func(flowID string) (flowstore.FlowRecord, error) {
			return flowstore.FlowRecord{
				FlowID: flowID,
				Phases: []flowstore.FlowPhase{{
					PhaseID:   "implementation",
					Status:    flowstore.PhaseRunning,
					LaunchIDs: []string{"launch-1"},
				}},
			}, nil
		},
		UpdatePhase: func(update flowstore.PhaseUpdate) (flowstore.FlowRecord, error) {
			mu.Lock()
			defer mu.Unlock()
			phaseUpdates = append(phaseUpdates, update)
			return flowstore.FlowRecord{}, nil
		},
	})

	snapshot, err := registry.Start(context.Background(), StartRequest{
		FlowID:   "flow-1",
		PhaseID:  "implementation",
		LaunchID: "launch-1",
		Context: actions.AgentLaunchContext{
			FlowID:      "flow-1",
			FlowPhaseID: "implementation",
			LaunchID:    "launch-1",
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForInternalJobStatus(t, registry, snapshot.ID, StatusRunning)

	result := registry.Cancel(snapshot.ID)
	if result.Code != CancelCanceled || result.Snapshot.Status != StatusCanceled {
		t.Fatalf("Cancel() = %#v, want canceled job", result)
	}
	if !strings.Contains(result.Snapshot.Error, "still running after forced kill") {
		t.Fatalf("Cancel() error = %q, want termination failure context", result.Snapshot.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(phaseUpdates) != 0 {
		t.Fatalf("phase updates = %#v, want none when termination was not confirmed", phaseUpdates)
	}
}

func waitForInternalJobStatus(t *testing.T, registry *Registry, id string, status Status) Snapshot {
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
	return Snapshot{}
}
