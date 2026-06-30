package runtimejobs_test

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
	if _, ok := registry.Lookup(snapshot.ID); ok {
		t.Fatal("completed job should be evicted after TTL during lookup")
	}
}

func TestRegistryStartDetachesRuntimeJobFromCallerCancellation(t *testing.T) {
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 0.05; printf 'done\n'"), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	snapshot, err := registry.Start(ctx, runtimejobs.StartRequest{
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
	cancel()

	final := waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusSucceeded)
	if !strings.Contains(final.LogTail, "done") {
		t.Fatalf("log tail = %q, want detached command output", final.LogTail)
	}
}

func TestRegistryCancelWhileCommandBuildIsInProgressMarksJobCanceled(t *testing.T) {
	buildEntered := make(chan struct{})
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			close(buildEntered)
			<-ctx.Done()
			return exec.CommandContext(ctx, "/bin/sh", "-c", "true"), nil
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
	select {
	case <-buildEntered:
	case <-time.After(time.Second):
		t.Fatal("BuildCommand did not start")
	}

	result := registry.Cancel(snapshot.ID)
	if !result.Found || !result.Transition || result.Code != runtimejobs.CancelCanceled {
		t.Fatalf("Cancel() = %#v, want canceled transition", result)
	}
	final := waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusCanceled)
	if final.Error != "runtime job canceled: user requested cancellation" {
		t.Fatalf("canceled error = %q, want user cancellation reason", final.Error)
	}
}

func TestRegistryActiveRuntimeJobRequiresCurrentPhaseLaunchID(t *testing.T) {
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5"), nil
		},
	})
	defer registry.CancelAll()
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

	record := flowstore.FlowRecord{FlowID: "flow-1"}
	noLaunch, err := registry.ActiveRuntimeJob(record, flowstore.FlowPhase{PhaseID: "implementation"})
	if err != nil {
		t.Fatalf("ActiveRuntimeJob(no launch) error = %v", err)
	}
	if noLaunch != nil {
		t.Fatalf("ActiveRuntimeJob(no launch) = %#v, want nil", noLaunch)
	}
	staleLaunch, err := registry.ActiveRuntimeJob(record, flowstore.FlowPhase{PhaseID: "implementation", LaunchIDs: []string{"launch-2"}})
	if err != nil {
		t.Fatalf("ActiveRuntimeJob(stale launch) error = %v", err)
	}
	if staleLaunch != nil {
		t.Fatalf("ActiveRuntimeJob(stale launch) = %#v, want nil", staleLaunch)
	}
	currentLaunch, err := registry.ActiveRuntimeJob(record, flowstore.FlowPhase{PhaseID: "implementation", LaunchIDs: []string{"launch-1"}})
	if err != nil {
		t.Fatalf("ActiveRuntimeJob(current launch) error = %v", err)
	}
	if currentLaunch == nil || currentLaunch.ID != snapshot.ID {
		t.Fatalf("ActiveRuntimeJob(current launch) = %#v, want job %s", currentLaunch, snapshot.ID)
	}
}

func TestRegistryCancelRejectsInvalidUnknownAndTerminalJobs(t *testing.T) {
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "true"), nil
		},
	})

	if result := registry.Cancel(" \t "); result.Found || result.Transition || result.Code != runtimejobs.CancelInvalidID {
		t.Fatalf("Cancel(blank) = %#v, want invalid id", result)
	}
	if result := registry.Cancel("missing"); result.Found || result.Transition || result.Code != runtimejobs.CancelNotFound {
		t.Fatalf("Cancel(missing) = %#v, want not found", result)
	}

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
	final := waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusSucceeded)
	repeated := registry.Cancel(snapshot.ID)
	if !repeated.Found || repeated.Transition || repeated.Code != runtimejobs.CancelAlreadyTerminal {
		t.Fatalf("Cancel(terminal) = %#v, want already terminal", repeated)
	}
	if !repeated.Snapshot.EndedAt.Equal(*final.EndedAt) || repeated.Snapshot.Error != final.Error || repeated.Snapshot.Status != final.Status {
		t.Fatalf("terminal snapshot mutated from %#v to %#v", final, repeated.Snapshot)
	}
}

