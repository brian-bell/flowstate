package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/agent"
	"github.com/brian-bell/flowstate/config"
	"github.com/brian-bell/flowstate/flowlaunch"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/daemonclient"
	"github.com/brian-bell/flowstate/internal/daemoncoords"
	"github.com/brian-bell/flowstate/internal/version"
	"github.com/brian-bell/flowstate/model"
	"github.com/brian-bell/flowstate/planstore"
	"github.com/brian-bell/flowstate/scanner"
	"github.com/brian-bell/flowstate/server"
	"github.com/brian-bell/flowstate/sessions"
	"github.com/brian-bell/flowstate/ui"
)

func main() {
	if err := run(os.Args, runDeps{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type runDeps struct {
	loadConfig              func() (config.Config, error)
	getenv                  func(string) string
	getwd                   func() (string, error)
	scan                    func(scanner.ScanOptions) ([]scanner.Repo, error)
	startProgram            func([]scanner.Repo, config.Config) error
	startProgramWithOptions func([]scanner.Repo, startProgramOptions) error
	serve                   func(context.Context, serveOptions) error
	newFlowClient           func(string) (daemonclient.FlowClient, error)
	stdin                   io.Reader
	stdout                  io.Writer
}

type serveOptions = server.Options

type startProgramOptions struct {
	Config         config.Config
	ScanRepos      func() ([]scanner.Repo, error)
	RepoCreateRoot string
}

func run(args []string, deps runDeps) error {
	deps = fillRunDeps(deps)
	if len(args) == 2 && isHelpArg(args[1]) {
		printMainHelp(deps.stdout)
		return nil
	}
	if len(args) > 1 && args[1] == "session-hook" {
		return runSessionHook(args, deps)
	}
	if len(args) > 1 && args[1] == "plan" {
		return runPlan(args, deps)
	}
	if len(args) > 1 && args[1] == "flow" {
		return runFlow(args, deps)
	}
	if len(args) > 1 && args[1] == "serve" {
		return runServe(args, deps)
	}

	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	versionFlag := flags.Bool("version", false, "print version and exit")
	flags.BoolVar(versionFlag, "v", false, "print version and exit")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return unknownCommandError(flags.Arg(0), mainCommands, mainHelpText)
	}

	if *versionFlag {
		fmt.Fprintln(deps.stdout, version.String())
		return nil
	}

	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	root := cfg.Scan.Root
	if envRoot := deps.getenv("WORKTREE_ROOT"); envRoot != "" {
		root = envRoot
	}
	repoCreateRoot, err := scanner.ResolveRoot(root)
	if err != nil {
		return fmt.Errorf("error resolving scan root: %w", err)
	}

	repos, err := deps.scan(scanner.ScanOptions{
		Root:     root,
		MaxDepth: cfg.Scan.MaxDepth,
	})
	if err != nil {
		return fmt.Errorf("error scanning repos: %w", err)
	}

	scanOptions := scanner.ScanOptions{
		Root:     root,
		MaxDepth: cfg.Scan.MaxDepth,
	}
	if err := deps.startProgramWithOptions(repos, startProgramOptions{
		Config:         cfg,
		RepoCreateRoot: repoCreateRoot,
		ScanRepos: func() ([]scanner.Repo, error) {
			return deps.scan(scanOptions)
		},
	}); err != nil {
		return fmt.Errorf("error: %w", err)
	}
	return nil
}

var mainCommands = []string{"plan", "flow", "serve", "session-hook"}

func isHelpArg(arg string) bool {
	return arg == "--help" || arg == "-h" || arg == "help"
}

func printMainHelp(w io.Writer) {
	io.WriteString(w, mainHelpText)
}

const mainHelpText = `Usage: flowstate [--version] [command]

Launch the Flow TUI, or use a command to persist agent artifacts.

Commands:
  plan          Save, list, read, and update saved plans.
  flow          Create, inspect, and update Flow records.
  serve         Start the secure local HTTP server.
  session-hook  Capture Claude or Codex session hook payloads.

Flags:
  --version, -v  Print version and exit.
  --help, -h     Print this help and exit.

Examples:
  flowstate
  flowstate plan --help
  flowstate flow --help
  flowstate serve --listen 127.0.0.1:0
  flowstate session-hook --provider codex
`

func unknownCommandError(got string, valid []string, usage string) error {
	if suggestion := nearestCommand(got, valid); suggestion != "" {
		return fmt.Errorf("unknown command %q; did you mean %q?\n\n%s", got, suggestion, usage)
	}
	return fmt.Errorf("unknown command %q\n\n%s", got, usage)
}

func nearestCommand(got string, valid []string) string {
	best := ""
	bestDistance := 3
	for _, candidate := range valid {
		distance := editDistance(got, candidate)
		if distance < bestDistance {
			best = candidate
			bestDistance = distance
		}
	}
	return best
}

func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr := make([]int, len(b)+1)
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = minInt(
				curr[j-1]+1,
				prev[j]+1,
				prev[j-1]+cost,
			)
		}
		prev = curr
	}
	return prev[len(b)]
}

