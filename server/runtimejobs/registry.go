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
	StatusCanceled  Status = "canceled"
)

const (
	defaultMaxLogBytes  = 64 * 1024
	defaultMaxLogLines  = 400
	defaultCompletedTTL = 10 * time.Minute
)

type CommandBuilder func(context.Context, actions.AgentLaunchContext) (*exec.Cmd, error)

type FlowReader func(string) (flowstore.FlowRecord, error)

type PhaseResetter func(flowstore.PhaseResetUpdate) (flowstore.FlowRecord, error)

type PhaseUpdater func(flowstore.PhaseUpdate) (flowstore.FlowRecord, error)

type Options struct {
	MaxLogBytes  int
	MaxLogLines  int
	CompletedTTL time.Duration
	WaitDelay    time.Duration
	Now          func() time.Time
	BuildCommand CommandBuilder
	ReadFlow     FlowReader
	ResetPhase   PhaseResetter
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

type CancelResult struct {
	Snapshot   Snapshot
	Found      bool
	Transition bool
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
	readFlow     FlowReader
	resetPhase   PhaseResetter
	updatePhase  PhaseUpdater
	nextID       atomic.Uint64
}

type job struct {
	snapshot Snapshot
	tail     logTail
	cancel   context.CancelFunc
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
		readFlow:     opts.ReadFlow,
		resetPhase:   opts.ResetPhase,
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
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	j := &job{
		snapshot: Snapshot{
			ID:        id,
			LaunchID:  launchID,
			FlowID:    flowID,
			PhaseID:   phaseID,
			Status:    StatusQueued,
			CreatedAt: createdAt,
		},
		tail:   logTail{maxBytes: r.maxLogBytes, maxLines: r.maxLogLines},
		cancel: cancel,
	}
	r.mu.Lock()
	r.evictExpiredLocked(createdAt)
	r.jobs[id] = j
	initial := j.snapshot
	r.mu.Unlock()

	go r.run(jobCtx, id, req.Context)
	return initial, nil
}

func (r *Registry) Lookup(id string) (Snapshot, bool) {
	r.EvictExpired()
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.jobs[id]
	if !ok {
		return Snapshot{}, false
	}
	return j.snapshot, true
}

func (r *Registry) Cancel(id string) CancelResult {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return CancelResult{}
	}
	now := r.now()
	r.mu.Lock()
	j, ok := r.jobs[trimmed]
	if !ok {
		r.mu.Unlock()
		return CancelResult{}
	}
	if terminal(j.snapshot.Status) {
		snapshot := j.snapshot
		r.mu.Unlock()
		return CancelResult{Snapshot: snapshot, Found: true}
	}
	j.snapshot.Status = StatusCanceled
	j.snapshot.EndedAt = &now
	j.snapshot.Error = "runtime job canceled"
	cancel := j.cancel
	snapshot := j.snapshot
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return CancelResult{Snapshot: snapshot, Found: true, Transition: true}
}

func (r *Registry) CancelAll() {
	r.mu.RLock()
	ids := make([]string, 0, len(r.jobs))
	for id := range r.jobs {
		ids = append(ids, id)
	}
	r.mu.RUnlock()
	for _, id := range ids {
		result := r.Cancel(id)
		if result.Transition && result.Snapshot.Status == StatusCanceled {
			r.resetCanceledAwaitingSessionPhase(result.Snapshot)
		}
	}
}