func TestRegistryCancelMarksMatchingRunningPhaseNeedsAttention(t *testing.T) {
	var mu sync.Mutex
	var phaseUpdates []flowstore.PhaseUpdate
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
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

	canceled := registry.Cancel(snapshot.ID)
	if !canceled.Found || !canceled.Transition || canceled.Code != runtimejobs.CancelCanceled ||
		canceled.Snapshot.Status != runtimejobs.StatusCanceled || canceled.Snapshot.EndedAt == nil {
		t.Fatalf("Cancel() = %#v; want transitioned canceled snapshot", canceled)
	}
	final := waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusCanceled)
	if final.Error != "runtime job canceled: user requested cancellation" {
		t.Fatalf("canceled error = %q, want cancellation reason", final.Error)
	}

	waitForPhaseUpdates(t, &mu, &phaseUpdates, 1)
	mu.Lock()
	defer mu.Unlock()
	if len(phaseUpdates) != 1 ||
		phaseUpdates[0].FlowID != "flow-1" ||
		phaseUpdates[0].PhaseID != "implementation" ||
		phaseUpdates[0].Status != flowstore.PhaseNeedsAttention ||
		phaseUpdates[0].ExpectedStatus != flowstore.PhaseRunning ||
		phaseUpdates[0].ExpectedLatestLaunchID != snapshot.LaunchID ||
		phaseUpdates[0].Outcome != "runtime_canceled" ||
		!strings.Contains(phaseUpdates[0].Notes, snapshot.ID) {
		t.Fatalf("phase updates = %#v, want runtime_canceled needs_attention for matching launch", phaseUpdates)
	}
}

