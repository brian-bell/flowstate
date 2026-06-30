// Package daemonclient talks to a local flowstate daemon over its loopback
// GraphQL API.
package daemonclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/daemoncoords"
	"github.com/google/uuid"
)

const (
	EnvURL   = "FLOWSTATE_DAEMON_URL"
	EnvToken = "FLOWSTATE_DAEMON_TOKEN"
)

var (
	ErrUnauthorized = errors.New("daemon unauthorized")
	ErrUnavailable  = errors.New("daemon unavailable")
)

type Options struct {
	EndpointURL string
	Token       string
	HTTPClient  *http.Client
	Coords      func() (daemoncoords.Coords, error)
	MaxAttempts int
	Backoff     func(attempt int) time.Duration
	Sleep       func(context.Context, time.Duration) error
	NewID       func() (string, error)
}

type Client struct {
	endpoint    string
	token       string
	httpClient  *http.Client
	maxAttempts int
	backoff     func(attempt int) time.Duration
	sleep       func(context.Context, time.Duration) error
	newID       func() (string, error)
}

type StartFlowInput struct {
	RepoPath        string
	Title           string
	Instructions    string
	BaseRef         string
	LaunchPlan      bool
	AgentCommand    string
	ReasoningEffort string
}

type StartFlowResult struct {
	Flow        flowstore.FlowRecord
	LaunchID    string
	Job         *RuntimeJob
	LaunchError string
}

type RuntimeJob struct {
	ID               string     `json:"id"`
	LaunchID         string     `json:"launchId"`
	FlowID           string     `json:"flowId"`
	PhaseID          string     `json:"phaseId"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"createdAt"`
	StartedAt        *time.Time `json:"startedAt"`
	EndedAt          *time.Time `json:"endedAt"`
	ExitCode         *int       `json:"exitCode"`
	Error            string     `json:"error"`
	PhaseUpdateError string     `json:"phaseUpdateError"`
	LogTail          string     `json:"logTail"`
	LogTruncated     bool       `json:"logTruncated"`
}

func New(opts Options) (*Client, error) {
	endpointURL := strings.TrimSpace(opts.EndpointURL)
	token := strings.TrimSpace(opts.Token)
	if endpointURL == "" {
		endpointURL = strings.TrimSpace(os.Getenv(EnvURL))
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv(EnvToken))
	}
	if endpointURL == "" || token == "" {
		readCoords := opts.Coords
		if readCoords == nil {
			readCoords = daemoncoords.Read
		}
		coords, err := readCoords()
		if err != nil {
			return nil, fmt.Errorf("resolve daemon coordinates: %w", err)
		}
		if endpointURL == "" {
			endpointURL = coords.URL
		}
		if token == "" {
			token = coords.Token
		}
	}
	endpoint, err := graphqlEndpoint(endpointURL)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, fmt.Errorf("daemon token is required")
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	backoff := opts.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	newID := opts.NewID
	if newID == nil {
		newID = func() (string, error) { return uuid.NewString(), nil }
	}
	return &Client{
		endpoint:    endpoint,
		token:       token,
		httpClient:  httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
		sleep:       sleep,
		newID:       newID,
	}, nil
}

func graphqlEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("daemon URL must be absolute: %q", raw)
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/graphql"
	}
	return parsed.String(), nil
}

func (c *Client) ListFlows(ctx context.Context, filter flowstore.FlowFilter) ([]flowstore.FlowRecord, error) {
	vars := map[string]any{"filter": nil}
	if strings.TrimSpace(filter.RepoPath) != "" {
		vars["filter"] = map[string]any{"repoPath": filter.RepoPath}
	}
	var data struct {
		Flows []flowDTO `json:"flows"`
	}
	if err := c.query(ctx, listFlowsQuery, vars, &data); err != nil {
		return nil, err
	}
	records := make([]flowstore.FlowRecord, 0, len(data.Flows))
	for _, dto := range data.Flows {
		records = append(records, dto.record())
	}
	return records, nil
}

func (c *Client) ReadFlow(ctx context.Context, flowID string) (flowstore.FlowRecord, error) {
	var data struct {
		Flow *flowDTO `json:"flow"`
	}
	if err := c.query(ctx, readFlowQuery, map[string]any{"id": flowID}, &data); err != nil {
		return flowstore.FlowRecord{}, err
	}
	if data.Flow == nil {
		return flowstore.FlowRecord{}, fmt.Errorf("flow %q not found: %w", flowID, flowstore.ErrFlowNotFound)
	}
	return data.Flow.record(), nil
}

