package runtimejobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/artifacts"
	"github.com/brian-bell/flowstate/server/flowquery"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusStarting  Status = "starting"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

const (
	defaultMaxLogBytes  = 64 * 1024
	defaultMaxLogLines  = 400
	defaultCompletedTTL = 10 * time.Minute
)

type CommandBuilder func(context.Context, actions.AgentLaunchContext) (*exec.Cmd, error)

type PhaseUpdater func(flowstore.PhaseUpdate) (flowstore.FlowRecord, error)

type Options struct {
	MaxLogBytes  int
	MaxLogLines  int
	CompletedTTL time.Duration
	WaitDelay    time.Duration
	Now          func() time.Time
	BuildCommand CommandBuilder
	UpdatePhase  PhaseUpdater
}

type StartRequest struct {
	FlowID   string
	PhaseID  string
	LaunchID string
	Context  actions.AgentLaunchContext
}

type Snapshot struct {
	ID               string
	LaunchID         string
	FlowID           string
	PhaseID          string
	Status           Status
	CreatedAt        time.Time
	StartedAt        *time.Time
	EndedAt          *time.Time
	ExitCode         *int
	Error            string
	PhaseUpdateError string
	LogTail          string
	LogTruncated     bool
}

type Registry struct {
	mu           sync.RWMutex
	jobs         map[string]*job
	maxLogBytes  int
	maxLogLines  int
	completedTTL time.Duration
	waitDelay    time.Duration
	now          func() time.Time
	buildCommand CommandBuilder
	updatePhase  PhaseUpdater
	nextID       atomic.Uint64
}

type job struct {
	snapshot Snapshot
	tail     logTail
}

func NewRegistry(opts Options) *Registry {
	maxLogBytes := opts.MaxLogBytes
	if maxLogBytes <= 0 {
		maxLogBytes = defaultMaxLogBytes
	}
	maxLogLines := opts.MaxLogLines
	if maxLogLines <= 0 {
		maxLogLines = defaultMaxLogLines
	}
	completedTTL := opts.CompletedTTL
	if completedTTL <= 0 {
		completedTTL = defaultCompletedTTL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	buildCommand := opts.BuildCommand
	if buildCommand == nil {
		buildCommand = defaultCommandBuilder
	}
	return &Registry{
		jobs:         make(map[string]*job),
		maxLogBytes:  maxLogBytes,
		maxLogLines:  maxLogLines,
		completedTTL: completedTTL,
		waitDelay:    opts.WaitDelay,
		now:          now,
		buildCommand: buildCommand,
		updatePhase:  opts.UpdatePhase,
	}
}

func (r *Registry) Start(ctx context.Context, req StartRequest) (Snapshot, error) {
	flowID := strings.TrimSpace(req.FlowID)
	if flowID == "" {
		flowID = strings.TrimSpace(req.Context.FlowID)
	}
	phaseID := artifacts.NormalizePhaseID(req.PhaseID)
	if phaseID == "" {
		phaseID = artifacts.NormalizePhaseID(req.Context.FlowPhaseID)
	}
	launchID := strings.TrimSpace(req.LaunchID)
	if launchID == "" {
		launchID = strings.TrimSpace(req.Context.LaunchID)
	}
	if flowID == "" {
		return Snapshot{}, fmt.Errorf("flow id is required")
	}
	if phaseID == "" {
		return Snapshot{}, fmt.Errorf("phase id is required")
	}
	if launchID == "" {
		return Snapshot{}, fmt.Errorf("launch id is required")
	}
	id := fmt.Sprintf("job-%d", r.nextID.Add(1))
	createdAt := r.now()
	j := &job{
		snapshot: Snapshot{
			ID:        id,
			LaunchID:  launchID,
			FlowID:    flowID,
			PhaseID:   phaseID,
			Status:    StatusQueued,
			CreatedAt: createdAt,
		},
		tail: logTail{maxBytes: r.maxLogBytes, maxLines: r.maxLogLines},
	}
	r.mu.Lock()
	r.jobs[id] = j
	initial := j.snapshot
	r.mu.Unlock()

	go r.run(ctx, id, req.Context)
	return initial, nil
}

func (r *Registry) Lookup(id string) (Snapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.jobs[id]
	if !ok {
		return Snapshot{}, false
	}
	return j.snapshot, true
}

func (r *Registry) ActiveRuntimeJob(record flowstore.FlowRecord, phase flowstore.FlowPhase) (*flowquery.RuntimeJob, error) {
	phaseID := artifacts.NormalizePhaseID(phase.PhaseID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var latest *Snapshot
	for _, j := range r.jobs {
		snapshot := j.snapshot
		if snapshot.FlowID != record.FlowID || artifacts.NormalizePhaseID(snapshot.PhaseID) != phaseID {
			continue
		}
		if latest == nil || snapshot.CreatedAt.After(latest.CreatedAt) {
			copy := snapshot
			latest = &copy
		}
	}
	if latest == nil {
		return nil, nil
	}
	return snapshotToFlowQuery(*latest), nil
}

func (r *Registry) RuntimeStateKnown() bool {
	return true
}

func (r *Registry) EvictExpired() {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, j := range r.jobs {
		snapshot := j.snapshot
		if !terminal(snapshot.Status) || snapshot.EndedAt == nil {
			continue
		}
		if now.Sub(*snapshot.EndedAt) > r.completedTTL {
			delete(r.jobs, id)
		}
	}
}

func (r *Registry) run(ctx context.Context, id string, launch actions.AgentLaunchContext) {
	r.setStatus(id, StatusStarting, nil)
	cmd, err := r.buildCommand(ctx, launch)
	if err != nil {
		r.fail(id, nil, fmt.Errorf("build agent command: %w", err), launch)
		return
	}
	if r.waitDelay > 0 {
		cmd.WaitDelay = r.waitDelay
	}
	writer := r.writer(id)
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Start(); err != nil {
		r.fail(id, nil, fmt.Errorf("start agent command: %w", err), launch)
		return
	}
	startedAt := r.now()
	r.setStatus(id, StatusRunning, &startedAt)
	err = cmd.Wait()
	if err == nil {
		code := 0
		r.finish(id, StatusSucceeded, &code, "")
		return
	}
	code := exitCode(err)
	r.fail(id, code, err, launch)
}

func (r *Registry) writer(id string) io.Writer {
	return writerFunc(func(p []byte) (int, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if j, ok := r.jobs[id]; ok {
			j.tail.Write(p)
			j.snapshot.LogTail = j.tail.String()
			j.snapshot.LogTruncated = j.tail.Truncated()
		}
		return len(p), nil
	})
}

func (r *Registry) setStatus(id string, status Status, startedAt *time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || terminal(j.snapshot.Status) {
		return
	}
	j.snapshot.Status = status
	if startedAt != nil {
		j.snapshot.StartedAt = cloneTime(*startedAt)
	}
}

func (r *Registry) finish(id string, status Status, exitCode *int, errText string) {
	endedAt := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || terminal(j.snapshot.Status) {
		return
	}
	j.snapshot.Status = status
	j.snapshot.EndedAt = &endedAt
	if exitCode != nil {
		code := *exitCode
		j.snapshot.ExitCode = &code
	}
	j.snapshot.Error = errText
}

func (r *Registry) fail(id string, code *int, err error, launch actions.AgentLaunchContext) {
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	r.finish(id, StatusFailed, code, errText)
	if !launch.FlowPhaseTerminal {
		r.markNeedsAttention(id, errText)
	}
}

func (r *Registry) markNeedsAttention(id, errText string) {
	if r.updatePhase == nil {
		return
	}
	snapshot, ok := r.Lookup(id)
	if !ok {
		return
	}
	_, err := r.updatePhase(flowstore.PhaseUpdate{
		FlowID:  snapshot.FlowID,
		PhaseID: snapshot.PhaseID,
		Status:  flowstore.PhaseNeedsAttention,
		Outcome: "runtime_failed",
		Notes:   "Runtime job failed: " + errText,
	})
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		j.snapshot.PhaseUpdateError = err.Error()
	}
}

func defaultCommandBuilder(ctx context.Context, launch actions.AgentLaunchContext) (*exec.Cmd, error) {
	base, err := actions.AgentCommand(launch)
	if err != nil {
		return nil, err
	}
	if base.Err != nil {
		return nil, base.Err
	}
	if len(base.Args) == 0 {
		return nil, fmt.Errorf("agent command has no argv")
	}
	name := base.Args[0]
	args := base.Args[1:]
	if base.Path != "" {
		name = base.Path
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = base.Dir
	cmd.Env = base.Env
	return cmd, nil
}

func exitCode(err error) *int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		return &code
	}
	return nil
}

