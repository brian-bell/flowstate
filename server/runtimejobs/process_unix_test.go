//go:build unix

package runtimejobs_test

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/server/runtimejobs"
)

func TestRegistryCancelKillsRuntimeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	parentPath := dir + "/parent.pid"
	childPath := dir + "/child.pid"
	script := strings.Join([]string{
		"trap '' TERM",
		"echo $$ > " + shellQuote(parentPath),
		"(trap '' TERM; while :; do sleep 1; done) &",
		"child=$!",
		"echo $child > " + shellQuote(childPath),
		"wait $child",
	}, "\n")
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		CancelGrace: 50 * time.Millisecond,
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", script), nil
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
	parentPID := waitForPIDFile(t, parentPath)
	childPID := waitForPIDFile(t, childPath)
	parentPGID, err := syscall.Getpgid(parentPID)
	if err != nil {
		t.Fatalf("Getpgid(parent %d): %v", parentPID, err)
	}
	childPGID, err := syscall.Getpgid(childPID)
	if err != nil {
		t.Fatalf("Getpgid(child %d): %v", childPID, err)
	}
	if childPGID != parentPGID {
		t.Fatalf("child pgid = %d, want parent runtime command pgid %d", childPGID, parentPGID)
	}

	result := registry.Cancel(snapshot.ID)
	if result.Code != runtimejobs.CancelCanceled || result.Snapshot.Status != runtimejobs.StatusCanceled {
		t.Fatalf("Cancel() = %#v, want canceled", result)
	}
	waitForProcessExit(t, parentPID)
	waitForProcessExit(t, childPID)
}

func TestRegistryCancelKillsRuntimeProcessGroupAfterParentExits(t *testing.T) {
	dir := t.TempDir()
	parentPath := dir + "/parent.pid"
	childPath := dir + "/child.pid"
	script := strings.Join([]string{
		"echo $$ > " + shellQuote(parentPath),
		"(trap '' TERM; while :; do sleep 1; done) &",
		"child=$!",
		"echo $child > " + shellQuote(childPath),
		"wait",
	}, "\n")
	registry := runtimejobs.NewRegistry(runtimejobs.Options{
		CancelGrace: 50 * time.Millisecond,
		BuildCommand: func(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", script), nil
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
	parentPID := waitForPIDFile(t, parentPath)
	childPID := waitForPIDFile(t, childPath)
	parentPGID, err := syscall.Getpgid(parentPID)
	if err != nil {
		t.Fatalf("Getpgid(parent %d): %v", parentPID, err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-parentPGID, syscall.SIGKILL)
	})

	result := registry.Cancel(snapshot.ID)
	if result.Code != runtimejobs.CancelCanceled || result.Snapshot.Status != runtimejobs.StatusCanceled {
		t.Fatalf("Cancel() = %#v, want canceled", result)
	}
	waitForProcessExit(t, parentPID)
	waitForProcessExit(t, childPID)
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil {
				t.Fatalf("parse pid file %s: %v", path, parseErr)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid file %s was not written", path)
	return 0
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still exists after runtime job cancellation", pid)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