func (c *Client) RestartFlowPhase(ctx context.Context, update flowstore.PhaseRestartUpdate) (flowstore.FlowRecord, flowstore.FlowPhase, error) {
	input := map[string]any{"flowId": update.FlowID, "phaseId": update.PhaseID}
	if update.Notes != "" {
		input["notes"] = update.Notes
	}
	var data struct {
		RestartFlowPhase struct {
			Flow  flowDTO      `json:"flow"`
			Phase flowPhaseDTO `json:"phase"`
		} `json:"restartFlowPhase"`
	}
	if err := c.mutation(ctx, restartFlowPhaseMutation, map[string]any{"input": input}, &data); err != nil {
		return flowstore.FlowRecord{}, flowstore.FlowPhase{}, err
	}
	return data.RestartFlowPhase.Flow.record(), data.RestartFlowPhase.Phase.phase(), nil
}

func (c *Client) AddFlowChildPhase(ctx context.Context, update flowstore.ChildPhaseUpdate) (flowstore.FlowRecord, flowstore.FlowPhase, error) {
	var data struct {
		AddFlowChildPhase struct {
			Flow  flowDTO      `json:"flow"`
			Phase flowPhaseDTO `json:"phase"`
		} `json:"addFlowChildPhase"`
	}
	if err := c.mutation(ctx, addFlowChildPhaseMutation, map[string]any{"input": map[string]any{
		"flowId":        update.FlowID,
		"parentPhaseId": update.ParentPhaseID,
		"phaseId":       update.PhaseID,
		"title":         update.Title,
		"order":         update.Order,
	}}, &data); err != nil {
		return flowstore.FlowRecord{}, flowstore.FlowPhase{}, err
	}
	return data.AddFlowChildPhase.Flow.record(), data.AddFlowChildPhase.Phase.phase(), nil
}

func (c *Client) SetFlowPlanLink(ctx context.Context, update flowstore.PlanLinkUpdate) (flowstore.FlowRecord, error) {
	var data struct {
		SetFlowPlanLink struct {
			Flow flowDTO `json:"flow"`
		} `json:"setFlowPlanLink"`
	}
	if err := c.mutation(ctx, setFlowPlanLinkMutation, map[string]any{"input": map[string]any{
		"flowId":   update.FlowID,
		"planId":   update.PlanID,
		"planPath": update.PlanPath,
	}}, &data); err != nil {
		return flowstore.FlowRecord{}, err
	}
	return data.SetFlowPlanLink.Flow.record(), nil
}

func (c *Client) SetFlowPR(ctx context.Context, update flowstore.PRUpdate) (flowstore.FlowRecord, flowstore.PullRequest, error) {
	input := map[string]any{
		"flowId":     update.FlowID,
		"provider":   update.Provider,
		"number":     update.Number,
		"url":        update.URL,
		"headBranch": update.HeadBranch,
		"baseBranch": update.BaseBranch,
	}
	if update.Status != "" {
		input["status"] = update.Status
	}
	var data struct {
		SetFlowPR struct {
			Flow flowDTO        `json:"flow"`
			PR   pullRequestDTO `json:"pr"`
		} `json:"setFlowPR"`
	}
	if err := c.mutation(ctx, setFlowPRMutation, map[string]any{"input": input}, &data); err != nil {
		return flowstore.FlowRecord{}, flowstore.PullRequest{}, err
	}
	return data.SetFlowPR.Flow.record(), data.SetFlowPR.PR.pullRequest(), nil
}

func (c *Client) SetFlowMerge(ctx context.Context, update flowstore.MergeUpdate) (flowstore.FlowRecord, flowstore.Merge, error) {
	input := map[string]any{"flowId": update.FlowID, "status": update.Status}
	if update.Commit != "" {
		input["commit"] = update.Commit
	}
	if !update.MergedAt.IsZero() {
		input["mergedAt"] = update.MergedAt
	}
	var data struct {
		SetFlowMerge struct {
			Flow  flowDTO  `json:"flow"`
			Merge mergeDTO `json:"merge"`
		} `json:"setFlowMerge"`
	}
	if err := c.mutation(ctx, setFlowMergeMutation, map[string]any{"input": input}, &data); err != nil {
		return flowstore.FlowRecord{}, flowstore.Merge{}, err
	}
	return data.SetFlowMerge.Flow.record(), data.SetFlowMerge.Merge.merge(), nil
}