func cloneTime(t time.Time) *time.Time {
	return &t
}

func terminal(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed
}

func snapshotToFlowQuery(snapshot Snapshot) *flowquery.RuntimeJob {
	return &flowquery.RuntimeJob{
		ID:               snapshot.ID,
		LaunchID:         snapshot.LaunchID,
		FlowID:           snapshot.FlowID,
		PhaseID:          snapshot.PhaseID,
		Status:           string(snapshot.Status),
		CreatedAt:        snapshot.CreatedAt,
		StartedAt:        snapshot.StartedAt,
		EndedAt:          snapshot.EndedAt,
		ExitCode:         snapshot.ExitCode,
		Error:            snapshot.Error,
		PhaseUpdateError: snapshot.PhaseUpdateError,
		LogTail:          snapshot.LogTail,
		LogTruncated:     snapshot.LogTruncated,
	}
}

type writerFunc func([]byte) (int, error)

func (fn writerFunc) Write(p []byte) (int, error) {
	return fn(p)
}

type logTail struct {
	data      []byte
	maxBytes  int
	maxLines  int
	truncated bool
}

func (t *logTail) Write(p []byte) {
	t.data = append(t.data, p...)
	if t.maxBytes > 0 && len(t.data) > t.maxBytes {
		t.data = append([]byte(nil), t.data[len(t.data)-t.maxBytes:]...)
		t.truncated = true
	}
	if t.maxLines > 0 {
		lines := strings.SplitAfter(string(t.data), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > t.maxLines {
			lines = lines[len(lines)-t.maxLines:]
			t.data = []byte(strings.Join(lines, ""))
			t.truncated = true
		}
	}
}

func (t logTail) String() string {
	return string(t.data)
}

func (t logTail) Truncated() bool {
	return t.truncated
}
