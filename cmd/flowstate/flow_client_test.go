package main

import (
	"context"
	"fmt"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/daemonclient"
)

type testFlowClient struct {
	store *flowstore.Store
}

func newTestFlowClient(t testFataler, stateRoot string, deps runDeps) (daemonclient.FlowClient, error) {
	t.Helper()
	root, err := resolveTestFlowRoot(stateRoot, deps)
	if err != nil {
		return nil, err
	}
	store, err := flowstore.NewStore(flowstore.StoreOptions{Root: root})
	if err != nil {
		return nil, err
	}
	return testFlowClient{store: store}, nil
}

func resolveTestFlowRoot(stateRoot string, deps runDeps) (string, error) {
	if stateRoot != "" {
		return stateRoot, nil
	}
	if root := deps.getenv("FLOWSTATE_FLOW_STATE_ROOT"); root != "" {
		return root, nil
	}
	if root := deps.getenv("FLOWSTATE_PLAN_STATE_ROOT"); root != "" {
		return root, nil
	}
	if root := deps.getenv("FLOWSTATE_SESSION_STATE_ROOT"); root != "" {
		return root, nil
	}
	cfg, err := deps.loadConfig()
	if err != nil {
		return "", fmt.Errorf("error loading config: %w", err)
	}
	return cfg.Sessions.Root, nil
}

type testFataler interface {
	Helper()
}

func (c testFlowClient) ListFlows(ctx context.Context, filter flowstore.FlowFilter) ([]flowstore.FlowRecord, error) {
	_ = ctx
	return c.store.List(filter)
}

func (c testFlowClient) ReadFlow(ctx context.Context, flowID string) (flowstore.FlowRecord, error) {
	_ = ctx
	return c.store.Read(flowID)
}

func (c testFlowClient) ListFlowViews(ctx context.Context, filter flowstore.FlowFilter) ([]daemonclient.FlowView, error) {
	records, err := c.ListFlows(ctx, filter)
	if err != nil {
		return nil, err
	}
	views := make([]daemonclient.FlowView, 0, len(records))
	for _, record := range records {
		views = append(views, daemonclient.FlowView{Record: record})
	}
	return views, nil
}

func (c testFlowClient) ReadFlowView(ctx context.Context, flowID string) (daemonclient.FlowView, error) {
	record, err := c.ReadFlow(ctx, flowID)
	if err != nil {
		return daemonclient.FlowView{}, err
	}
	return daemonclient.FlowView{Record: record}, nil
}

func (c testFlowClient) CreateRawFlow(ctx context.Context, record flowstore.FlowRecord) (flowstore.FlowRecord, error) {
	_ = ctx
	return c.store.Create(record)
}

func (c testFlowClient) SetPhase(ctx context.Context, update flowstore.PhaseUpdate) (flowstore.FlowRecord, flowstore.FlowPhase, error) {
	_ = ctx
	record, err := c.store.SetPhase(update)
	if err != nil {
		return flowstore.FlowRecord{}, flowstore.FlowPhase{}, err
	}
	phase, ok := flowPhaseByID(record, update.PhaseID)
	if !ok {
		return flowstore.FlowRecord{}, flowstore.FlowPhase{}, fmt.Errorf("phase %q not found in updated flow %q", update.PhaseID, update.FlowID)
	}
	return record, phase, nil
}

func (c testFlowClient) RestartFlowPhase(ctx context.Context, update flowstore.PhaseRestartUpdate) (flowstore.FlowRecord, flowstore.FlowPhase, error) {
	_ = ctx
	record, err := c.store.RestartPhase(update)
	if err != nil {
		return flowstore.FlowRecord{}, flowstore.FlowPhase{}, err
	}
	phase, ok := flowPhaseByID(record, update.PhaseID)
	if !ok {
		return flowstore.FlowRecord{}, flowstore.FlowPhase{}, fmt.Errorf("phase %q not found in updated flow %q", update.PhaseID, update.FlowID)
	}
	return record, phase, nil
}

func (c testFlowClient) AddFlowChildPhase(ctx context.Context, update flowstore.ChildPhaseUpdate) (flowstore.FlowRecord, flowstore.FlowPhase, error) {
	_ = ctx
	record, err := c.store.AddChildPhase(update)
	if err != nil {
		return flowstore.FlowRecord{}, flowstore.FlowPhase{}, err
	}
	phase, ok := flowPhaseByID(record, update.PhaseID)
	if !ok {
		return flowstore.FlowRecord{}, flowstore.FlowPhase{}, fmt.Errorf("phase %q not found in updated flow %q", update.PhaseID, update.FlowID)
	}
	return record, phase, nil
}

func (c testFlowClient) SetFlowPlanLink(ctx context.Context, update flowstore.PlanLinkUpdate) (flowstore.FlowRecord, error) {
	_ = ctx
	return c.store.SetPlanLink(update)
}

func (c testFlowClient) SetFlowPR(ctx context.Context, update flowstore.PRUpdate) (flowstore.FlowRecord, flowstore.PullRequest, error) {
	_ = ctx
	record, err := c.store.SetPR(update)
	if err != nil {
		return flowstore.FlowRecord{}, flowstore.PullRequest{}, err
	}
	return record, record.PR, nil
}

func (c testFlowClient) SetFlowMerge(ctx context.Context, update flowstore.MergeUpdate) (flowstore.FlowRecord, flowstore.Merge, error) {
	_ = ctx
	record, err := c.store.SetMerge(update)
	if err != nil {
		return flowstore.FlowRecord{}, flowstore.Merge{}, err
	}
	return record, record.Merge, nil
}

func (c testFlowClient) SetFlowAutoMode(ctx context.Context, update flowstore.AutoModeUpdate) (flowstore.FlowRecord, error) {
	_ = ctx
	return c.store.SetAutoMode(update)
}

func (c testFlowClient) DeleteFlow(ctx context.Context, flowID string) (string, error) {
	_ = ctx
	if err := c.store.Delete(flowID); err != nil {
		return "", err
	}
	return flowID, nil
}

func (c testFlowClient) StartFlow(ctx context.Context, input daemonclient.StartFlowInput) (daemonclient.StartFlowResult, error) {
	record, err := c.CreateRawFlow(ctx, flowstore.FlowRecord{
		RepoPath:     input.RepoPath,
		Title:        input.Title,
		Instructions: input.Instructions,
		BaseRef:      input.BaseRef,
	})
	if err != nil {
		return daemonclient.StartFlowResult{}, err
	}
	return daemonclient.StartFlowResult{Flow: record}, nil
}

func (c testFlowClient) LaunchFlowPhase(context.Context, string, string, string, string) (daemonclient.LaunchFlowPhaseResult, error) {
	return daemonclient.LaunchFlowPhaseResult{}, fmt.Errorf("test flow client does not launch runtime jobs")
}

func (c testFlowClient) CancelRuntimeJob(context.Context, string) (daemonclient.RuntimeJob, error) {
	return daemonclient.RuntimeJob{}, fmt.Errorf("test flow client does not cancel runtime jobs")
}