func (c *Client) SetFlowAutoMode(ctx context.Context, update flowstore.AutoModeUpdate) (flowstore.FlowRecord, error) {
	var data struct {
		SetFlowAutoMode struct {
			Flow flowDTO `json:"flow"`
		} `json:"setFlowAutoMode"`
	}
	if err := c.mutation(ctx, setFlowAutoModeMutation, map[string]any{"input": map[string]any{
		"flowId":  update.FlowID,
		"enabled": update.Enabled,
	}}, &data); err != nil {
		return flowstore.FlowRecord{}, err
	}
	return data.SetFlowAutoMode.Flow.record(), nil
}

func (c *Client) DeleteFlow(ctx context.Context, flowID string) (string, error) {
	var data struct {
		DeleteFlow struct {
			DeletedID string `json:"deletedId"`
		} `json:"deleteFlow"`
	}
	if err := c.mutation(ctx, deleteFlowMutation, map[string]any{"id": flowID}, &data); err != nil {
		return "", err
	}
	return data.DeleteFlow.DeletedID, nil
}

func (c *Client) StartFlow(ctx context.Context, input StartFlowInput) (StartFlowResult, error) {
	graphInput := map[string]any{
		"repoPath":     input.RepoPath,
		"title":        input.Title,
		"instructions": input.Instructions,
		"launchPlan":   input.LaunchPlan,
	}
	if input.BaseRef != "" {
		graphInput["baseRef"] = input.BaseRef
	}
	if input.AgentCommand != "" {
		graphInput["agentCommand"] = input.AgentCommand
	}
	if input.ReasoningEffort != "" {
		graphInput["reasoningEffort"] = input.ReasoningEffort
	}
	var data struct {
		StartFlow struct {
			Flow        flowDTO     `json:"flow"`
			LaunchID    *string     `json:"launchId"`
			Job         *RuntimeJob `json:"job"`
			LaunchError string      `json:"launchError"`
		} `json:"startFlow"`
	}
	if err := c.mutation(ctx, startFlowMutation, map[string]any{"input": graphInput}, &data); err != nil {
		return StartFlowResult{}, err
	}
	result := StartFlowResult{
		Flow:        data.StartFlow.Flow.record(),
		Job:         data.StartFlow.Job,
		LaunchError: data.StartFlow.LaunchError,
	}
	if data.StartFlow.LaunchID != nil {
		result.LaunchID = *data.StartFlow.LaunchID
	}
	return result, nil
}

func (c *Client) query(ctx context.Context, query string, variables map[string]any, out any) error {
	return c.execute(ctx, graphQLRequest{Query: query, Variables: variables}, "", out)
}

func (c *Client) mutation(ctx context.Context, query string, variables map[string]any, out any) error {
	idempotencyKey, err := c.newID()
	if err != nil {
		return fmt.Errorf("mint idempotency key: %w", err)
	}
	return c.execute(ctx, graphQLRequest{Query: query, Variables: variables}, idempotencyKey, out)
}

func (c *Client) execute(ctx context.Context, payload graphQLRequest, idempotencyKey string, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode GraphQL request: %w", err)
	}
	attempts := c.maxAttempts
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build GraphQL request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.token)
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		res, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt < attempts {
				if err := c.sleep(ctx, c.backoff(attempt)); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
		}
		data, readErr := readAndClose(res.Body)
		if res.StatusCode == http.StatusUnauthorized {
			return ErrUnauthorized
		}
		if readErr != nil {
			return fmt.Errorf("read GraphQL response: %w", readErr)
		}
		if res.StatusCode >= 500 {
			lastErr = fmt.Errorf("daemon GraphQL status %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
			if attempt < attempts {
				if err := c.sleep(ctx, c.backoff(attempt)); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return fmt.Errorf("daemon GraphQL status %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
		}
		var envelope graphQLResponse
		if err := json.Unmarshal(data, &envelope); err != nil {
			return fmt.Errorf("decode GraphQL response: %w", err)
		}
		if len(envelope.Errors) > 0 {
			return graphQLErrors(envelope.Errors)
		}
		if out == nil {
			return nil
		}
		if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
			return nil
		}
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode GraphQL data: %w", err)
		}
		return nil
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
}