func (r *Registry) ActiveRuntimeJob(record flowstore.FlowRecord, phase flowstore.FlowPhase) (*flowquery.RuntimeJob, error) {
	r.EvictExpired()
	phaseID := artifacts.NormalizePhaseID(phase.PhaseID)
	launchID := flowstore.LatestPhaseLaunchID(phase)
	if launchID == "" {
		return nil, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var latest *Snapshot
	for _, j := range r.jobs {
		snapshot := j.snapshot
		if snapshot.FlowID != record.FlowID ||
			artifacts.NormalizePhaseID(snapshot.PhaseID) != phaseID ||
			snapshot.LaunchID != launchID {
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
	r.evictExpiredLocked(now)
}

func (r *Registry) evictExpiredLocked(now time.Time) {
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
	if !r.setStatus(id, StatusStarting, nil) {
		return
	}
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
	if !r.setStatus(id, StatusRunning, &startedAt) {
		_ = cmd.Wait()
		return
	}
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

func (r *Registry) setStatus(id string, status Status, startedAt *time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || terminal(j.snapshot.Status) {
		return false
	}
	j.snapshot.Status = status
	if startedAt != nil {
		j.snapshot.StartedAt = cloneTime(*startedAt)
	}
	return true
}

func (r *Registry) finish(id string, status Status, exitCode *int, errText string) bool {
	endedAt := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || terminal(j.snapshot.Status) {
		return false
	}
	j.snapshot.Status = status
	j.snapshot.EndedAt = &endedAt
	if exitCode != nil {
		code := *exitCode
		j.snapshot.ExitCode = &code
	}
	j.snapshot.Error = errText
	return true
}

func (r *Registry) fail(id string, code *int, err error, launch actions.AgentLaunchContext) {
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	finished := r.finish(id, StatusFailed, code, errText)
	if finished && !launch.FlowPhaseTerminal {
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
	if !r.phaseStillActiveForFailure(snapshot) {
		return
	}
	_, err := r.updatePhase(flowstore.PhaseUpdate{
		FlowID:  snapshot.FlowID,
		PhaseID: snapshot.PhaseID,
		Status:  flowstore.PhaseNeedsAttention,
		Outcome: runtimeFailureOutcome(snapshot.PhaseID),
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

func runtimeFailureOutcome(phaseID string) string {
	if artifacts.NormalizePhaseID(phaseID) == "plan-review" {
		return flowstore.OutcomeChangesRequested
	}
	return "runtime_failed"
}

func (r *Registry) resetCanceledAwaitingSessionPhase(snapshot Snapshot) {
	if r.readFlow == nil || r.resetPhase == nil {
		return
	}
	record, err := r.readFlow(snapshot.FlowID)
	if err != nil {
		r.setPhaseUpdateError(snapshot.ID, fmt.Sprintf("read flow before cancel reset: %v", err))
		return
	}
	phase, ok := phaseByID(record, snapshot.PhaseID)
	if !ok {
		r.setPhaseUpdateError(snapshot.ID, fmt.Sprintf("phase %q not found in flow %q", snapshot.PhaseID, snapshot.FlowID))
		return
	}
	if phase.Status != flowstore.PhaseRunning ||
		flowstore.LatestPhaseLaunchID(phase) != snapshot.LaunchID ||
		!flowstore.PhaseAwaitingSession(phase) ||
		flowstore.PhaseSessionLaunchMismatch(phase) {
		return
	}
	if _, err := r.resetPhase(flowstore.PhaseResetUpdate{
		FlowID:  snapshot.FlowID,
		PhaseID: snapshot.PhaseID,
	}); err != nil {
		r.setPhaseUpdateError(snapshot.ID, fmt.Sprintf("reset phase after cancel: %v", err))
	}
}

func (r *Registry) phaseStillActiveForFailure(snapshot Snapshot) bool {
	if r.readFlow == nil {
		return true
	}
	record, err := r.readFlow(snapshot.FlowID)
	if err != nil {
		r.setPhaseUpdateError(snapshot.ID, fmt.Sprintf("read flow before runtime failure update: %v", err))
		return false
	}
	phase, ok := phaseByID(record, snapshot.PhaseID)
	if !ok {
		r.setPhaseUpdateError(snapshot.ID, fmt.Sprintf("phase %q not found in flow %q", snapshot.PhaseID, snapshot.FlowID))
		return false
	}
	return phase.Status == flowstore.PhaseRunning && flowstore.LatestPhaseLaunchID(phase) == snapshot.LaunchID
}

func (r *Registry) setPhaseUpdateError(id, errText string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		j.snapshot.PhaseUpdateError = errText
	}
}

func phaseByID(record flowstore.FlowRecord, phaseID string) (flowstore.FlowPhase, bool) {
	normalized := artifacts.NormalizePhaseID(phaseID)
	if normalized == "" {
		return flowstore.FlowPhase{}, false
	}
	for _, phase := range record.Phases {
		if phase.PhaseID == phaseID {
			return phase, true
		}
	}
	for _, phase := range record.Phases {
		if artifacts.NormalizePhaseID(phase.PhaseID) == normalized {
			return phase, true
		}
	}
	return flowstore.FlowPhase{}, false
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
	return status == StatusSucceeded || status == StatusFailed || status == StatusCanceled
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
