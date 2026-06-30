package flowrepo

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/artifacts"
)

const fakeSchemaVersion = 1

// FakeOptions configures an in-memory Fake repository.
type FakeOptions struct {
	Now       func() time.Time
	PlanPaths map[string]string
}

type fakePlanPhase struct {
	PhaseID string
	Title   string
	Status  string
	Order   int
}

// Fake is an in-memory FlowRepository for tests and future repository contract
// checks. It stores copies at the repository boundary so callers cannot mutate
// persisted state through returned records.
type Fake struct {
	mu                 sync.Mutex
	now                func() time.Time
	records            map[string]flowstore.FlowRecord
	planPaths          map[string]string
	planPhases         map[string][]fakePlanPhase
	brokenPlanMetadata map[string]bool
}

var _ FlowRepository = (*Fake)(nil)

// NewFake creates an empty in-memory FlowRepository.
func NewFake(opts FakeOptions) *Fake {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	planPaths := make(map[string]string, len(opts.PlanPaths))
	for id, path := range opts.PlanPaths {
		planPaths[id] = path
	}
	return &Fake{
		now:                now,
		records:            make(map[string]flowstore.FlowRecord),
		planPaths:          planPaths,
		planPhases:         make(map[string][]fakePlanPhase),
		brokenPlanMetadata: make(map[string]bool),
	}
}

// SeedPlan records a readable plan path for SetPlanLink validation.
func (f *Fake) SeedPlan(planID, planPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.planPaths[planID] = planPath
	delete(f.brokenPlanMetadata, planID)
}

// SeedPlanPhase records saved-plan phase metadata for linked phase sync tests.
func (f *Fake) SeedPlanPhase(planID, phaseID, title, status string, order int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	phase := fakePlanPhase{
		PhaseID: artifacts.NormalizePhaseID(phaseID),
		Title:   strings.TrimSpace(title),
		Status:  strings.TrimSpace(status),
		Order:   order,
	}
	phases := f.planPhases[planID]
	updated := false
	for i, existing := range phases {
		if artifacts.NormalizePhaseID(existing.PhaseID) == phase.PhaseID {
			phases[i] = phase
			updated = true
			break
		}
	}
	if !updated {
		phases = append(phases, phase)
	}
	f.planPhases[planID] = phases
	delete(f.brokenPlanMetadata, planID)
}

// PlanPhaseStatus reports a seeded linked-plan phase status.
func (f *Fake) PlanPhaseStatus(planID, phaseID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	phase, ok := f.planPhaseLocked(planID, phaseID)
	if !ok {
		return "", false
	}
	return phase.Status, true
}

// BreakPlanMetadata makes later linked-plan metadata reads fail for planID.
func (f *Fake) BreakPlanMetadata(planID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.brokenPlanMetadata[planID] = true
}