func readAndClose(body io.ReadCloser) ([]byte, error) {
	defer body.Close()
	return io.ReadAll(body)
}

func defaultBackoff(attempt int) time.Duration {
	base := time.Duration(attempt) * 100 * time.Millisecond
	jitter := time.Duration(rand.Int63n(int64(50 * time.Millisecond)))
	return base + jitter
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphQLError  `json:"errors"`
}

type graphQLError struct {
	Message string `json:"message"`
}

func graphQLErrors(errors []graphQLError) error {
	messages := make([]string, 0, len(errors))
	for _, gqlErr := range errors {
		messages = append(messages, gqlErr.Message)
	}
	return fmt.Errorf("daemon GraphQL error: %s", strings.Join(messages, "; "))
}

type flowDTO struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Instructions string         `json:"instructions"`
	StatusRaw    string         `json:"statusRaw"`
	RepoPath     string         `json:"repoPath"`
	WorktreePath string         `json:"worktreePath"`
	Branch       string         `json:"branch"`
	BaseRef      string         `json:"baseRef"`
	Commit       string         `json:"commit"`
	PlanID       string         `json:"planId"`
	PlanPath     string         `json:"planPath"`
	AutoMode     bool           `json:"autoMode"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
	PR           pullRequestDTO `json:"pr"`
	Merge        mergeDTO       `json:"merge"`
	Phases       []flowPhaseDTO `json:"phases"`
}

func (dto flowDTO) record() flowstore.FlowRecord {
	phases := make([]flowstore.FlowPhase, 0, len(dto.Phases))
	for _, phase := range dto.Phases {
		phases = append(phases, phase.phase())
	}
	return flowstore.FlowRecord{
		FlowID:       dto.ID,
		Title:        dto.Title,
		Instructions: dto.Instructions,
		Status:       dto.StatusRaw,
		RepoPath:     dto.RepoPath,
		WorktreePath: dto.WorktreePath,
		Branch:       dto.Branch,
		BaseRef:      dto.BaseRef,
		Commit:       dto.Commit,
		PlanID:       dto.PlanID,
		PlanPath:     dto.PlanPath,
		PR:           dto.PR.pullRequest(),
		Merge:        dto.Merge.merge(),
		AutoMode:     dto.AutoMode,
		Phases:       phases,
		CreatedAt:    dto.CreatedAt,
		UpdatedAt:    dto.UpdatedAt,
	}
}

type flowPhaseDTO struct {
	PhaseID       string       `json:"phaseId"`
	ParentPhaseID string       `json:"parentPhaseId"`
	Title         string       `json:"title"`
	Kind          string       `json:"kind"`
	StatusRaw     string       `json:"statusRaw"`
	Order         int          `json:"order"`
	Outcome       string       `json:"outcome"`
	Notes         string       `json:"notes"`
	Summary       string       `json:"summary"`
	LaunchIDs     []string     `json:"launchIds"`
	Sessions      []sessionDTO `json:"sessions"`
	CreatedAt     time.Time    `json:"createdAt"`
	UpdatedAt     time.Time    `json:"updatedAt"`
}

func (dto flowPhaseDTO) phase() flowstore.FlowPhase {
	sessions := make([]flowstore.Session, 0, len(dto.Sessions))
	for _, session := range dto.Sessions {
		sessions = append(sessions, session.session())
	}
	return flowstore.FlowPhase{
		PhaseID:       dto.PhaseID,
		ParentPhaseID: dto.ParentPhaseID,
		Title:         dto.Title,
		Kind:          dto.Kind,
		Status:        dto.StatusRaw,
		Order:         dto.Order,
		Outcome:       dto.Outcome,
		Notes:         dto.Notes,
		Summary:       dto.Summary,
		LaunchIDs:     append([]string(nil), dto.LaunchIDs...),
		Sessions:      sessions,
		CreatedAt:     dto.CreatedAt,
		UpdatedAt:     dto.UpdatedAt,
	}
}

type sessionDTO struct {
	Provider       string     `json:"provider"`
	SessionID      string     `json:"sessionId"`
	LaunchID       string     `json:"launchId"`
	Status         string     `json:"status"`
	StartedAt      *time.Time `json:"startedAt"`
	EndedAt        *time.Time `json:"endedAt"`
	TranscriptPath string     `json:"transcriptPath"`
}

func (dto sessionDTO) session() flowstore.Session {
	session := flowstore.Session{
		Provider:       dto.Provider,
		SessionID:      dto.SessionID,
		LaunchID:       dto.LaunchID,
		Status:         dto.Status,
		TranscriptPath: dto.TranscriptPath,
	}
	if dto.StartedAt != nil {
		session.StartedAt = *dto.StartedAt
	}
	if dto.EndedAt != nil {
		session.EndedAt = *dto.EndedAt
	}
	return session
}

type pullRequestDTO struct {
	Provider   string `json:"provider"`
	Number     int    `json:"number"`
	URL        string `json:"url"`
	HeadBranch string `json:"headBranch"`
	BaseBranch string `json:"baseBranch"`
	Status     string `json:"status"`
}

func (dto pullRequestDTO) pullRequest() flowstore.PullRequest {
	return flowstore.PullRequest{
		Provider:   dto.Provider,
		Number:     dto.Number,
		URL:        dto.URL,
		HeadBranch: dto.HeadBranch,
		BaseBranch: dto.BaseBranch,
		Status:     dto.Status,
	}
}

type mergeDTO struct {
	Status   string     `json:"status"`
	Commit   string     `json:"commit"`
	MergedAt *time.Time `json:"mergedAt"`
}

func (dto mergeDTO) merge() flowstore.Merge {
	return flowstore.Merge{
		Status:   dto.Status,
		Commit:   dto.Commit,
		MergedAt: dto.MergedAt,
	}
}

const flowFields = `id
title
instructions
statusRaw
repoPath
worktreePath
branch
baseRef
commit
planId
planPath
autoMode
createdAt
updatedAt
pr { provider number url headBranch baseBranch status }
merge { status commit mergedAt }
phases {
	phaseId
	parentPhaseId
	title
	kind
	statusRaw
	order
	outcome
	notes
	summary
	launchIds
	sessions { provider sessionId launchId status startedAt endedAt transcriptPath }
	createdAt
	updatedAt
}`

const listFlowsQuery = `query($filter: FlowFilterInput) {
	flows(filter: $filter) {
		` + flowFields + `
	}
}`

const readFlowQuery = `query($id: ID!) {
	flow(id: $id) {
		` + flowFields + `
	}
}`

const restartFlowPhaseMutation = `mutation($input: RestartFlowPhaseInput!) {
	restartFlowPhase(input: $input) {
		flow { ` + flowFields + ` }
		phase {
			phaseId parentPhaseId title kind statusRaw order outcome notes summary launchIds
			sessions { provider sessionId launchId status startedAt endedAt transcriptPath }
			createdAt updatedAt
		}
	}
}`

const addFlowChildPhaseMutation = `mutation($input: AddFlowChildPhaseInput!) {
	addFlowChildPhase(input: $input) {
		flow { ` + flowFields + ` }
		phase {
			phaseId parentPhaseId title kind statusRaw order outcome notes summary launchIds
			sessions { provider sessionId launchId status startedAt endedAt transcriptPath }
			createdAt updatedAt
		}
	}
}`

const setFlowPlanLinkMutation = `mutation($input: SetFlowPlanLinkInput!) {
	setFlowPlanLink(input: $input) { flow { ` + flowFields + ` } }
}`

const setFlowPRMutation = `mutation($input: SetFlowPRInput!) {
	setFlowPR(input: $input) {
		flow { ` + flowFields + ` }
		pr { provider number url headBranch baseBranch status }
	}
}`

const setFlowMergeMutation = `mutation($input: SetFlowMergeInput!) {
	setFlowMerge(input: $input) {
		flow { ` + flowFields + ` }
		merge { status commit mergedAt }
	}
}`

const setFlowAutoModeMutation = `mutation($input: SetFlowAutoModeInput!) {
	setFlowAutoMode(input: $input) { flow { ` + flowFields + ` } }
}`

const deleteFlowMutation = `mutation($id: ID!) {
	deleteFlow(id: $id) { deletedId }
}`

const startFlowMutation = `mutation($input: StartFlowInput!) {
	startFlow(input: $input) {
		flow { ` + flowFields + ` }
		launchId
		launchError
		job {
			id launchId flowId phaseId status createdAt startedAt endedAt exitCode
			error phaseUpdateError logTail logTruncated
		}
	}
}`