func TestRegistryCancelPlanReviewUsesChangesRequestedOutcome(t *testing.T) {
	var mu sync.Mutex
	var phaseUpdates []flowstore.PhaseUpdate
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5"), nil
		},
		ReadFlow: func(flowID string) (flowstore.FlowRecord, error) {
			return flowstore.FlowRecord{
				FlowID: flowID,
				Phases: []flowstore.FlowPhase{{
					PhaseID:   "plan-review",
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

	snapshot, err := registry.Start(context.Background(), runtimejobs.StartRequest{
		FlowID:   "flow-1",
		PhaseID:  "plan-review",
		LaunchID: "launch-1",
		Context: actions.AgentLaunchContext{
			FlowID:      "flow-1",
			FlowPhaseID: "plan-review",
			LaunchID:    "launch-1",
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusRunning)

	registry.Cancel(snapshot.ID)
	waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusCanceled)
	waitForPhaseUpdates(t, &mu, &phaseUpdates, 1)
	mu.Lock()
	defer mu.Unlock()
	if len(phaseUpdates) != 1 ||
		phaseUpdates[0].PhaseID != "plan-review" ||
		phaseUpdates[0].Outcome != flowstore.OutcomeChangesRequested ||
		phaseUpdates[0].Status != flowstore.PhaseNeedsAttention ||
		phaseUpdates[0].ExpectedStatus != flowstore.PhaseRunning ||
		phaseUpdates[0].ExpectedLatestLaunchID != snapshot.LaunchID {
		t.Fatalf("phase updates = %#v, want plan-review changes_requested needs_attention", phaseUpdates)
	}
}

func TestRegistryCancelSkipsPhaseUpdateWhenPhaseAdvancedOrRelaunched(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   string
		launchID string
	}{
		{name: "completed", status: flowstore.PhaseCompleted, launchID: "launch-1"},
		{name: "blocked", status: flowstore.PhaseBlocked, launchID: "launch-1"},
		{name: "skipped", status: flowstore.PhaseSkipped, launchID: "launch-1"},
		{name: "needs attention", status: flowstore.PhaseNeedsAttention, launchID: "launch-1"},
		{name: "new launch", status: flowstore.PhaseRunning, launchID: "launch-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var phaseUpdates []flowstore.PhaseUpdate
			registry := runtimejobs.NewRegistry(runtimejobs.Options{
				BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
					return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5"), nil
				},
				ReadFlow: func(flowID string) (flowstore.FlowRecord, error) {
					return flowstore.FlowRecord{
						FlowID: flowID,
						Phases: []flowstore.FlowPhase{{
							PhaseID:   "implementation",
							Status:    tc.status,
							LaunchIDs: []string{tc.launchID},
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
			waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusRunning)
			registry.Cancel(snapshot.ID)
			waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusCanceled)
			time.Sleep(50 * time.Millisecond)

			mu.Lock()
			defer mu.Unlock()
			if len(phaseUpdates) != 0 {
				t.Fatalf("phase updates = %#v, want none", phaseUpdates)
			}
		})
	}
}

func TestRegistryCancelRecordsPhaseUpdateFailureOnSnapshot(t *testing.T) {
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
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
			return flowstore.FlowRecord{}, errors.New("store locked")
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
	waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusRunning)
	result := registry.Cancel(snapshot.ID)
	if result.Snapshot.Status != runtimejobs.StatusCanceled ||
		!strings.Contains(result.Snapshot.PhaseUpdateError, "store locked") {
		t.Fatalf("Cancel() = %#v, want canceled snapshot with phase update error", result)
	}
}

func TestRegistryCancelAllDoesNotMutateFlowPhase(t *testing.T) {
	var mu sync.Mutex
	var reads int
	var phaseUpdates []flowstore.PhaseUpdate
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5"), nil
		},
		ReadFlow: func(flowID string) (flowstore.FlowRecord, error) {
			mu.Lock()
			defer mu.Unlock()
			reads++
			return flowstore.FlowRecord{}, nil
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
	waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusRunning)

	registry.CancelAll()
	final := waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusCanceled)
	if final.Error != "runtime job canceled: server shutting down" {
		t.Fatalf("shutdown cancel error = %q", final.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 0 || len(phaseUpdates) != 0 {
		t.Fatalf("shutdown cleanup read count=%d updates=%#v, want no flow mutation", reads, phaseUpdates)
	}
}

func TestRegistryNonZeroExitMarksPhaseNeedsAttention(t *testing.T) {
	var mu sync.Mutex
	var phaseUpdates []flowstore.PhaseUpdate
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "echo bad; exit 7"), nil
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
		phaseUpdates[0].Status != flowstore.PhaseNeedsAttention ||
		phaseUpdates[0].ExpectedStatus != flowstore.PhaseRunning ||
		phaseUpdates[0].ExpectedLatestLaunchID != snapshot.LaunchID {
		t.Fatalf("phase updates = %#v, want one needs_attention update", phaseUpdates)
	}
}

func TestRegistryNonZeroExitPreservesExistingPhaseUpdate(t *testing.T) {
	var mu sync.Mutex
	var phaseUpdates []flowstore.PhaseUpdate
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 7"), nil
		},
		ReadFlow: func(flowID string) (flowstore.FlowRecord, error) {
			return flowstore.FlowRecord{
				FlowID: flowID,
				Phases: []flowstore.FlowPhase{{
					PhaseID:   "implementation",
					Status:    flowstore.PhaseNeedsAttention,
					Outcome:   "agent_reported",
					Notes:     "specific agent failure notes",
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
	waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusFailed)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(phaseUpdates) != 0 {
		t.Fatalf("phase updates = %#v, want runtime failure to preserve existing phase update", phaseUpdates)
	}
}

func TestRegistryPlanReviewNonZeroExitUsesChangesRequestedOutcome(t *testing.T) {
	var mu sync.Mutex
	var phaseUpdates []flowstore.PhaseUpdate
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 7"), nil
		},
		ReadFlow: func(flowID string) (flowstore.FlowRecord, error) {
			return flowstore.FlowRecord{
				FlowID: flowID,
				Phases: []flowstore.FlowPhase{{
					PhaseID:   "plan-review",
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

	snapshot, err := registry.Start(context.Background(), runtimejobs.StartRequest{
		FlowID:   "flow-1",
		PhaseID:  "plan-review",
		LaunchID: "launch-1",
		Context: actions.AgentLaunchContext{
			FlowID:      "flow-1",
			FlowPhaseID: "plan-review",
			LaunchID:    "launch-1",
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForJobStatus(t, registry, snapshot.ID, runtimejobs.StatusFailed)
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
		phaseUpdates[0].PhaseID != "plan-review" ||
		phaseUpdates[0].Status != flowstore.PhaseNeedsAttention ||
		phaseUpdates[0].Outcome != flowstore.OutcomeChangesRequested ||
		phaseUpdates[0].ExpectedStatus != flowstore.PhaseRunning ||
		phaseUpdates[0].ExpectedLatestLaunchID != snapshot.LaunchID ||
		phaseUpdates[0].Notes == "" {
		t.Fatalf("phase updates = %#v, want plan-review changes_requested needs_attention", phaseUpdates)
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

func waitForPhaseUpdates(t *testing.T, mu *sync.Mutex, updates *[]flowstore.PhaseUpdate, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(*updates)
		mu.Unlock()
		if got >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("phase updates = %#v, want at least %d", *updates, count)
}