func minInt(values ...int) int {
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

func parseCommandFlags(flags *flag.FlagSet, args []string) (bool, error) {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func fillRunDeps(deps runDeps) runDeps {
	if deps.loadConfig == nil {
		deps.loadConfig = func() (config.Config, error) {
			return config.Load()
		}
	}
	if deps.getenv == nil {
		deps.getenv = os.Getenv
	}
	if deps.getwd == nil {
		deps.getwd = os.Getwd
	}
	if deps.scan == nil {
		deps.scan = scanner.Scan
	}
	if deps.startProgramWithOptions == nil {
		if deps.startProgram != nil {
			deps.startProgramWithOptions = func(repos []scanner.Repo, opts startProgramOptions) error {
				return deps.startProgram(repos, opts.Config)
			}
		} else {
			deps.startProgramWithOptions = startProgram
		}
	}
	if deps.serve == nil {
		deps.serve = server.Run
	}
	if deps.newFlowClient == nil {
		deps.newFlowClient = func(stateRoot string) (daemonclient.FlowClient, error) {
			opts := daemonclient.Options{}
			if stateRoot != "" {
				opts.Coords = func() (daemoncoords.Coords, error) {
					return daemoncoords.ReadForStateRoot(stateRoot)
				}
			}
			return daemonclient.New(opts)
		}
	}
	if deps.stdin == nil {
		deps.stdin = os.Stdin
	}
	if deps.stdout == nil {
		deps.stdout = os.Stdout
	}
	return deps
}

func runServe(args []string, deps runDeps) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {
		io.WriteString(deps.stdout, serveHelpText)
	}
	listen := flags.String("listen", "127.0.0.1:0", "local listen address")
	if ok, err := parseCommandFlags(flags, args[2:]); err != nil {
		return err
	} else if ok {
		return nil
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("serve accepts no positional arguments")
	}
	if err := server.ValidateListenAddress(*listen); err != nil {
		return err
	}
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}
	stateRoot, err := runtimeArtifactRootWithEnv(cfg, deps.getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := deps.serve(ctx, serveOptions{
		Listen:                *listen,
		StateRoot:             stateRoot,
		AgentCommand:          cfg.Agent.Command,
		CodexReasoningEffort:  cfg.Agent.CodexReasoningEffort,
		ClaudeReasoningEffort: cfg.Agent.ClaudeReasoningEffort,
		FlowPromptTemplates: flowlaunch.PromptTemplates{
			Plan:           cfg.FlowPrompts.Plan,
			PlanReview:     cfg.FlowPrompts.PlanReview,
			Implementation: cfg.FlowPrompts.Implementation,
			ReviewLoop:     cfg.FlowPrompts.ReviewLoop,
			PRCreation:     cfg.FlowPrompts.PRCreation,
			Autoreview:     cfg.FlowPrompts.Autoreview,
			Merge:          cfg.FlowPrompts.Merge,
			Generic:        cfg.FlowPrompts.Generic,
		},
		BootstrapHookForRepo: bootstrapHookResolver(cfg),
		RunBootstrapHook:     actions.RunBootstrapHook,
		PublishCoords:        true,
		Stdout:               deps.stdout,
	}); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

const serveHelpText = `Usage: flowstate serve [--listen host:port]

Start the secure local HTTP server. The listen target must be localhost, a literal loopback IP, or tailscale:PORT.
tailscale:PORT resolves to an up Tailnet address before binding and fails when no Tailscale address is available.

Flags:
  --listen  Local or Tailnet listen target. Default: 127.0.0.1:0
  --help    Print this help and exit.
`

func runSessionHook(args []string, deps runDeps) error {
	flags := flag.NewFlagSet("session-hook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	providerFlag := flags.String("provider", "", "session provider")
	stateRoot := flags.String("state-root", "", "session state root")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	provider := sessions.Provider(*providerFlag)
	switch provider {
	case sessions.ProviderClaude, sessions.ProviderCodex:
	default:
		return fmt.Errorf("unsupported session provider %q", *providerFlag)
	}
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}
	root := *stateRoot
	if root == "" {
		root = deps.getenv("FLOWSTATE_SESSION_STATE_ROOT")
	}
	if root == "" {
		root = cfg.Sessions.Root
	}
	_, err = sessions.IngestHook(provider, deps.stdin, sessions.IngestOptions{
		StateRoot:          root,
		CopyRawTranscripts: cfg.Sessions.CopyRawTranscripts,
		Env: map[string]string{
			"FLOWSTATE_LAUNCH_ID":          deps.getenv("FLOWSTATE_LAUNCH_ID"),
			"FLOWSTATE_REPO_PATH":          deps.getenv("FLOWSTATE_REPO_PATH"),
			"FLOWSTATE_WORKTREE_PATH":      deps.getenv("FLOWSTATE_WORKTREE_PATH"),
			"FLOWSTATE_PLAN_ID":            deps.getenv("FLOWSTATE_PLAN_ID"),
			"FLOWSTATE_PLAN_PATH":          deps.getenv("FLOWSTATE_PLAN_PATH"),
			"FLOWSTATE_PLAN_STATE_ROOT":    deps.getenv("FLOWSTATE_PLAN_STATE_ROOT"),
			"FLOWSTATE_FLOW_ID":            deps.getenv("FLOWSTATE_FLOW_ID"),
			"FLOWSTATE_FLOW_PHASE_ID":      deps.getenv("FLOWSTATE_FLOW_PHASE_ID"),
			"FLOWSTATE_FLOW_STATE_ROOT":    deps.getenv("FLOWSTATE_FLOW_STATE_ROOT"),
			"FLOWSTATE_BRANCH":             deps.getenv("FLOWSTATE_BRANCH"),
			"FLOWSTATE_COMMIT":             deps.getenv("FLOWSTATE_COMMIT"),
			"FLOWSTATE_SESSION_STATE_ROOT": deps.getenv("FLOWSTATE_SESSION_STATE_ROOT"),
		},
	})
	return err
}

func startProgram(repos []scanner.Repo, opts startProgramOptions) error {
	cfg := opts.Config
	artifactRoot, err := runtimeArtifactRoot(cfg)
	if err != nil {
		return err
	}
	sessionStore, err := sessions.NewStore(sessions.StoreOptions{
		Root:               artifactRoot,
		CopyRawTranscripts: cfg.Sessions.CopyRawTranscripts,
	})
	if err != nil {
		return err
	}
	planStore, err := planstore.NewStore(planstore.StoreOptions{Root: sessionStore.Root()})
	if err != nil {
		return err
	}
	flowClient, err := daemonclient.New(daemonclient.Options{
		Coords: func() (daemoncoords.Coords, error) {
			return daemoncoords.ReadForStateRoot(artifactRoot)
		},
	})
	if err != nil {
		return err
	}
	modelOpts := modelOptionsFromConfig(cfg, opts.ScanRepos, sessionStore, planStore, flowClient)
	modelOpts.RepoCreateRoot = opts.RepoCreateRoot
	p := tea.NewProgram(model.NewWithOptions(repos, modelOpts), tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func modelOptionsFromConfig(cfg config.Config, scanRepos func() ([]scanner.Repo, error), sessionStore *sessions.Store, planStore *planstore.Store, flowClient daemonclient.FlowClient) model.Options {
	launchOpts := actions.LaunchOptions{TerminalCommand: cfg.Terminal.Command}
	startupMode := ui.ModeFlows
	if cfg.UI.DefaultView != nil {
		if mode, ok := model.ModeForViewNumber(*cfg.UI.DefaultView); ok {
			startupMode = mode
		}
	}
	return model.Options{
		AgentCommand:          cfg.Agent.Command,
		CodexReasoningEffort:  cfg.Agent.CodexReasoningEffort,
		ClaudeReasoningEffort: cfg.Agent.ClaudeReasoningEffort,
		StartupMode:           startupMode,
		PlanPromptTemplate:    cfg.Agent.PlanPrompt,
		FlowPromptTemplates: model.FlowPromptTemplates{
			Plan:           cfg.FlowPrompts.Plan,
			PlanReview:     cfg.FlowPrompts.PlanReview,
			Implementation: cfg.FlowPrompts.Implementation,
			ReviewLoop:     cfg.FlowPrompts.ReviewLoop,
			PRCreation:     cfg.FlowPrompts.PRCreation,
			Autoreview:     cfg.FlowPrompts.Autoreview,
			Merge:          cfg.FlowPrompts.Merge,
			Generic:        cfg.FlowPrompts.Generic,
		},
		ScanRepos:        scanRepos,
		SessionStateRoot: sessionStore.Root(),
		ListSessions:     sessionStore.List,
		ReadTranscript:   sessionStore.ReadTranscript,
		ListPlans:        planStore.List,
		ListFlows: func(filter flowstore.FlowFilter) ([]flowstore.FlowRecord, error) {
			return flowClient.ListFlows(context.Background(), filter)
		},
		ListFlowViews: func(filter flowstore.FlowFilter) ([]model.FlowView, error) {
			views, err := flowClient.ListFlowViews(context.Background(), filter)
			if err != nil {
				return nil, err
			}
			return modelFlowViewsFromDaemon(views), nil
		},
		CreateFlow: func(req model.FlowStartRequest) (model.FlowStartResult, error) {
			result, err := flowClient.StartFlow(context.Background(), daemonclient.StartFlowInput{
				RepoPath:     req.RepoPath,
				Title:        req.Title,
				Instructions: req.Instructions,
				BaseRef:      req.BaseRef,
				LaunchPlan:   false,
			})
			if err != nil {
				return model.FlowStartResult{}, err
			}
			return model.FlowStartResult{Flow: result.Flow}, nil
		},
		StartFlowPlan: func(req model.FlowStartRequest) (model.FlowStartResult, error) {
			if agent.Normalize(req.AgentCommand) == agent.CommandCodexApp {
				result, err := flowClient.StartFlow(context.Background(), daemonclient.StartFlowInput{
					RepoPath:     req.RepoPath,
					Title:        req.Title,
					Instructions: req.Instructions,
					BaseRef:      req.BaseRef,
					LaunchPlan:   false,
				})
				if err != nil {
					return model.FlowStartResult{}, err
				}
				phaseID := req.PlanPhaseID
				if phaseID == "" {
					phaseID = "plan"
				}
				phase, ok := flowlaunch.PhaseByID(result.Flow, phaseID)
				if !ok {
					return model.FlowStartResult{}, fmt.Errorf("phase %q not found in flow %q", phaseID, result.Flow.FlowID)
				}
				if !flowlaunch.PhaseCanLaunch(result.Flow, phase) {
					return model.FlowStartResult{Flow: result.Flow, DaemonLaunched: true}, nil
				}
				launchID := uuid.NewString()
				return model.FlowStartResult{
					Flow:     result.Flow,
					LaunchID: launchID,
					LaunchContext: actions.AgentLaunchContext{
						Command:          agent.CommandCodexApp,
						LaunchID:         launchID,
						RepoPath:         result.Flow.RepoPath,
						WorktreePath:     result.Flow.WorktreePath,
						Branch:           result.Flow.Branch,
						Commit:           result.Flow.Commit,
						SessionStateRoot: sessionStore.Root(),
						PlanPhaseID:      phaseID,
						PlanPhaseTitle:   req.PlanPhaseTitle,
						PlanPhaseStatus:  req.PlanPhaseStatus,
						FlowID:           result.Flow.FlowID,
						FlowPhaseID:      phase.PhaseID,
						InitialPrompt:    flowlaunch.PhasePrompt(result.Flow, phase, result.Flow.PlanPath, "", flowLaunchPromptTemplatesFromConfig(cfg)),
					},
				}, nil
			}
			result, err := flowClient.StartFlow(context.Background(), daemonclient.StartFlowInput{
				RepoPath:        req.RepoPath,
				Title:           req.Title,
				Instructions:    req.Instructions,
				BaseRef:         req.BaseRef,
				LaunchPlan:      true,
				Headless:        true,
				AgentCommand:    req.AgentCommand,
				ReasoningEffort: req.ReasoningEffort,
			})
			if err != nil {
				return model.FlowStartResult{}, err
			}
			if result.LaunchError != "" {
				return model.FlowStartResult{Flow: result.Flow, LaunchID: result.LaunchID, DaemonLaunched: true}, fmt.Errorf("daemon launch failed: %s", result.LaunchError)
			}
			return model.FlowStartResult{
				Flow:           result.Flow,
				LaunchID:       result.LaunchID,
				DaemonLaunched: true,
			}, nil
		},
		LaunchFlowPhase: func(req model.DaemonFlowPhaseLaunchRequest) (model.DaemonFlowPhaseLaunchResult, error) {
			result, err := flowClient.LaunchFlowPhase(context.Background(), daemonclient.LaunchFlowPhaseInput{
				FlowID:          req.FlowID,
				PhaseID:         req.PhaseID,
				AgentCommand:    req.AgentCommand,
				ReasoningEffort: req.ReasoningEffort,
				Headless:        true,
				AutoLaunch:      req.AutoLaunch,
			})
			if err != nil {
				return model.DaemonFlowPhaseLaunchResult{}, err
			}
			return model.DaemonFlowPhaseLaunchResult{
				FlowID:   result.FlowID,
				PhaseID:  result.PhaseID,
				LaunchID: result.LaunchID,
				Skipped:  result.Skipped,
			}, nil
		},
		CancelRuntimeJob: func(jobID string) (model.FlowRuntimeJob, error) {
			job, err := flowClient.CancelRuntimeJob(context.Background(), jobID)
			if err != nil {
				return model.FlowRuntimeJob{}, err
			}
			return modelFlowRuntimeJobFromDaemon(job), nil
		},
		SetFlowPhase: func(update flowstore.PhaseUpdate) (flowstore.FlowRecord, error) {
			record, _, err := flowClient.SetPhase(context.Background(), update)
			return record, err
		},
		ResetFlowPhase: func(update flowstore.PhaseResetUpdate) (flowstore.FlowRecord, error) {
			record, _, err := flowClient.ResetFlowPhase(context.Background(), update)
			return record, err
		},
		SetFlowAutoMode: func(update flowstore.AutoModeUpdate) (flowstore.FlowRecord, error) {
			return flowClient.SetFlowAutoMode(context.Background(), update)
		},
		DeleteFlow: func(flowID string) error {
			_, err := flowClient.DeleteFlow(context.Background(), flowID)
			return err
		},
		ReadPlan: planStore.ReadPlan,
		LaunchTerminal: func(path string) (actions.TerminalLaunchSpec, error) {
			return actions.TerminalLaunchWithOptions(path, launchOpts)
		},
		LaunchDetachedTerminal: func(targetShellCommand, cwd string) (actions.TerminalLaunchSpec, error) {
			return actions.DetachedTerminalLaunch(targetShellCommand, cwd, launchOpts)
		},
		EditFile: func(path string) (actions.TerminalLaunchSpec, error) {
			return actions.EditFileWithOptions(path, actions.EditorOptions{EditorCommand: cfg.Editor.Command})
		},
		LaunchAgent: func(ctx actions.AgentLaunchContext) (actions.TerminalLaunchSpec, error) {
			return actions.AgentLaunchWithOptions(ctx, launchOpts)
		},
		FinalizeAgentSession: func(ctx actions.AgentLaunchContext) error {
			return sessionStore.MarkLaunchEnded(ctx.LaunchID, time.Now().UTC())
		},
		BootstrapHookForRepo: bootstrapHookResolver(cfg),
		RunBootstrapHook:     actions.RunBootstrapHook,
		SaveAgentCommand: func(command string) error {
			return config.SaveAgentCommand(command)
		},
		SaveAgentReasoningEffort: func(command, effort string) error {
			return config.SaveAgentReasoningEffort(command, effort)
		},
		SaveDefaultView: func(mode ui.Mode) error {
			number, ok := model.ViewNumber(mode)
			if !ok {
				return fmt.Errorf("unsupported default view %d", mode)
			}
			return config.SaveDefaultView(number)
		},
		SavePromptTemplate: func(section, key, value string) error {
			return config.SavePromptTemplate(section, key, value)
		},
		ResetPromptTemplate: func(section, key string) error {
			return config.ResetPromptTemplate(section, key)
		},
	}
}

func flowLaunchPromptTemplatesFromConfig(cfg config.Config) flowlaunch.PromptTemplates {
	return flowlaunch.PromptTemplates{
		Plan:           cfg.FlowPrompts.Plan,
		PlanReview:     cfg.FlowPrompts.PlanReview,
		Implementation: cfg.FlowPrompts.Implementation,
		ReviewLoop:     cfg.FlowPrompts.ReviewLoop,
		PRCreation:     cfg.FlowPrompts.PRCreation,
		Autoreview:     cfg.FlowPrompts.Autoreview,
		Merge:          cfg.FlowPrompts.Merge,
		Generic:        cfg.FlowPrompts.Generic,
	}
}

func modelFlowViewsFromDaemon(views []daemonclient.FlowView) []model.FlowView {
	out := make([]model.FlowView, 0, len(views))
	for _, view := range views {
		runtimeJobs := make(map[string]model.FlowRuntimeJob, len(view.RuntimeJobs))
		for phaseID, job := range view.RuntimeJobs {
			runtimeJobs[phaseID] = modelFlowRuntimeJobFromDaemon(job)
		}
		out = append(out, model.FlowView{Record: view.Record, RuntimeJobs: runtimeJobs})
	}
	return out
}

func modelFlowRuntimeJobFromDaemon(job daemonclient.RuntimeJob) model.FlowRuntimeJob {
	return model.FlowRuntimeJob{
		ID:               job.ID,
		LaunchID:         job.LaunchID,
		FlowID:           job.FlowID,
		PhaseID:          job.PhaseID,
		Status:           job.Status,
		CreatedAt:        job.CreatedAt,
		StartedAt:        job.StartedAt,
		EndedAt:          job.EndedAt,
		ExitCode:         job.ExitCode,
		Error:            job.Error,
		PhaseUpdateError: job.PhaseUpdateError,
		LogTail:          job.LogTail,
		LogTruncated:     job.LogTruncated,
	}
}

func runtimeArtifactRoot(cfg config.Config) (string, error) {
	return runtimeArtifactRootWithEnv(cfg, os.Getenv)
}

func runtimeArtifactRootWithEnv(cfg config.Config, getenv func(string) string) (string, error) {
	if envRoot := getenv("FLOWSTATE_FLOW_STATE_ROOT"); envRoot != "" {
		return envRoot, nil
	}
	if envRoot := getenv("FLOWSTATE_PLAN_STATE_ROOT"); envRoot != "" {
		return envRoot, nil
	}
	if envRoot := getenv("FLOWSTATE_SESSION_STATE_ROOT"); envRoot != "" {
		return envRoot, nil
	}
	if cfg.Sessions.Root != "" {
		return cfg.Sessions.Root, nil
	}
	return flowstore.DefaultRoot()
}

func bootstrapHookResolver(cfg config.Config) func(string) (actions.BootstrapHook, bool) {
	hooks := make(map[string]actions.BootstrapHook, len(cfg.Bootstrap.Hooks))
	for _, hook := range cfg.Bootstrap.Hooks {
		timeout := hook.TimeoutSeconds
		if timeout == 0 {
			timeout = cfg.Bootstrap.TimeoutSeconds
		}
		if timeout == 0 {
			timeout = 120
		}
		hooks[filepath.Clean(hook.RepoPath)] = actions.BootstrapHook{
			Script:         hook.Script,
			TimeoutSeconds: timeout,
		}
	}
	return func(repoPath string) (actions.BootstrapHook, bool) {
		hook, ok := hooks[filepath.Clean(repoPath)]
		return hook, ok
	}
}
