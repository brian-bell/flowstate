package runtimejobs_test

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/server/runtimejobs"
)

func TestRegistryStartReturnsImmediatelyAndCapturesCappedLifecycle(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	var phaseUpdates []flowstore.PhaseUpdate
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		Now:          func() time.Time { return now },
		MaxLogBytes:  18,
		MaxLogLines:  2,
		CompletedTTL: time.Minute,
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'first\\nsecond\\nthird\\n'"), nil
		},
		UpdatePhase: func(update flowstore.PhaseUpdate) (flowstore.FlowRecord, error) {
			phaseUpdates = append(phaseUpdates, update)
			return flowstore.FlowRecord{}, nil
		},
	})

	snapshot, err := registry.Start(context.Background(), runtimejobs.StartRequest{
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
	if snapshot.ID == "" || snapshot.LaunchID != "launch-1" || snapshot.Status != runtimejobs.StatusQueued {
		t.Fatalf("initial snapshot = %#v, want queued with launch id", snapshot)
	}

	final := waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusSucceeded)
	if final.StartedAt == nil || final.EndedAt == nil {
		t.Fatalf("final timestamps = started %v ended %v, want both set", final.StartedAt, final.EndedAt)
	}
	if final.ExitCode == nil || *final.ExitCode != 0 || final.Error != "" {
		t.Fatalf("final exit/error = %v/%q, want zero/no error", final.ExitCode, final.Error)
	}
	if !final.LogTruncated || strings.Contains(final.LogTail, "first") || !strings.Contains(final.LogTail, "third") {
		t.Fatalf("log tail = %q truncated=%v, want capped tail", final.LogTail, final.LogTruncated)
	}
	if len(phaseUpdates) != 0 {
		t.Fatalf("zero exit phase updates = %#v, want no completion or needs_attention update", phaseUpdates)
	}

	now = now.Add(2 * time.Minute)
	registry.EvictExpired()
	if _, ok := registry.Lookup(snapshot.ID); ok {
		t.Fatal("completed job should be evicted after TTL")
	}
}

func TestRegistryNonZeroExitMarksPhaseNeedsAttention(t *testing.T) {
	var mu sync.Mutex
	var phaseUpdates []flowstore.PhaseUpdate
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "echo bad; exit 7"), nil
		},
		UpdatePhase: func(update flowstore.PhaseUpdate) (flowstore.FlowRecord, error) {
			mu.Lock()
			defer mu.Unlock()
			phaseUpdates = append(phaseUpdates, update)
			return flowstore.FlowRecord{}, nil
		},
	})

	snapshot, err := registry.Start(context.Background(), runtimejobs.StartRequest{
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
	final := waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusFailed)
	if final.ExitCode == nil || *final.ExitCode != 7 {
		t.Fatalf("exit code = %v, want 7", final.ExitCode)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(phaseUpdates)
		mu.Unlock()
		if got > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(phaseUpdates) != 1 ||
		phaseUpdates[0].FlowID != "flow-1" ||
		phaseUpdates[0].PhaseID != "implementation" ||
		phaseUpdates[0].Status != flowstore.PhaseNeedsAttention {
		t.Fatalf("phase updates = %#v, want one needs_attention update", phaseUpdates)
	}
}

func waitForJobStatus(t *testing.T, registry *runtimejobs.Registry, id string, status runtimejobs.Status) runtimejobs.Snapshot {
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