func (f *Fake) Read(flowID string) (flowstore.FlowRecord, error) {
	if err := validateSafeID("flow", flowID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.records[flowID]
	if !ok {
		return flowstore.FlowRecord{}, flowNotFound(flowID)
	}
	return cloneRecord(normalizeRecord(record)), nil
}

func (f *Fake) List(filter flowstore.FlowFilter) ([]flowstore.FlowRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var records []flowstore.FlowRecord
	for _, record := range f.records {
		record = normalizeRecord(record)
		if filter.RepoPath != "" && filepath.Clean(record.RepoPath) != filepath.Clean(filter.RepoPath) {
			continue
		}
		records = append(records, cloneRecord(record))
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].UpdatedAt.Equal(records[j].UpdatedAt) {
			return records[i].FlowID < records[j].FlowID
		}
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	return records, nil
}

func (f *Fake) Create(record flowstore.FlowRecord) (flowstore.FlowRecord, error) {
	if strings.TrimSpace(record.Title) == "" {
		return flowstore.FlowRecord{}, fmt.Errorf("flow title is required")
	}
	if strings.TrimSpace(record.Instructions) == "" {
		return flowstore.FlowRecord{}, fmt.Errorf("flow instructions are required")
	}
	if strings.TrimSpace(record.RepoPath) == "" {
		return flowstore.FlowRecord{}, fmt.Errorf("flow repo path is required")
	}
	if !filepath.IsAbs(record.RepoPath) {
		return flowstore.FlowRecord{}, fmt.Errorf("flow repo path must be absolute: %s", record.RepoPath)
	}
	now := f.now()
	if record.FlowID != "" {
		if err := validateSafeID("flow", record.FlowID); err != nil {
			return flowstore.FlowRecord{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if record.FlowID == "" {
		record.FlowID = f.generatedFlowIDLocked(record.Title, now)
	} else if _, ok := f.records[record.FlowID]; ok {
		return flowstore.FlowRecord{}, fmt.Errorf("flow %q already exists", record.FlowID)
	}
	record.SchemaVersion = fakeSchemaVersion
	record.CreatedAt = defaultTime(record.CreatedAt, now)
	record.UpdatedAt = defaultTime(record.UpdatedAt, now)
	record.AutoMode = true
	if len(record.Phases) == 0 {
		record.Phases = defaultPhases(record.CreatedAt, record.UpdatedAt)
	}
	record = normalizeRecord(record)
	f.records[record.FlowID] = cloneRecord(record)
	return cloneRecord(record), nil
}

func (f *Fake) generatedFlowIDLocked(title string, now time.Time) string {
	base := now.UTC().Format("20060102T150405Z") + "-" + artifacts.Slug(title, "flow")
	candidate := base
	for i := 2; ; i++ {
		if _, ok := f.records[candidate]; !ok {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

func (f *Fake) SetPhase(update flowstore.PhaseUpdate) (flowstore.FlowRecord, error) {
	update.PhaseID = artifacts.NormalizePhaseID(update.PhaseID)
	if err := validateSafeID("flow", update.FlowID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	record, ok := f.records[update.FlowID]
	if !ok {
		return flowstore.FlowRecord{}, flowNotFound(update.FlowID)
	}
	record = cloneRecord(record)
	phaseIndex := phaseIndexByID(record.Phases, update.PhaseID)
	if phaseIndex < 0 {
		return flowstore.FlowRecord{}, fmt.Errorf("phase %q not found in flow %q", update.PhaseID, update.FlowID)
	}

	now := f.now()
	phase := record.Phases[phaseIndex]
	originalStatus := phase.Status
	if err := validatePhaseUpdate(phase, update); err != nil {
		return flowstore.FlowRecord{}, err
	}
	phase.Status = update.Status
	if update.Status == flowstore.PhaseRunning {
		phase.Outcome = ""
	}
	if outcome := strings.TrimSpace(update.Outcome); outcome != "" {
		phase.Outcome = outcome
	}
	if update.Notes != "" {
		phase.Notes = update.Notes
	}
	if update.Summary != "" {
		phase.Summary = update.Summary
	}
	phase.PhaseID = update.PhaseID
	phase.UpdatedAt = now
	record.Phases[phaseIndex] = phase
	record.Phases = collapseDuplicatePhaseRows(record.Phases, phaseIndex)
	if phase.PhaseID == "merge" && (phase.Status == flowstore.PhaseRunning || phase.Status == flowstore.PhaseSkipped) {
		record.Merge = flowstore.Merge{Status: flowstore.MergePending}
	}
	record.UpdatedAt = now
	record = flowstore.RefreshPhaseReadiness(record, now)
	record = normalizeRecord(record)
	f.records[update.FlowID] = cloneRecord(record)

	if err := f.syncLinkedPlanPhaseLocked(record, phase); err != nil {
		if originalStatus == flowstore.PhaseCompleted {
			return cloneRecord(record), nil
		}
		record = markPhaseSyncNeedsAttention(record, phase, err, now)
		f.records[update.FlowID] = cloneRecord(record)
		return flowstore.FlowRecord{}, err
	}
	return cloneRecord(record), nil
}

func (f *Fake) RestartPhase(update flowstore.PhaseRestartUpdate) (flowstore.FlowRecord, error) {
	update.PhaseID = artifacts.NormalizePhaseID(update.PhaseID)
	if err := validateSafeID("flow", update.FlowID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	if err := validateSafeID("phase", update.PhaseID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	if strings.TrimSpace(update.Notes) == "" {
		return flowstore.FlowRecord{}, fmt.Errorf("phase restart requires notes")
	}
	return f.updateFlow(update.FlowID, func(record flowstore.FlowRecord, now time.Time) (flowstore.FlowRecord, error) {
		phaseIndex := phaseIndexByID(record.Phases, update.PhaseID)
		if phaseIndex < 0 {
			return flowstore.FlowRecord{}, fmt.Errorf("phase %q not found in flow %q", update.PhaseID, update.FlowID)
		}
		phase := record.Phases[phaseIndex]
		if phase.Status != flowstore.PhaseNeedsAttention && phase.Status != flowstore.PhaseBlocked {
			return flowstore.FlowRecord{}, fmt.Errorf("flow phase restart requires current status needs_attention or blocked; %s is %s", phase.PhaseID, phase.Status)
		}
		if err := validatePhaseUpdate(phase, flowstore.PhaseUpdate{
			FlowID:  update.FlowID,
			PhaseID: update.PhaseID,
			Status:  flowstore.PhaseRunning,
			Notes:   update.Notes,
		}); err != nil {
			return flowstore.FlowRecord{}, err
		}
		phase.Status = flowstore.PhaseRunning
		phase.Outcome = ""
		phase.Notes = update.Notes
		phase.PhaseID = update.PhaseID
		phase.UpdatedAt = now
		record.Phases[phaseIndex] = phase
		record.Phases = collapseDuplicatePhaseRows(record.Phases, phaseIndex)
		if phase.PhaseID == "merge" {
			record.Merge = flowstore.Merge{Status: flowstore.MergePending}
		}
		record.UpdatedAt = now
		record = flowstore.RefreshPhaseReadiness(record, now)
		return record, nil
	})
}

func (f *Fake) AddChildPhase(update flowstore.ChildPhaseUpdate) (flowstore.FlowRecord, error) {
	update.PhaseID = artifacts.NormalizePhaseID(update.PhaseID)
	update.ParentPhaseID = artifacts.NormalizePhaseID(update.ParentPhaseID)
	if err := validateChildPhaseUpdate(update); err != nil {
		return flowstore.FlowRecord{}, err
	}
	return f.updateFlow(update.FlowID, func(record flowstore.FlowRecord, now time.Time) (flowstore.FlowRecord, error) {
		parentIndex := phaseIndexByID(record.Phases, update.ParentPhaseID)
		if parentIndex < 0 {
			return flowstore.FlowRecord{}, fmt.Errorf("parent phase %q not found in flow %q", update.ParentPhaseID, update.FlowID)
		}
		if record.Phases[parentIndex].PhaseID != "implementation" {
			return flowstore.FlowRecord{}, fmt.Errorf("child phases can only be added under implementation")
		}
		if childIndex := phaseIndexByID(record.Phases, update.PhaseID); childIndex >= 0 {
			child := record.Phases[childIndex]
			if child.ParentPhaseID != update.ParentPhaseID {
				return flowstore.FlowRecord{}, fmt.Errorf("phase %q already belongs to parent %q", update.PhaseID, child.ParentPhaseID)
			}
			record.Phases = collapseDuplicatePhaseRows(record.Phases, childIndex)
			childIndex = phaseIndexByID(record.Phases, update.PhaseID)
			child = record.Phases[childIndex]
			if child.PhaseID == update.PhaseID &&
				child.Title == strings.TrimSpace(update.Title) &&
				child.Kind == "implementation_child" &&
				child.Order == update.Order {
				return record, nil
			}
			child.PhaseID = update.PhaseID
			child.Title = strings.TrimSpace(update.Title)
			child.Kind = "implementation_child"
			child.Order = update.Order
			child.UpdatedAt = now
			record.Phases[childIndex] = child
		} else {
			record.Phases = append(record.Phases, flowstore.FlowPhase{
				PhaseID:       update.PhaseID,
				ParentPhaseID: update.ParentPhaseID,
				Title:         strings.TrimSpace(update.Title),
				Kind:          "implementation_child",
				Status:        flowstore.PhasePending,
				Order:         update.Order,
				CreatedAt:     now,
				UpdatedAt:     now,
			})
		}
		record.Phases = flowstore.OrderedPhases(record.Phases)
		record.UpdatedAt = now
		record = flowstore.RefreshPhaseReadiness(record, now)
		return record, nil
	})
}

func (f *Fake) SetPlanLink(update flowstore.PlanLinkUpdate) (flowstore.FlowRecord, error) {
	if err := validateSafeID("flow", update.FlowID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	planID := strings.TrimSpace(update.PlanID)
	if planID == "" {
		return flowstore.FlowRecord{}, fmt.Errorf("plan id is required")
	}
	if err := validateSafeID("plan", planID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	planPath, ok := f.planPath(planID)
	if !ok {
		return flowstore.FlowRecord{}, fmt.Errorf("plan %q not found", planID)
	}
	if !filepath.IsAbs(planPath) {
		return flowstore.FlowRecord{}, fmt.Errorf("flow plan path must be absolute: %s", planPath)
	}
	if _, err := os.ReadFile(planPath); err != nil {
		return flowstore.FlowRecord{}, fmt.Errorf("read plan %q: %w", planID, err)
	}
	if supplied := strings.TrimSpace(update.PlanPath); supplied != "" {
		if !filepath.IsAbs(supplied) {
			return flowstore.FlowRecord{}, fmt.Errorf("flow plan path must be absolute: %s", supplied)
		}
		if filepath.Clean(supplied) != filepath.Clean(planPath) {
			return flowstore.FlowRecord{}, fmt.Errorf("flow plan path %q does not match plan %q path %q", filepath.Clean(supplied), planID, planPath)
		}
	}
	return f.updateFlow(update.FlowID, func(record flowstore.FlowRecord, now time.Time) (flowstore.FlowRecord, error) {
		if record.PlanID == planID && record.PlanPath == planPath {
			return record, nil
		}
		record.PlanID = planID
		record.PlanPath = planPath
		record.UpdatedAt = now
		return record, nil
	})
}

func (f *Fake) SetPR(update flowstore.PRUpdate) (flowstore.FlowRecord, error) {
	if err := validateSafeID("flow", update.FlowID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	return f.updateFlow(update.FlowID, func(record flowstore.FlowRecord, now time.Time) (flowstore.FlowRecord, error) {
		pr, err := validatePRUpdate(record, update)
		if err != nil {
			return flowstore.FlowRecord{}, err
		}
		if record.PR == pr {
			return record, nil
		}
		record.PR = pr
		record.UpdatedAt = now
		record = flowstore.RefreshPhaseReadiness(record, now)
		return record, nil
	})
}

func (f *Fake) SetMerge(update flowstore.MergeUpdate) (flowstore.FlowRecord, error) {
	if err := validateSafeID("flow", update.FlowID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	return f.updateFlow(update.FlowID, func(record flowstore.FlowRecord, now time.Time) (flowstore.FlowRecord, error) {
		merge, err := validateMergeUpdate(record, update)
		if err != nil {
			return flowstore.FlowRecord{}, err
		}
		if mergeEqual(record.Merge, merge) {
			return record, nil
		}
		record.Merge = merge
		record.UpdatedAt = now
		return record, nil
	})
}

func (f *Fake) SetAutoMode(update flowstore.AutoModeUpdate) (flowstore.FlowRecord, error) {
	if err := validateSafeID("flow", update.FlowID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	return f.updateFlow(update.FlowID, func(record flowstore.FlowRecord, now time.Time) (flowstore.FlowRecord, error) {
		if record.AutoMode == update.Enabled {
			return record, nil
		}
		record.AutoMode = update.Enabled
		record.UpdatedAt = now
		return record, nil
	})
}

func (f *Fake) SetStartMetadata(update flowstore.StartMetadataUpdate) (flowstore.FlowRecord, error) {
	if err := validateSafeID("flow", update.FlowID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	if strings.TrimSpace(update.WorktreePath) != "" && !filepath.IsAbs(update.WorktreePath) {
		return flowstore.FlowRecord{}, fmt.Errorf("flow worktree path must be absolute: %s", update.WorktreePath)
	}
	if strings.TrimSpace(update.PlanPath) != "" && !filepath.IsAbs(update.PlanPath) {
		return flowstore.FlowRecord{}, fmt.Errorf("flow plan path must be absolute: %s", update.PlanPath)
	}
	return f.updateFlow(update.FlowID, func(record flowstore.FlowRecord, now time.Time) (flowstore.FlowRecord, error) {
		if value := strings.TrimSpace(update.WorktreePath); value != "" {
			record.WorktreePath = filepath.Clean(value)
		}
		if value := strings.TrimSpace(update.Branch); value != "" {
			record.Branch = value
		}
		if value := strings.TrimSpace(update.BaseRef); value != "" {
			record.BaseRef = value
		}
		if value := strings.TrimSpace(update.Commit); value != "" {
			record.Commit = value
		}
		if value := strings.TrimSpace(update.PlanID); value != "" {
			record.PlanID = value
		}
		if value := strings.TrimSpace(update.PlanPath); value != "" {
			record.PlanPath = filepath.Clean(value)
		}
		record.UpdatedAt = now
		return record, nil
	})
}

func (f *Fake) AddPhaseLaunchID(update flowstore.PhaseLaunchUpdate) (flowstore.FlowRecord, error) {
	if err := validateSafeID("flow", update.FlowID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	requestedPhaseID := strings.TrimSpace(update.PhaseID)
	update.PhaseID = artifacts.NormalizePhaseID(update.PhaseID)
	if update.PhaseID == "" {
		return flowstore.FlowRecord{}, fmt.Errorf("phase id is required")
	}
	launchID := strings.TrimSpace(update.LaunchID)
	if launchID == "" {
		return flowstore.FlowRecord{}, fmt.Errorf("launch id is required")
	}
	return f.updateFlow(update.FlowID, func(record flowstore.FlowRecord, now time.Time) (flowstore.FlowRecord, error) {
		phaseIndex := phaseIndexPreferringExactID(record.Phases, requestedPhaseID)
		if phaseIndex < 0 {
			return flowstore.FlowRecord{}, fmt.Errorf("phase %q not found in flow %q", update.PhaseID, update.FlowID)
		}
		phase := record.Phases[phaseIndex]
		if update.RejectRunning && !update.Resume {
			if phase.Status == flowstore.PhaseRunning {
				return flowstore.FlowRecord{}, fmt.Errorf("flow phase %q is already running", update.PhaseID)
			}
			if !phaseLaunchableForFreshStart(record, phase) {
				return flowstore.FlowRecord{}, fmt.Errorf("flow phase %q is not launchable from status %q", update.PhaseID, phase.Status)
			}
		}
		if update.AutoLaunch {
			if err := validateAutoPhaseLaunch(record, phase); err != nil {
				return flowstore.FlowRecord{}, err
			}
		}
		if update.Resume && flowstore.PhaseStatusTerminal(phase.Status) {
			phase.LaunchIDs = appendUnique(phase.LaunchIDs, launchID)
			phase.PhaseID = update.PhaseID
			phase.UpdatedAt = now
			record.Phases[phaseIndex] = phase
			record.Phases = collapseDuplicatePhaseRows(record.Phases, phaseIndex)
			record.UpdatedAt = now
			record = flowstore.RefreshPhaseReadiness(record, now)
			return record, nil
		}
		phaseUpdate := flowstore.PhaseUpdate{FlowID: update.FlowID, PhaseID: update.PhaseID, Status: flowstore.PhaseRunning}
		if phase.Status == flowstore.PhaseNeedsAttention || phase.Status == flowstore.PhaseBlocked {
			phaseUpdate.Notes = fmt.Sprintf("Relaunched after %s.", phase.Status)
		}
		if err := validatePhaseUpdate(phase, phaseUpdate); err != nil {
			return flowstore.FlowRecord{}, err
		}
		phase.Status = flowstore.PhaseRunning
		phase.Outcome = ""
		if phaseUpdate.Notes != "" {
			phase.Notes = phaseUpdate.Notes
		}
		phase.LaunchIDs = appendUnique(phase.LaunchIDs, launchID)
		phase.PhaseID = update.PhaseID
		phase.UpdatedAt = now
		record.Phases[phaseIndex] = phase
		record.Phases = collapseDuplicatePhaseRows(record.Phases, phaseIndex)
		if phase.PhaseID == "merge" && phase.Status == flowstore.PhaseRunning {
			record.Merge = flowstore.Merge{Status: flowstore.MergePending}
		}
		record.UpdatedAt = now
		record = flowstore.RefreshPhaseReadiness(record, now)
		return record, nil
	})
}

func (f *Fake) ResetAwaitingSessionPhase(update flowstore.PhaseResetUpdate) (flowstore.FlowRecord, error) {
	if err := validateSafeID("flow", update.FlowID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	requestedPhaseID := strings.TrimSpace(update.PhaseID)
	update.PhaseID = artifacts.NormalizePhaseID(update.PhaseID)
	if update.PhaseID == "" {
		return flowstore.FlowRecord{}, fmt.Errorf("phase id is required")
	}
	return f.updateFlow(update.FlowID, func(record flowstore.FlowRecord, now time.Time) (flowstore.FlowRecord, error) {
		phaseIndex := phaseIndexPreferringExactID(record.Phases, requestedPhaseID)
		if phaseIndex < 0 {
			return flowstore.FlowRecord{}, fmt.Errorf("phase %q not found in flow %q", update.PhaseID, update.FlowID)
		}
		phase := record.Phases[phaseIndex]
		if phase.Status != flowstore.PhaseRunning {
			return flowstore.FlowRecord{}, fmt.Errorf("flow phase reset requires running await-session; %s is %s", phase.PhaseID, phase.Status)
		}
		if !flowstore.PhaseAwaitingSession(phase) {
			return flowstore.FlowRecord{}, fmt.Errorf("flow phase reset requires latest launch without an attached session")
		}
		if flowstore.PhaseSessionLaunchMismatch(phase) {
			return flowstore.FlowRecord{}, fmt.Errorf("flow phase reset requires attached sessions to match phase launch ids")
		}
		if !flowstore.PhasePredecessorsSatisfied(record, phase.PhaseID) {
			return flowstore.FlowRecord{}, fmt.Errorf("flow phase reset requires satisfied predecessors for %s", phase.PhaseID)
		}
		launchIDs, removedLaunchID, ok := removeLatestPhaseLaunchID(phase.LaunchIDs)
		if !ok {
			return flowstore.FlowRecord{}, fmt.Errorf("flow phase reset requires an orphan launch id")
		}
		phase.LaunchIDs = launchIDs
		phase.Status = flowstore.PhasePending
		phase.PhaseID = update.PhaseID
		phase.UpdatedAt = now
		record.Phases[phaseIndex] = phase
		record.Phases = collapseDuplicatePhaseRows(record.Phases, phaseIndex)
		if resetIndex := phaseIndexByID(record.Phases, update.PhaseID); resetIndex >= 0 {
			resetPhase := record.Phases[resetIndex]
			resetPhase.LaunchIDs = removePhaseLaunchID(resetPhase.LaunchIDs, removedLaunchID)
			if flowstore.PhaseSessionLaunchMismatch(resetPhase) {
				return flowstore.FlowRecord{}, fmt.Errorf("flow phase reset requires attached sessions to match phase launch ids")
			}
			record.Phases[resetIndex] = resetPhase
		}
		record.UpdatedAt = now
		record = flowstore.RefreshPhaseReadiness(record, now)
		resetIndex := phaseIndexByID(record.Phases, update.PhaseID)
		if resetIndex < 0 || record.Phases[resetIndex].Status != flowstore.PhaseReady {
			return flowstore.FlowRecord{}, fmt.Errorf("flow phase reset could not derive %s back to ready", update.PhaseID)
		}
		return record, nil
	})
}

func (f *Fake) AttachSession(update flowstore.SessionAttachUpdate) (flowstore.FlowRecord, error) {
	if err := validateSafeID("flow", update.FlowID); err != nil {
		return flowstore.FlowRecord{}, err
	}
	requestedPhaseID := strings.TrimSpace(update.PhaseID)
	update.PhaseID = artifacts.NormalizePhaseID(update.PhaseID)
	if update.PhaseID == "" {
		return flowstore.FlowRecord{}, fmt.Errorf("phase id is required")
	}
	if strings.TrimSpace(update.Session.Provider) == "" {
		return flowstore.FlowRecord{}, fmt.Errorf("session provider is required")
	}
	if strings.TrimSpace(update.Session.SessionID) == "" {
		return flowstore.FlowRecord{}, fmt.Errorf("session id is required")
	}
	return f.updateFlow(update.FlowID, func(record flowstore.FlowRecord, now time.Time) (flowstore.FlowRecord, error) {
		phaseIndex := phaseIndexPreferringExactID(record.Phases, requestedPhaseID)
		if phaseIndex < 0 {
			return flowstore.FlowRecord{}, fmt.Errorf("phase %q not found in flow %q", update.PhaseID, update.FlowID)
		}
		phase := record.Phases[phaseIndex]
		replaced := false
		for i, existing := range phase.Sessions {
			if existing.Provider == update.Session.Provider && existing.SessionID == update.Session.SessionID {
				phase.Sessions[i] = update.Session
				replaced = true
				break
			}
		}
		if !replaced {
			phase.Sessions = append(phase.Sessions, update.Session)
		}
		phase.PhaseID = update.PhaseID
		phase.UpdatedAt = now
		record.Phases[phaseIndex] = phase
		record.Phases = collapseDuplicatePhaseRows(record.Phases, phaseIndex)
		record.UpdatedAt = now
		return record, nil
	})
}

func (f *Fake) Delete(flowID string) error {
	if err := validateSafeID("flow", flowID); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.records[flowID]; !ok {
		return flowNotFound(flowID)
	}
	delete(f.records, flowID)
	return nil
}

func (f *Fake) updateFlow(flowID string, mutate func(flowstore.FlowRecord, time.Time) (flowstore.FlowRecord, error)) (flowstore.FlowRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.records[flowID]
	if !ok {
		return flowstore.FlowRecord{}, flowNotFound(flowID)
	}
	updated, err := mutate(cloneRecord(record), f.now())
	if err != nil {
		return flowstore.FlowRecord{}, err
	}
	updated = normalizeRecord(updated)
	f.records[flowID] = cloneRecord(updated)
	return cloneRecord(updated), nil
}

func (f *Fake) planPath(planID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	planPath, ok := f.planPaths[planID]
	return planPath, ok
}

func (f *Fake) syncLinkedPlanPhaseLocked(record flowstore.FlowRecord, phase flowstore.FlowPhase) error {
	planID := strings.TrimSpace(record.PlanID)
	if planID == "" || phase.Status != flowstore.PhaseCompleted {
		return nil
	}
	if f.brokenPlanMetadata[planID] {
		return fmt.Errorf("sync linked plan phase: plan %q metadata not found", planID)
	}
	planPhase, ok := f.planPhaseLocked(planID, phase.PhaseID)
	if !ok {
		return nil
	}
	if planPhase.Status == flowstore.PhaseCompleted {
		return nil
	}
	planPhase.Status = flowstore.PhaseCompleted
	phases := f.planPhases[planID]
	for i, existing := range phases {
		if artifacts.NormalizePhaseID(existing.PhaseID) == artifacts.NormalizePhaseID(phase.PhaseID) {
			phases[i] = planPhase
			f.planPhases[planID] = phases
			return nil
		}
	}
	return nil
}

func (f *Fake) planPhaseLocked(planID, phaseID string) (fakePlanPhase, bool) {
	want := artifacts.NormalizePhaseID(phaseID)
	for _, phase := range f.planPhases[planID] {
		if artifacts.NormalizePhaseID(phase.PhaseID) == want {
			return phase, true
		}
	}
	return fakePlanPhase{}, false
}

func markPhaseSyncNeedsAttention(record flowstore.FlowRecord, phase flowstore.FlowPhase, syncErr error, now time.Time) flowstore.FlowRecord {
	failedPhase := phase
	failedPhase.Status = flowstore.PhaseNeedsAttention
	failedPhase.Outcome = ""
	note := fmt.Sprintf("Linked plan phase sync failed: %v", syncErr)
	if strings.TrimSpace(failedPhase.Notes) != "" {
		failedPhase.Notes = strings.TrimSpace(failedPhase.Notes) + "\n" + note
	} else {
		failedPhase.Notes = note
	}
	failedPhase.UpdatedAt = now
	if failedIndex := phaseIndexByID(record.Phases, failedPhase.PhaseID); failedIndex >= 0 {
		record.Phases[failedIndex] = failedPhase
	}
	record.UpdatedAt = now
	record = flowstore.RefreshPhaseReadiness(record, now)
	record.Status = flowstore.DeriveStatus(record)
	return record
}

func defaultPhases(createdAt, updatedAt time.Time) []flowstore.FlowPhase {
	specs := []struct {
		id    string
		title string
		kind  string
	}{
		{"plan", "Plan", "plan"},
		{"plan-review", "Plan Review", "plan_review"},
		{"implementation", "Implementation", "implementation"},
		{"review-loop", "Review loop", "review_loop"},
		{"pr-creation", "PR creation", "pr_creation"},
		{"autoreview", "Autoreview", "autoreview"},
		{"merge", "Merge", "merge"},
	}
	phases := make([]flowstore.FlowPhase, 0, len(specs))
	for i, spec := range specs {
		status := flowstore.PhasePending
		if i == 0 {
			status = flowstore.PhaseReady
		}
		phases = append(phases, flowstore.FlowPhase{
			PhaseID:   spec.id,
			Title:     spec.title,
			Kind:      spec.kind,
			Status:    status,
			Order:     i + 1,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}
	return phases
}

func normalizeRecord(record flowstore.FlowRecord) flowstore.FlowRecord {
	if record.Merge.Status == "" {
		record.Merge.Status = flowstore.MergePending
	}
	for i := range record.Phases {
		if record.Phases[i].PhaseID == "plan-review" &&
			record.Phases[i].Status == flowstore.PhaseCompleted &&
			strings.TrimSpace(record.Phases[i].Outcome) == "" {
			record.Phases[i].Outcome = flowstore.OutcomeApproved
		}
	}
	if phaseIndexByID(record.Phases, "plan-review") >= 0 {
		record = flowstore.RefreshPhaseReadiness(record, record.UpdatedAt)
	}
	record.Status = flowstore.DeriveStatus(record)
	return record
}

func cloneRecord(record flowstore.FlowRecord) flowstore.FlowRecord {
	record.Phases = append([]flowstore.FlowPhase(nil), record.Phases...)
	for i := range record.Phases {
		record.Phases[i].LaunchIDs = append([]string(nil), record.Phases[i].LaunchIDs...)
		record.Phases[i].Sessions = append([]flowstore.Session(nil), record.Phases[i].Sessions...)
	}
	if record.Merge.MergedAt != nil {
		mergedAt := *record.Merge.MergedAt
		record.Merge.MergedAt = &mergedAt
	}
	return record
}

func validateSafeID(kind, id string) error {
	if !artifacts.IsSafeID(id) {
		return fmt.Errorf("invalid %s id %q", kind, id)
	}
	return nil
}

func flowNotFound(flowID string) error {
	return fmt.Errorf("flow %q not found: %w", flowID, flowstore.ErrFlowNotFound)
}

func defaultTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value
}

func validatePhaseUpdate(current flowstore.FlowPhase, update flowstore.PhaseUpdate) error {
	if strings.TrimSpace(update.Status) == "" {
		return fmt.Errorf("phase status is required")
	}
	if update.Status == flowstore.PhaseReady {
		return fmt.Errorf("cannot set phase status to ready; readiness is derived")
	}
	if !slices.Contains(flowstore.AgentSettablePhaseStatuses(), update.Status) {
		return fmt.Errorf("invalid phase status %q", update.Status)
	}
	if update.Status == flowstore.PhaseSkipped && strings.TrimSpace(update.Notes) == "" {
		return fmt.Errorf("skipped phase requires notes")
	}
	if err := validatePlanReviewUpdate(current, update); err != nil {
		return err
	}
	if current.Status == update.Status {
		return nil
	}
	if !slices.Contains(flowstore.AllowedNextPhaseStatuses(current.Status), update.Status) {
		message := fmt.Sprintf("invalid phase transition %s -> %s", current.Status, update.Status)
		if allowed := flowstore.AllowedNextPhaseStatuses(current.Status); len(allowed) > 0 {
			message += fmt.Sprintf("; allowed from %s: %s", current.Status, strings.Join(allowed, ", "))
		}
		if (current.Status == flowstore.PhaseNeedsAttention || current.Status == flowstore.PhaseBlocked) && update.Status == flowstore.PhaseCompleted {
			message += "; restart with --status running --notes before completing"
		}
		return fmt.Errorf("%s", message)
	}
	restarting := current.Status == flowstore.PhaseNeedsAttention || current.Status == flowstore.PhaseBlocked
	if restarting && update.Status == flowstore.PhaseRunning && strings.TrimSpace(update.Notes) == "" {
		return fmt.Errorf("restarting %s phase requires notes", current.Status)
	}
	return nil
}

func validatePlanReviewUpdate(current flowstore.FlowPhase, update flowstore.PhaseUpdate) error {
	if current.PhaseID != "plan-review" {
		return nil
	}
	if current.Status == flowstore.PhasePending && update.Status != flowstore.PhaseSkipped {
		return nil
	}
	outcome := strings.TrimSpace(update.Outcome)
	notes := strings.TrimSpace(update.Notes)
	if outcome == "" {
		switch update.Status {
		case flowstore.PhaseCompleted:
			return fmt.Errorf("plan-review completed requires outcome approved or approved_with_concerns")
		case flowstore.PhaseNeedsAttention:
			return fmt.Errorf("plan-review needs_attention requires outcome changes_requested")
		case flowstore.PhaseBlocked:
			return fmt.Errorf("plan-review blocked requires outcome blocked")
		}
		return nil
	}
	if update.Status == flowstore.PhaseBlocked && outcome != flowstore.OutcomeBlocked {
		return fmt.Errorf("plan-review blocked requires outcome blocked")
	}
	switch outcome {
	case flowstore.OutcomeApproved:
		if update.Status != flowstore.PhaseCompleted {
			return fmt.Errorf("plan-review outcome approved requires completed status")
		}
	case flowstore.OutcomeApprovedWithConcerns:
		if update.Status != flowstore.PhaseCompleted {
			return fmt.Errorf("plan-review outcome approved_with_concerns requires completed status")
		}
		if notes == "" {
			return fmt.Errorf("plan-review approved_with_concerns requires notes")
		}
	case flowstore.OutcomeChangesRequested:
		if update.Status != flowstore.PhaseNeedsAttention {
			return fmt.Errorf("plan-review outcome changes_requested requires needs_attention status")
		}
		if notes == "" {
			return fmt.Errorf("plan-review changes_requested requires notes")
		}
	case flowstore.OutcomeBlocked:
		if update.Status != flowstore.PhaseBlocked {
			return fmt.Errorf("plan-review blocked requires outcome blocked")
		}
		if notes == "" {
			return fmt.Errorf("plan-review blocked requires notes")
		}
	default:
		return fmt.Errorf("invalid plan-review outcome %q", outcome)
	}
	return nil
}

func validateChildPhaseUpdate(update flowstore.ChildPhaseUpdate) error {
	if err := validateSafeID("flow", update.FlowID); err != nil {
		return err
	}
	if err := validateSafeID("parent phase", update.ParentPhaseID); err != nil {
		return fmt.Errorf("invalid parent phase id: %w", err)
	}
	if update.ParentPhaseID != "implementation" {
		return fmt.Errorf("child phases can only be added under implementation")
	}
	if err := validateSafeID("phase", update.PhaseID); err != nil {
		return err
	}
	if update.PhaseID == update.ParentPhaseID {
		return fmt.Errorf("child phase id must differ from parent phase id")
	}
	if strings.TrimSpace(update.Title) == "" {
		return fmt.Errorf("child phase title is required")
	}
	if update.Order < 1 {
		return fmt.Errorf("child phase order must be positive")
	}
	return nil
}

func validatePRUpdate(record flowstore.FlowRecord, update flowstore.PRUpdate) (flowstore.PullRequest, error) {
	provider := strings.ToLower(strings.TrimSpace(update.Provider))
	if provider != "github" {
		return flowstore.PullRequest{}, fmt.Errorf("unsupported PR provider %q", update.Provider)
	}
	if update.Number <= 0 {
		return flowstore.PullRequest{}, fmt.Errorf("PR number must be positive")
	}
	prURL := strings.TrimSpace(update.URL)
	parsed, err := url.Parse(prURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return flowstore.PullRequest{}, fmt.Errorf("PR URL must be an absolute http(s) URL")
	}
	if err := validateGitHubPRURL(parsed, update.Number); err != nil {
		return flowstore.PullRequest{}, err
	}
	head := strings.TrimSpace(update.HeadBranch)
	if head == "" {
		return flowstore.PullRequest{}, fmt.Errorf("PR head branch is required")
	}
	base := strings.TrimSpace(update.BaseBranch)
	if base == "" {
		return flowstore.PullRequest{}, fmt.Errorf("PR base branch is required")
	}
	flowBranch := strings.TrimSpace(record.Branch)
	if flowBranch == "" {
		return flowstore.PullRequest{}, fmt.Errorf("flow branch is required before recording PR metadata")
	}
	if head != flowBranch {
		return flowstore.PullRequest{}, fmt.Errorf("PR head branch %q must match flow branch %q", head, flowBranch)
	}
	return flowstore.PullRequest{
		Provider:   provider,
		Number:     update.Number,
		URL:        prURL,
		HeadBranch: head,
		BaseBranch: base,
		Status:     strings.TrimSpace(update.Status),
	}, nil
}

func validateGitHubPRURL(parsed *url.URL, number int) error {
	host := strings.ToLower(parsed.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return fmt.Errorf("GitHub PR URL must use github.com")
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
		return fmt.Errorf("GitHub PR URL must have /owner/repo/pull/number path")
	}
	urlNumber, err := strconv.Atoi(parts[3])
	if err != nil || urlNumber <= 0 {
		return fmt.Errorf("GitHub PR URL must have numeric pull request number")
	}
	if urlNumber != number {
		return fmt.Errorf("GitHub PR URL number %d must match PR number %d", urlNumber, number)
	}
	return nil
}

func validateMergeUpdate(record flowstore.FlowRecord, update flowstore.MergeUpdate) (flowstore.Merge, error) {
	status := strings.TrimSpace(update.Status)
	switch status {
	case flowstore.MergeMerged:
		if !flowstore.HasPRTarget(record.PR) {
			return flowstore.Merge{}, fmt.Errorf("merge status merged requires existing PR metadata")
		}
		commit := strings.TrimSpace(update.Commit)
		if commit == "" {
			return flowstore.Merge{}, fmt.Errorf("merge status merged requires merge commit")
		}
		if update.MergedAt.IsZero() {
			return flowstore.Merge{}, fmt.Errorf("merge status merged requires merge timestamp")
		}
		phaseIndex := phaseIndexByID(record.Phases, "merge")
		if phaseIndex < 0 || record.Phases[phaseIndex].Status != flowstore.PhaseCompleted {
			return flowstore.Merge{}, fmt.Errorf("merge status merged requires completed merge phase")
		}
		mergedAt := update.MergedAt.UTC()
		return flowstore.Merge{Status: flowstore.MergeMerged, Commit: commit, MergedAt: &mergedAt}, nil
	case flowstore.MergeBlocked:
		phaseIndex := phaseIndexByID(record.Phases, "merge")
		if phaseIndex < 0 || record.Phases[phaseIndex].Status != flowstore.PhaseBlocked || strings.TrimSpace(record.Phases[phaseIndex].Notes) == "" {
			return flowstore.Merge{}, fmt.Errorf("merge status blocked requires blocked merge phase notes")
		}
		return flowstore.Merge{Status: flowstore.MergeBlocked}, nil
	default:
		return flowstore.Merge{}, fmt.Errorf("invalid merge status %q", update.Status)
	}
}

func mergeEqual(left, right flowstore.Merge) bool {
	if left.Status != right.Status || left.Commit != right.Commit {
		return false
	}
	switch {
	case left.MergedAt == nil && right.MergedAt == nil:
		return true
	case left.MergedAt == nil || right.MergedAt == nil:
		return false
	default:
		return left.MergedAt.Equal(*right.MergedAt)
	}
}

func phaseLaunchableForFreshStart(record flowstore.FlowRecord, phase flowstore.FlowPhase) bool {
	phaseID := artifacts.NormalizePhaseID(phase.PhaseID)
	if phase.Status == flowstore.PhaseReady {
		return true
	}
	return phaseID == "autoreview" &&
		(phase.Status == flowstore.PhaseNeedsAttention || phase.Status == flowstore.PhaseBlocked) &&
		flowstore.HasPRTarget(record.PR) &&
		flowstore.PhasePredecessorsSatisfied(record, phase.PhaseID)
}

func validateAutoPhaseLaunch(record flowstore.FlowRecord, phase flowstore.FlowPhase) error {
	phaseID := artifacts.NormalizePhaseID(phase.PhaseID)
	switch {
	case !record.AutoMode:
		return fmt.Errorf("auto launch for flow %q is disabled: %w", record.FlowID, flowstore.ErrAutoLaunchOutdated)
	case phaseID == "" || phaseID == "merge":
		return fmt.Errorf("auto launch target %q is not eligible: %w", phase.PhaseID, flowstore.ErrAutoLaunchOutdated)
	case phase.Status != flowstore.PhaseReady:
		return fmt.Errorf("auto launch target %q is %s, not ready: %w", phase.PhaseID, phase.Status, flowstore.ErrAutoLaunchOutdated)
	default:
		return nil
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func removeLatestPhaseLaunchID(values []string) ([]string, string, bool) {
	for i := len(values) - 1; i >= 0; i-- {
		if values[i] == "" {
			continue
		}
		out := append([]string(nil), values[:i]...)
		out = append(out, values[i+1:]...)
		return out, values[i], true
	}
	return values, "", false
}

func removePhaseLaunchID(values []string, target string) []string {
	if target == "" {
		return values
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}

func collapseDuplicatePhaseRows(phases []flowstore.FlowPhase, keepIndex int) []flowstore.FlowPhase {
	survivor := phases[keepIndex]
	want := artifacts.NormalizePhaseID(survivor.PhaseID)
	kept := make([]flowstore.FlowPhase, 0, len(phases))
	survivorPos := -1
	for i, phase := range phases {
		if i == keepIndex {
			survivorPos = len(kept)
			kept = append(kept, phase)
			continue
		}
		if artifacts.NormalizePhaseID(phase.PhaseID) != want {
			kept = append(kept, phase)
			continue
		}
		for _, launchID := range phase.LaunchIDs {
			survivor.LaunchIDs = appendUnique(survivor.LaunchIDs, launchID)
		}
		for _, session := range phase.Sessions {
			survivor.Sessions = appendUniqueSession(survivor.Sessions, session)
		}
		if survivor.Notes == "" {
			survivor.Notes = phase.Notes
		}
		if survivor.Summary == "" {
			survivor.Summary = phase.Summary
		}
	}
	kept[survivorPos] = survivor
	return kept
}

func appendUniqueSession(sessions []flowstore.Session, session flowstore.Session) []flowstore.Session {
	for _, existing := range sessions {
		if existing.Provider == session.Provider && existing.SessionID == session.SessionID {
			return sessions
		}
	}
	return append(sessions, session)
}

func phaseIndexPreferringExactID(phases []flowstore.FlowPhase, phaseID string) int {
	for i, phase := range phases {
		if phase.PhaseID == phaseID {
			return i
		}
	}
	return phaseIndexByID(phases, phaseID)
}

func phaseIndexByID(phases []flowstore.FlowPhase, phaseID string) int {
	want := artifacts.NormalizePhaseID(phaseID)
	for i, phase := range phases {
		if artifacts.NormalizePhaseID(phase.PhaseID) == want {
			return i
		}
	}
	return -1
}
