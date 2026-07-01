package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/brian-bell/flowstate/actions"
	"github.com/brian-bell/flowstate/config"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/internal/daemonclient"
	"github.com/brian-bell/flowstate/internal/version"
	"github.com/brian-bell/flowstate/model"
	"github.com/brian-bell/flowstate/planstore"
	"github.com/brian-bell/flowstate/scanner"
	"github.com/brian-bell/flowstate/sessions"
	"github.com/brian-bell/flowstate/ui"
)

func TestRun_VersionBypassesConfigAndScan(t *testing.T) {
	var stdout bytes.Buffer
	err := run([]string{"wtui", "--version"}, runDeps{
		loadConfig: func() (config.Config, error) {
			t.Fatal("loadConfig should not run for --version")
			return config.Config{}, nil
		},
		scan: func(scanner.ScanOptions) ([]scanner.Repo, error) {
			t.Fatal("scan should not run for --version")
			return nil, nil
		},
		startProgram: func([]scanner.Repo, config.Config) error {
			t.Fatal("program should not start for --version")
			return nil
		},
		stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if strings.TrimSpace(stdout.String()) != version.String() {
		t.Fatalf("expected version output %q, got %q", version.String(), stdout.String())
	}
}

func TestRun_HelpBypassesConfigAndScan(t *testing.T) {
	var stdout bytes.Buffer
	err := run([]string{"wtui", "--help"}, runDeps{
		loadConfig: func() (config.Config, error) {
			t.Fatal("loadConfig should not run for --help")
			return config.Config{}, nil
		},
		scan: func(scanner.ScanOptions) ([]scanner.Repo, error) {
			t.Fatal("scan should not run for --help")
			return nil, nil
		},
		startProgram: func([]scanner.Repo, config.Config) error {
			t.Fatal("program should not start for --help")
			return nil
		},
		stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	requireContainsAll(t, stdout.String(), []string{
		"Usage: flowstate [--version] [command]",
		"flowstate plan --help",
		"flowstate flow --help",
		"flowstate serve --listen 127.0.0.1:0",
		"flowstate session-hook --provider codex",
	})
}

func TestRun_UnknownCommandSuggestsNearbyCommand(t *testing.T) {
	err := run([]string{"wtui", "plna"}, runDeps{
		loadConfig: func() (config.Config, error) {
			t.Fatal("loadConfig should not run for unknown command")
			return config.Config{}, nil
		},
		scan: func(scanner.ScanOptions) ([]scanner.Repo, error) {
			t.Fatal("scan should not run for unknown command")
			return nil, nil
		},
		startProgram: func([]scanner.Repo, config.Config) error {
			t.Fatal("program should not start for unknown command")
			return nil
		},
		stdout: &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("expected unknown command error")
	}
	requireContainsAll(t, err.Error(), []string{
		`unknown command "plna"; did you mean "plan"?`,
		"Usage: flowstate [--version] [command]",
	})
}

func TestRun_UnknownCommandSuggestsServe(t *testing.T) {
	err := run([]string{"wtui", "serbe"}, runDeps{
		loadConfig: func() (config.Config, error) {
			t.Fatal("loadConfig should not run for unknown command")
			return config.Config{}, nil
		},
		scan: func(scanner.ScanOptions) ([]scanner.Repo, error) {
			t.Fatal("scan should not run for unknown command")
			return nil, nil
		},
		serve: func(context.Context, serveOptions) error {
			t.Fatal("serve should not run for unknown command")
			return nil
		},
		stdout: &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("expected unknown command error")
	}
	requireContainsAll(t, err.Error(), []string{
		`unknown command "serbe"; did you mean "serve"?`,
		"Usage: flowstate [--version] [command]",
	})
}

func TestRun_UnknownCommandFarFromValidShowsUsageWithoutSuggestion(t *testing.T) {
	err := run([]string{"wtui", "definitely-not-close"}, runDeps{
		loadConfig: func() (config.Config, error) {
			t.Fatal("loadConfig should not run for unknown command")
			return config.Config{}, nil
		},
		scan: func(scanner.ScanOptions) ([]scanner.Repo, error) {
			t.Fatal("scan should not run for unknown command")
			return nil, nil
		},
		startProgram: func([]scanner.Repo, config.Config) error {
			t.Fatal("program should not start for unknown command")
			return nil
		},
		stdout: &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("expected unknown command error")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("far command should not suggest a command:\n%s", err)
	}
	requireContainsAll(t, err.Error(), []string{
		`unknown command "definitely-not-close"`,
		"Usage: flowstate [--version] [command]",
	})
}

func TestRunServeLoadsConfigForStateRootButBypassesScanAndTUI(t *testing.T) {
	called := false
	var got serveOptions
	configRoot := filepath.Join(t.TempDir(), "from-config")
	err := run([]string{"wtui", "serve"}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{
				Sessions: config.SessionsConfig{Root: configRoot},
				Agent: config.AgentConfig{
					Command:               "codex",
					CodexReasoningEffort:  "high",
					ClaudeReasoningEffort: "max",
				},
				FlowPrompts: config.FlowPromptConfig{
					Implementation: "custom implementation {flow_id}",
				},
			}, nil
		},
		scan: func(scanner.ScanOptions) ([]scanner.Repo, error) {
			t.Fatal("scan should not run for serve")
			return nil, nil
		},
		startProgram: func([]scanner.Repo, config.Config) error {
			t.Fatal("program should not start for serve")
			return nil
		},
		serve: func(_ context.Context, opts serveOptions) error {
			called = true
			got = opts
			return nil
		},
		stdout: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !called {
		t.Fatal("serve dependency was not called")
	}
	if got.Listen != "127.0.0.1:0" {
		t.Fatalf("serve listen = %q, want local default", got.Listen)
	}
	if got.StateRoot != configRoot {
		t.Fatalf("serve state root = %q, want config root %q", got.StateRoot, configRoot)
	}
	if got.AgentCommand != "codex" || got.CodexReasoningEffort != "high" || got.ClaudeReasoningEffort != "max" {
		t.Fatalf("serve agent defaults = command %q codex %q claude %q", got.AgentCommand, got.CodexReasoningEffort, got.ClaudeReasoningEffort)
	}
	if got.FlowPromptTemplates.Implementation != "custom implementation {flow_id}" {
		t.Fatalf("serve implementation prompt = %q, want configured template", got.FlowPromptTemplates.Implementation)
	}
}

func TestRunServePublishesDaemonCoords(t *testing.T) {
	var got serveOptions
	err := run([]string{"wtui", "serve"}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{
				Sessions: config.SessionsConfig{Root: filepath.Join(t.TempDir(), "state")},
			}, nil
		},
		scan: func(scanner.ScanOptions) ([]scanner.Repo, error) {
			t.Fatal("scan should not run for serve")
			return nil, nil
		},
		startProgram: func([]scanner.Repo, config.Config) error {
			t.Fatal("program should not start for serve")
			return nil
		},
		serve: func(_ context.Context, opts serveOptions) error {
			got = opts
			return nil
		},
		stdout: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !got.PublishCoords {
		t.Fatal("serve options PublishCoords = false, want true so the daemon publishes discovery data")
	}
}

func TestRunServePassesBootstrapHookConfig(t *testing.T) {
	var got serveOptions
	repoPath := filepath.Join(t.TempDir(), "repo")
	err := run([]string{"wtui", "serve"}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{
				Sessions: config.SessionsConfig{Root: filepath.Join(t.TempDir(), "state")},
				Bootstrap: config.BootstrapConfig{
					TimeoutSeconds: 90,
					Hooks: []config.BootstrapHookConfig{{
						RepoPath: repoPath,
						Script:   ".flowstate/bootstrap",
					}},
				},
			}, nil
		},
		scan: func(scanner.ScanOptions) ([]scanner.Repo, error) {
			t.Fatal("scan should not run for serve")
			return nil, nil
		},
		startProgram: func([]scanner.Repo, config.Config) error {
			t.Fatal("program should not start for serve")
			return nil
		},
		serve: func(_ context.Context, opts serveOptions) error {
			got = opts
			return nil
		},
		stdout: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got.BootstrapHookForRepo == nil {
		t.Fatal("serve options missing bootstrap hook resolver")
	}
	hook, ok := got.BootstrapHookForRepo(repoPath)
	if !ok {
		t.Fatal("serve bootstrap resolver did not match configured repo")
	}
	if hook.Script != ".flowstate/bootstrap" || hook.TimeoutSeconds != 90 {
		t.Fatalf("hook = %#v", hook)
	}
	if got.RunBootstrapHook == nil {
		t.Fatal("serve options missing bootstrap runner")
	}
}

func TestRunServeStateRootUsesFlowPlanSessionConfigPrecedence(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "from-config")
	sessionRoot := filepath.Join(t.TempDir(), "from-session")
	planRoot := filepath.Join(t.TempDir(), "from-plan")
	flowRoot := filepath.Join(t.TempDir(), "from-flow")
	for _, tt := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "flow", env: map[string]string{"FLOWSTATE_FLOW_STATE_ROOT": flowRoot, "FLOWSTATE_PLAN_STATE_ROOT": planRoot, "FLOWSTATE_SESSION_STATE_ROOT": sessionRoot}, want: flowRoot},
		{name: "plan", env: map[string]string{"FLOWSTATE_PLAN_STATE_ROOT": planRoot, "FLOWSTATE_SESSION_STATE_ROOT": sessionRoot}, want: planRoot},
		{name: "session", env: map[string]string{"FLOWSTATE_SESSION_STATE_ROOT": sessionRoot}, want: sessionRoot},
		{name: "config", env: map[string]string{}, want: configRoot},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got serveOptions
			err := run([]string{"wtui", "serve"}, runDeps{
				loadConfig: func() (config.Config, error) {
					return config.Config{Sessions: config.SessionsConfig{Root: configRoot}}, nil
				},
				getenv: func(key string) string {
					return tt.env[key]
				},
				serve: func(_ context.Context, opts serveOptions) error {
					got = opts
					return nil
				},
				stdout: &bytes.Buffer{},
			})
			if err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			if got.StateRoot != tt.want {
				t.Fatalf("serve state root = %q, want %q", got.StateRoot, tt.want)
			}
		})
	}
}

func TestRunServeAcceptsOnlyExplicitLoopbackAndTailscaleListenTargets(t *testing.T) {
	for _, listen := range []string{"localhost:8080", "127.0.0.1:0", "[::1]:8080", "tailscale:8080"} {
		t.Run("accepts "+listen, func(t *testing.T) {
			called := false
			err := run([]string{"wtui", "serve", "--listen", listen}, runDeps{
				loadConfig: func() (config.Config, error) {
					return config.Config{Sessions: config.SessionsConfig{Root: t.TempDir()}}, nil
				},
				serve: func(_ context.Context, opts serveOptions) error {
					called = true
					if opts.Listen != listen {
						t.Fatalf("listen = %q, want %q", opts.Listen, listen)
					}
					return nil
				},
				stdout: &bytes.Buffer{},
			})
			if err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			if !called {
				t.Fatal("serve dependency was not called")
			}
		})
	}

	rejected := []string{
		"",
		"0.0.0.0:8080",
		":8080",
		"[::]:8080",
		"192.168.1.20:8080",
		"example.com:8080",
		"localhost.:8080",
		"[::ffff:127.0.0.1]:8080",
		"127.1:8080",
		"[fe80::1%lo0]:8080",
	}
	for _, listen := range rejected {
		t.Run("rejects "+listen, func(t *testing.T) {
			err := run([]string{"wtui", "serve", "--listen", listen}, runDeps{
				serve: func(context.Context, serveOptions) error {
					t.Fatal("serve dependency should not run for invalid listen address")
					return nil
				},
				stdout: &bytes.Buffer{},
			})
			if err == nil {
				t.Fatal("expected listen validation error")
			}
			if !strings.Contains(err.Error(), "listen address must be host:port with host localhost, a loopback IP, or tailscale:PORT") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestRunServeHelpBypassesServer(t *testing.T) {
	var stdout bytes.Buffer
	err := run([]string{"wtui", "serve", "--help"}, runDeps{
		loadConfig: func() (config.Config, error) {
			t.Fatal("loadConfig should not run for serve --help")
			return config.Config{}, nil
		},
		serve: func(context.Context, serveOptions) error {
			t.Fatal("serve dependency should not run for serve --help")
			return nil
		},
		stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	requireContainsAll(t, stdout.String(), []string{
		"Usage: flowstate serve [--listen host:port]",
		"--listen",
		"tailscale:PORT",
		"127.0.0.1:0",
	})
}

func TestRunServeHelpAfterFlagsBypassesServer(t *testing.T) {
	var stdout bytes.Buffer
	err := run([]string{"wtui", "serve", "--listen", "127.0.0.1:0", "--help"}, runDeps{
		loadConfig: func() (config.Config, error) {
			t.Fatal("loadConfig should not run for serve --help")
			return config.Config{}, nil
		},
		serve: func(context.Context, serveOptions) error {
			t.Fatal("serve dependency should not run for serve --help")
			return nil
		},
		stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	requireContainsAll(t, stdout.String(), []string{
		"Usage: flowstate serve [--listen host:port]",
		"--listen",
		"tailscale:PORT",
		"127.0.0.1:0",
	})
}

func TestNearestCommandSuggestsOnlyNearbyCommands(t *testing.T) {
	valid := []string{"plan", "flow", "session-hook"}
	if got := nearestCommand("flw", valid); got != "flow" {
		t.Fatalf("nearestCommand(flw) = %q, want flow", got)
	}
	if got := nearestCommand("definitely-not-close", valid); got != "" {
		t.Fatalf("nearestCommand(definitely-not-close) = %q, want no suggestion", got)
	}
}

func TestRun_LoadsConfigBeforeScanning(t *testing.T) {
	var got scanner.ScanOptions
	err := run([]string{"wtui"}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{
				Scan: config.ScanConfig{
					Root:     "/from/config",
					MaxDepth: 1,
				},
			}, nil
		},
		getenv: func(string) string { return "" },
		scan: func(opts scanner.ScanOptions) ([]scanner.Repo, error) {
			got = opts
			return []scanner.Repo{{Path: "/repo", DisplayName: "repo"}}, nil
		},
		startProgram: func([]scanner.Repo, config.Config) error { return nil },
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if got.Root != "/from/config" {
		t.Fatalf("expected config scan root, got %q", got.Root)
	}
	if got.MaxDepth != 1 {
		t.Fatalf("expected config max depth 1, got %d", got.MaxDepth)
	}
}

func TestRun_WorktreeRootEnvOverridesConfigRoot(t *testing.T) {
	var got scanner.ScanOptions
	err := run([]string{"wtui"}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{
				Scan: config.ScanConfig{Root: "/from/config", MaxDepth: 1},
			}, nil
		},
		getenv: func(key string) string {
			if key == "WORKTREE_ROOT" {
				return "/from/env"
			}
			return ""
		},
		scan: func(opts scanner.ScanOptions) ([]scanner.Repo, error) {
			got = opts
			return nil, nil
		},
		startProgram: func([]scanner.Repo, config.Config) error { return nil },
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if got.Root != "/from/env" {
		t.Fatalf("expected WORKTREE_ROOT to override config root, got %q", got.Root)
	}
	if got.MaxDepth != 1 {
		t.Fatalf("expected config max depth to remain 1, got %d", got.MaxDepth)
	}
}

func TestRun_PassesRefreshScannerWithResolvedScanOptions(t *testing.T) {
	var startupScan scanner.ScanOptions
	var refreshScan scanner.ScanOptions
	var repoCreateRoot string
	scans := 0
	err := run([]string{"wtui"}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{
				Scan: config.ScanConfig{Root: "/from/config", MaxDepth: 1},
			}, nil
		},
		getenv: func(key string) string {
			if key == "WORKTREE_ROOT" {
				return "/from/env"
			}
			return ""
		},
		scan: func(opts scanner.ScanOptions) ([]scanner.Repo, error) {
			scans++
			if scans == 1 {
				startupScan = opts
			} else {
				refreshScan = opts
			}
			return []scanner.Repo{{Path: "/repo", DisplayName: "repo"}}, nil
		},
		startProgramWithOptions: func(_ []scanner.Repo, opts startProgramOptions) error {
			if opts.ScanRepos == nil {
				t.Fatal("expected refresh scanner")
			}
			repoCreateRoot = opts.RepoCreateRoot
			_, err := opts.ScanRepos()
			return err
		},
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if scans != 2 {
		t.Fatalf("scan count = %d, want startup + refresh", scans)
	}
	if startupScan.Root != "/from/env" || refreshScan.Root != "/from/env" {
		t.Fatalf("scan roots startup=%q refresh=%q, want WORKTREE_ROOT", startupScan.Root, refreshScan.Root)
	}
	if repoCreateRoot != "/from/env" {
		t.Fatalf("repo create root = %q, want WORKTREE_ROOT", repoCreateRoot)
	}
	if startupScan.MaxDepth != 1 || refreshScan.MaxDepth != 1 {
		t.Fatalf("scan max depth startup=%d refresh=%d, want 1", startupScan.MaxDepth, refreshScan.MaxDepth)
	}
}

func TestRun_ResolvesRelativeScanRootForScanAndRepoCreation(t *testing.T) {
	cwd := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	var startupScan scanner.ScanOptions
	var refreshScan scanner.ScanOptions
	var repoCreateRoot string
	scans := 0
	err = run([]string{"wtui"}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{Scan: config.ScanConfig{Root: "repos", MaxDepth: 2}}, nil
		},
		getenv: func(string) string { return "" },
		scan: func(opts scanner.ScanOptions) ([]scanner.Repo, error) {
			scans++
			if scans == 1 {
				startupScan = opts
			} else {
				refreshScan = opts
			}
			return nil, nil
		},
		startProgramWithOptions: func(_ []scanner.Repo, opts startProgramOptions) error {
			repoCreateRoot = opts.RepoCreateRoot
			if opts.ScanRepos == nil {
				t.Fatal("expected refresh scanner")
			}
			_, err := opts.ScanRepos()
			return err
		},
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	wantRoot, err := filepath.Abs("repos")
	if err != nil {
		t.Fatal(err)
	}
	if startupScan.Root != "repos" || refreshScan.Root != "repos" {
		t.Fatalf("scan roots startup=%q refresh=%q, want configured relative root", startupScan.Root, refreshScan.Root)
	}
	if repoCreateRoot != wantRoot {
		t.Fatalf("repo create root = %q, want %q", repoCreateRoot, wantRoot)
	}
}

func TestRun_ConfigErrorStopsBeforeScan(t *testing.T) {
	scanned := false
	err := run([]string{"wtui"}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{}, errors.New("bad config")
		},
		getenv: func(string) string { return "" },
		scan: func(scanner.ScanOptions) ([]scanner.Repo, error) {
			scanned = true
			return nil, nil
		},
		startProgram: func([]scanner.Repo, config.Config) error { return nil },
	})
	if err == nil {
		t.Fatal("expected config error")
	}
	if scanned {
		t.Fatal("scan should not run when config fails")
	}
}

func TestRun_PassesConfigToProgram(t *testing.T) {
	var got config.Config
	err := run([]string{"wtui"}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{Agent: config.AgentConfig{
				Command:    "codex",
				PlanPrompt: "Implement {title}",
			}}, nil
		},
		getenv: func(string) string { return "" },
		scan: func(scanner.ScanOptions) ([]scanner.Repo, error) {
			return []scanner.Repo{{Path: "/repo", DisplayName: "repo"}}, nil
		},
		startProgram: func(_ []scanner.Repo, cfg config.Config) error {
			got = cfg
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got.Agent.Command != "codex" {
		t.Fatalf("expected agent config passed to program, got %q", got.Agent.Command)
	}
	if got.Agent.PlanPrompt != "Implement {title}" {
		t.Fatalf("expected agent plan prompt passed to program, got %q", got.Agent.PlanPrompt)
	}
}

func TestRuntimeArtifactRootPrecedenceIncludesFlowRoot(t *testing.T) {
	cfg := config.Config{Sessions: config.SessionsConfig{Root: "/from/config"}}
	t.Setenv("FLOWSTATE_SESSION_STATE_ROOT", "/from/session")
	t.Setenv("FLOWSTATE_PLAN_STATE_ROOT", "/from/plan")
	t.Setenv("FLOWSTATE_FLOW_STATE_ROOT", "/from/flow")

	got, err := runtimeArtifactRoot(cfg)
	if err != nil {
		t.Fatalf("runtimeArtifactRoot returned error: %v", err)
	}
	if got != "/from/flow" {
		t.Fatalf("artifact root = %q, want flow root", got)
	}
}

func TestRuntimeArtifactRootFallsBackThroughPlanSessionConfig(t *testing.T) {
	cfg := config.Config{Sessions: config.SessionsConfig{Root: "/from/config"}}
	t.Setenv("FLOWSTATE_FLOW_STATE_ROOT", "")
	t.Setenv("FLOWSTATE_SESSION_STATE_ROOT", "/from/session")
	t.Setenv("FLOWSTATE_PLAN_STATE_ROOT", "/from/plan")
	got, err := runtimeArtifactRoot(cfg)
	if err != nil {
		t.Fatalf("runtimeArtifactRoot returned error: %v", err)
	}
	if got != "/from/plan" {
		t.Fatalf("artifact root = %q, want plan root", got)
	}

	t.Setenv("FLOWSTATE_PLAN_STATE_ROOT", "")
	got, err = runtimeArtifactRoot(cfg)
	if err != nil {
		t.Fatalf("runtimeArtifactRoot returned error: %v", err)
	}
	if got != "/from/session" {
		t.Fatalf("artifact root = %q, want session root", got)
	}

	t.Setenv("FLOWSTATE_SESSION_STATE_ROOT", "")
	got, err = runtimeArtifactRoot(cfg)
	if err != nil {
		t.Fatalf("runtimeArtifactRoot returned error: %v", err)
	}
	if got != "/from/config" {
		t.Fatalf("artifact root = %q, want config root", got)
	}
}

func TestRuntimeArtifactRootDefaultsWhenConfigRootEmpty(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("FLOWSTATE_FLOW_STATE_ROOT", "")
	t.Setenv("FLOWSTATE_PLAN_STATE_ROOT", "")
	t.Setenv("FLOWSTATE_SESSION_STATE_ROOT", "")

	got, err := runtimeArtifactRoot(config.Config{})
	if err != nil {
		t.Fatalf("runtimeArtifactRoot returned error: %v", err)
	}
	want := filepath.Join(stateHome, "flowstate", "sessions", "v1")
	if got != want {
		t.Fatalf("artifact root = %q, want default %q", got, want)
	}
}

func TestFlowClientForTUIToleratesMissingDaemonCoords(t *testing.T) {
	t.Setenv("FLOWSTATE_DAEMON_URL", "")
	t.Setenv("FLOWSTATE_DAEMON_TOKEN", "")
	client := flowClientForTUI(t.TempDir())

	_, err := client.ListFlows(context.Background(), flowstore.FlowFilter{})
	if err == nil || !strings.Contains(err.Error(), "flow daemon client is not available") {
		t.Fatalf("ListFlows error = %v, want daemon unavailable error", err)
	}
}

func TestModelOptionsFromConfigPassesReasoningEffort(t *testing.T) {
	sessionStore, planStore, flowStore := testArtifactStores(t)

	opts := modelOptionsFromConfig(config.Config{
		Agent: config.AgentConfig{
			Command:               "codex",
			CodexReasoningEffort:  "high",
			ClaudeReasoningEffort: "max",
		},
	}, nil, sessionStore, planStore, testFlowClient{store: flowStore})

	if opts.CodexReasoningEffort != "high" || opts.ClaudeReasoningEffort != "max" {
		t.Fatalf("reasoning efforts = codex %q claude %q, want high/max", opts.CodexReasoningEffort, opts.ClaudeReasoningEffort)
	}
	if opts.SaveAgentReasoningEffort == nil {
		t.Fatal("SaveAgentReasoningEffort should be wired")
	}
}

func TestModelOptionsFromConfigMapsDefaultView(t *testing.T) {
	for _, tt := range []struct {
		name string
		view *int
		want ui.Mode
	}{
		{name: "missing uses flows", want: ui.ModeFlows},
		{name: "view 1", view: intPtr(1), want: ui.ModeWorktrees},
		{name: "view 8", view: intPtr(8), want: ui.ModeFlows},
		{name: "view 9", view: intPtr(9), want: ui.ModeActiveFlows},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sessionStore, planStore, flowStore := testArtifactStores(t)
			opts := modelOptionsFromConfig(config.Config{
				UI: config.UIConfig{DefaultView: tt.view},
			}, nil, sessionStore, planStore, testFlowClient{store: flowStore})

			if opts.StartupMode != tt.want {
				t.Fatalf("StartupMode = %v, want %v", opts.StartupMode, tt.want)
			}
			if opts.SaveDefaultView == nil {
				t.Fatal("SaveDefaultView should be wired")
			}
		})
	}
}

func TestModelOptionsFromConfigPassesTerminalCommandToLaunchers(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("ZELLIJ", "")
	t.Setenv("TERMINAL", "")
	root := t.TempDir()
	sessionStore, err := sessions.NewStore(sessions.StoreOptions{Root: root})
	if err != nil {
		t.Fatalf("NewStore sessions: %v", err)
	}
	planStore, err := planstore.NewStore(planstore.StoreOptions{Root: sessionStore.Root()})
	if err != nil {
		t.Fatalf("NewStore plans: %v", err)
	}
	flowStore, err := flowstore.NewStore(flowstore.StoreOptions{Root: sessionStore.Root()})
	if err != nil {
		t.Fatalf("NewStore flows: %v", err)
	}
	terminalCommand := putCommandOnPath(t, "wtui-test-terminal")
	putCommandOnPath(t, "codex")

	opts := modelOptionsFromConfig(config.Config{
		Agent:    config.AgentConfig{Command: "codex"},
		Terminal: config.TerminalConfig{Command: terminalCommand + " --reuse"},
	}, nil, sessionStore, planStore, testFlowClient{store: flowStore})

	terminalLaunch, err := opts.LaunchTerminal("/repo/worktree")
	if err != nil {
		t.Fatalf("LaunchTerminal returned error: %v", err)
	}
	if !reflect.DeepEqual(terminalLaunch.Cmd.Args, []string{terminalCommand, "--reuse"}) {
		t.Fatalf("expected LaunchTerminal to use configured terminal command, got %#v", terminalLaunch.Cmd.Args)
	}
	if terminalLaunch.Cmd.Dir != "/repo/worktree" {
		t.Fatalf("LaunchTerminal dir = %q, want /repo/worktree", terminalLaunch.Cmd.Dir)
	}

	agentLaunch, err := opts.LaunchAgent(actions.AgentLaunchContext{Command: "codex", WorktreePath: "/repo/worktree"})
	if err != nil {
		t.Fatalf("LaunchAgent returned error: %v", err)
	}
	if len(agentLaunch.Cmd.Args) < 5 || !reflect.DeepEqual(agentLaunch.Cmd.Args[:5], []string{terminalCommand, "--reuse", "-e", "sh", "-c"}) {
		t.Fatalf("expected LaunchAgent to use configured terminal command with -e, got %#v", agentLaunch.Cmd.Args)
	}
	if agentLaunch.Cleanup != nil {
		agentLaunch.Cleanup()
	}

	detachLaunch, err := opts.LaunchDetachedTerminal("tmux attach-session -t agent", "/repo/worktree")
	if err != nil {
		t.Fatalf("LaunchDetachedTerminal returned error: %v", err)
	}
	wantDetachArgs := []string{terminalCommand, "--reuse", "-e", "sh", "-c", "tmux attach-session -t agent"}
	if !reflect.DeepEqual(detachLaunch.Cmd.Args, wantDetachArgs) {
		t.Fatalf("expected LaunchDetachedTerminal to use configured terminal command, got %#v", detachLaunch.Cmd.Args)
	}
	if detachLaunch.Cmd.Dir != "/repo/worktree" {
		t.Fatalf("LaunchDetachedTerminal dir = %q, want /repo/worktree", detachLaunch.Cmd.Dir)
	}
}

func testArtifactStores(t *testing.T) (*sessions.Store, *planstore.Store, *flowstore.Store) {
	t.Helper()
	root := t.TempDir()
	sessionStore, err := sessions.NewStore(sessions.StoreOptions{Root: root})
	if err != nil {
		t.Fatalf("NewStore sessions: %v", err)
	}
	planStore, err := planstore.NewStore(planstore.StoreOptions{Root: sessionStore.Root()})
	if err != nil {
		t.Fatalf("NewStore plans: %v", err)
	}
	flowStore, err := flowstore.NewStore(flowstore.StoreOptions{Root: sessionStore.Root()})
	if err != nil {
		t.Fatalf("NewStore flows: %v", err)
	}
	return sessionStore, planStore, flowStore
}

func intPtr(value int) *int {
	return &value
}

func TestModelOptionsFromConfigTerminalEnvOverridesConfiguredCommand(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("ZELLIJ", "")
	envTerminal := putCommandOnPath(t, "wtui-env-terminal")
	configTerminal := putCommandOnPath(t, "wtui-config-terminal")
	t.Setenv("TERMINAL", envTerminal)
	root := t.TempDir()
	sessionStore, err := sessions.NewStore(sessions.StoreOptions{Root: root})
	if err != nil {
		t.Fatalf("NewStore sessions: %v", err)
	}
	planStore, err := planstore.NewStore(planstore.StoreOptions{Root: sessionStore.Root()})
	if err != nil {
		t.Fatalf("NewStore plans: %v", err)
	}
	flowStore, err := flowstore.NewStore(flowstore.StoreOptions{Root: sessionStore.Root()})
	if err != nil {
		t.Fatalf("NewStore flows: %v", err)
	}

	opts := modelOptionsFromConfig(config.Config{
		Terminal: config.TerminalConfig{Command: configTerminal},
	}, nil, sessionStore, planStore, testFlowClient{store: flowStore})

	launch, err := opts.LaunchTerminal("/repo/worktree")
	if err != nil {
		t.Fatalf("LaunchTerminal returned error: %v", err)
	}
	if !reflect.DeepEqual(launch.Cmd.Args, []string{envTerminal}) {
		t.Fatalf("expected TERMINAL to override configured command, got %#v", launch.Cmd.Args)
	}
}

func TestModelOptionsFromConfigPassesEditorCommandToEditFile(t *testing.T) {
	t.Setenv("EDITOR", "vim")
	root := t.TempDir()
	sessionStore, err := sessions.NewStore(sessions.StoreOptions{Root: root})
	if err != nil {
		t.Fatalf("NewStore sessions: %v", err)
	}
	planStore, err := planstore.NewStore(planstore.StoreOptions{Root: sessionStore.Root()})
	if err != nil {
		t.Fatalf("NewStore plans: %v", err)
	}
	flowStore, err := flowstore.NewStore(flowstore.StoreOptions{Root: sessionStore.Root()})
	if err != nil {
		t.Fatalf("NewStore flows: %v", err)
	}
	editorCommand := putCommandOnPath(t, "wtui-test-editor")

	opts := modelOptionsFromConfig(config.Config{
		Editor: config.EditorConfig{Command: editorCommand + " --wait"},
	}, nil, sessionStore, planStore, testFlowClient{store: flowStore})

	launch, err := opts.EditFile("/state/plans/plan-1/plan.md")
	if err != nil {
		t.Fatalf("EditFile returned error: %v", err)
	}
	if !launch.Interactive {
		t.Fatal("EditFile launch should be interactive")
	}
	want := []string{editorCommand, "--wait", "/state/plans/plan-1/plan.md"}
	if !reflect.DeepEqual(launch.Cmd.Args, want) {
		t.Fatalf("editor args = %#v, want %#v", launch.Cmd.Args, want)
	}
}

func TestModelOptionsFromConfigPassesFlowPromptTemplates(t *testing.T) {
	root := t.TempDir()
	sessionStore, err := sessions.NewStore(sessions.StoreOptions{Root: root})
	if err != nil {
		t.Fatalf("NewStore sessions: %v", err)
	}
	planStore, err := planstore.NewStore(planstore.StoreOptions{Root: sessionStore.Root()})
	if err != nil {
		t.Fatalf("NewStore plans: %v", err)
	}
	flowStore, err := flowstore.NewStore(flowstore.StoreOptions{Root: sessionStore.Root()})
	if err != nil {
		t.Fatalf("NewStore flows: %v", err)
	}

	opts := modelOptionsFromConfig(config.Config{
		FlowPrompts: config.FlowPromptConfig{
			Plan:           "plan",
			PlanReview:     "plan review",
			Implementation: "implementation",
			ReviewLoop:     "review loop",
			PRCreation:     "pr creation",
			Autoreview:     "autoreview",
			Merge:          "merge",
			Generic:        "generic",
		},
	}, nil, sessionStore, planStore, testFlowClient{store: flowStore})

	want := model.FlowPromptTemplates{
		Plan:           "plan",
		PlanReview:     "plan review",
		Implementation: "implementation",
		ReviewLoop:     "review loop",
		PRCreation:     "pr creation",
		Autoreview:     "autoreview",
		Merge:          "merge",
		Generic:        "generic",
	}
	if opts.FlowPromptTemplates != want {
		t.Fatalf("flow prompt templates = %#v, want %#v", opts.FlowPromptTemplates, want)
	}
}

func putCommandOnPath(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake command: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("fake command not on PATH: %v", err)
	}
	return path
}

func TestRunSessionHookWritesSessionMetadata(t *testing.T) {
	root := t.TempDir()
	transcriptPath := filepath.Join(root, "claude.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"timestamp":"2026-06-06T14:01:00Z","role":"user","kind":"message","text":"Fix scanner tests"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	stdin := bytes.NewBufferString(`{
		"session_id": "claude-session-1",
		"cwd": "/repo/worktree",
		"transcript_path": ` + quoteJSON(transcriptPath) + `,
		"summary": "Fix scanner tests",
		"ended_at": "2026-06-06T14:45:00Z"
	}`)

	err := run([]string{"wtui", "session-hook", "--provider", "claude", "--state-root", root}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{Sessions: config.SessionsConfig{CopyRawTranscripts: true}}, nil
		},
		scan: func(scanner.ScanOptions) ([]scanner.Repo, error) {
			t.Fatal("scan should not run for session-hook")
			return nil, nil
		},
		stdin: stdin,
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	metaPath := singleSessionFile(t, root, "claude", "meta.json")
	meta, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	for _, want := range []string{`"provider": "claude"`, `"session_id": "claude-session-1"`, `"status": "ended"`, `"summary": "Fix scanner tests"`} {
		if !strings.Contains(string(meta), want) {
			t.Fatalf("metadata missing %s:\n%s", want, meta)
		}
	}
}

func TestRunSessionHookPersistsPlanEnvironment(t *testing.T) {
	root := t.TempDir()
	planPath := filepath.Join(root, "plans", "plan-1", "plan.md")

	err := run([]string{"wtui", "session-hook", "--provider", "codex", "--state-root", root}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{}, nil
		},
		getenv: func(key string) string {
			switch key {
			case "FLOWSTATE_PLAN_ID":
				return "plan-1"
			case "FLOWSTATE_PLAN_PATH":
				return planPath
			case "FLOWSTATE_FLOW_ID":
				return "flow-1"
			case "FLOWSTATE_FLOW_PHASE_ID":
				return "plan"
			case "FLOWSTATE_FLOW_STATE_ROOT":
				return root
			default:
				return ""
			}
		},
		stdin: strings.NewReader(`{"session_id":"codex-plan-1","cwd":"/repo/worktree"}`),
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	meta, err := os.ReadFile(singleSessionFile(t, root, "codex", "meta.json"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	for _, want := range []string{`"plan_id": "plan-1"`, `"plan_path": ` + quoteJSON(planPath), `"flow_id": "flow-1"`, `"flow_phase_id": "plan"`} {
		if !strings.Contains(string(meta), want) {
			t.Fatalf("metadata missing %s:\n%s", want, meta)
		}
	}
}

func TestRunSessionHookAttachesFlowFromPlanStateRoot(t *testing.T) {
	planRoot := t.TempDir()
	sessionRoot := t.TempDir()
	repoPath := filepath.Join(planRoot, "repo")
	flowStore, err := flowstore.NewStore(flowstore.StoreOptions{Root: planRoot})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	flow, err := flowStore.Create(flowstore.FlowRecord{
		FlowID:       "flow-1",
		Title:        "Plan Root Flow",
		Instructions: "attach the session",
		RepoPath:     repoPath,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	err = run([]string{"wtui", "session-hook", "--provider", "codex", "--state-root", sessionRoot}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{}, nil
		},
		getenv: func(key string) string {
			switch key {
			case "FLOWSTATE_PLAN_STATE_ROOT":
				return planRoot
			case "FLOWSTATE_FLOW_ID":
				return flow.FlowID
			case "FLOWSTATE_FLOW_PHASE_ID":
				return "plan"
			default:
				return ""
			}
		},
		stdin: strings.NewReader(`{"session_id":"codex-flow-plan-root","cwd":"/repo/worktree"}`),
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	read, err := flowStore.Read(flow.FlowID)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(read.Phases) == 0 || len(read.Phases[0].Sessions) != 1 {
		t.Fatalf("attached sessions = %#v, want one on first phase", read.Phases)
	}
	if got := read.Phases[0].Sessions[0].SessionID; got != "codex-flow-plan-root" {
		t.Fatalf("attached session ID = %q, want codex-flow-plan-root", got)
	}
}

func TestRunSessionHookRejectsMalformedJSON(t *testing.T) {
	err := run([]string{"wtui", "session-hook", "--provider", "codex", "--state-root", t.TempDir()}, runDeps{
		loadConfig: func() (config.Config, error) { return config.Config{}, nil },
		stdin:      strings.NewReader(`{"session_id":`),
	})
	if err == nil {
		t.Fatal("expected malformed JSON error")
	}
	if !strings.Contains(err.Error(), "parse hook payload") {
		t.Fatalf("expected parse hook payload error, got %q", err)
	}
}

func TestRunSessionHookRejectsUnsupportedProvider(t *testing.T) {
	err := run([]string{"wtui", "session-hook", "--provider", "other", "--state-root", t.TempDir()}, runDeps{
		stdin: strings.NewReader(`{}`),
	})
	if err == nil {
		t.Fatal("expected unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported session provider") {
		t.Fatalf("expected unsupported provider error, got %q", err)
	}
}

func TestRunSessionHookHonorsCopyRawTranscriptConfig(t *testing.T) {
	root := t.TempDir()
	transcriptPath := filepath.Join(root, "codex.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"role":"user","kind":"message","text":"secret"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	err := run([]string{"wtui", "session-hook", "--provider", "codex", "--state-root", root}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{Sessions: config.SessionsConfig{CopyRawTranscripts: false}}, nil
		},
		stdin: strings.NewReader(`{
			"session_id": "codex-session-1",
			"cwd": "/repo/worktree",
			"transcript_path": ` + quoteJSON(transcriptPath) + `
		}`),
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(root, "sessions", "codex", "*", "raw.jsonl")); err != nil || len(matches) != 0 {
		t.Fatalf("expected no copied raw transcript, matches=%#v err=%v", matches, err)
	}
}

func TestRunSessionHookEnvStateRootOverridesConfig(t *testing.T) {
	configRoot := t.TempDir()
	envRoot := t.TempDir()
	err := run([]string{"wtui", "session-hook", "--provider", "codex"}, runDeps{
		loadConfig: func() (config.Config, error) {
			return config.Config{Sessions: config.SessionsConfig{Root: configRoot}}, nil
		},
		getenv: func(key string) string {
			if key == "FLOWSTATE_SESSION_STATE_ROOT" {
				return envRoot
			}
			return ""
		},
		stdin: strings.NewReader(`{"session_id":"codex-env-root","cwd":"/repo/worktree"}`),
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(envRoot, "sessions", "codex", "*", "meta.json")); err != nil || len(matches) != 1 {
		t.Fatalf("expected metadata under env root, matches=%#v err=%v", matches, err)
	}
	if matches, err := filepath.Glob(filepath.Join(configRoot, "sessions", "codex", "*", "meta.json")); err != nil || len(matches) != 0 {
		t.Fatalf("expected no metadata under config root, matches=%#v err=%v", matches, err)
	}
}

func TestModelOptionsStartFlowPlanCreatesOnlyForCodexApp(t *testing.T) {
	root := t.TempDir()
	sessionStore, err := sessions.NewStore(sessions.StoreOptions{Root: root})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	planStore, err := planstore.NewStore(planstore.StoreOptions{Root: root})
	if err != nil {
		t.Fatalf("NewPlanStore: %v", err)
	}
	client := &capturingStartFlowClient{
		result: daemonclient.StartFlowResult{Flow: flowstore.FlowRecord{
			FlowID:       "flow-1",
			Title:        "Codex App Flow",
			Instructions: "Write the plan",
			RepoPath:     "/dev/alpha",
			WorktreePath: "/dev/alpha-worktrees/flow-codex-app",
			Branch:       "flow/codex-app",
			Commit:       "abc123",
			Status:       flowstore.StatusInProgress,
			Phases: []flowstore.FlowPhase{
				{PhaseID: "plan", Title: "Plan", Status: flowstore.PhaseReady, Order: 1},
			},
		}},
	}
	opts := modelOptionsFromConfig(config.Config{Agent: config.AgentConfig{Command: "codex-app"}}, nil, sessionStore, planStore, client)

	result, err := opts.StartFlowPlan(model.FlowStartRequest{
		RepoPath:         "/dev/alpha",
		Title:            "Codex App Flow",
		Instructions:     "Write the plan",
		BaseRef:          "main",
		AgentCommand:     "codex-app",
		SessionStateRoot: root,
		PlanPhaseID:      "plan",
		PlanPhaseTitle:   "Plan",
		PlanPhaseStatus:  flowstore.PhaseRunning,
	})
	if err != nil {
		t.Fatalf("StartFlowPlan: %v", err)
	}
	if !client.called || client.input.LaunchPlan {
		t.Fatalf("StartFlow input = %#v, want create-only daemon call for codex-app", client.input)
	}
	if result.DaemonLaunched {
		t.Fatal("codex-app result should launch externally, not report daemon runtime launch")
	}
	ctx := result.LaunchContext
	if ctx.Command != "codex-app" ||
		ctx.FlowID != "flow-1" ||
		ctx.FlowPhaseID != "plan" ||
		ctx.WorktreePath != "/dev/alpha-worktrees/flow-codex-app" ||
		ctx.LaunchID == "" ||
		ctx.FlowLaunchTracked {
		t.Fatalf("launch context = %#v, want external codex-app flow plan launch", ctx)
	}
	if !strings.Contains(ctx.InitialPrompt, "Write the plan") {
		t.Fatalf("initial prompt = %q, want flow instructions", ctx.InitialPrompt)
	}
}

func TestModelOptionsStartFlowPlanRejectsInteractiveDaemonRuntime(t *testing.T) {
	sessionStore, planStore, _ := testArtifactStores(t)
	client := &capturingStartFlowClient{}
	opts := modelOptionsFromConfig(config.Config{Agent: config.AgentConfig{Command: "codex"}}, nil, sessionStore, planStore, client)

	_, err := opts.StartFlowPlan(model.FlowStartRequest{
		RepoPath:     "/dev/alpha",
		Title:        "Interactive Flow",
		Instructions: "Write the plan",
		AgentCommand: "codex",
		Headless:     false,
		PlanPhaseID:  "plan",
	})
	if err == nil || !strings.Contains(err.Error(), "interactive daemon Flow launches are not supported") {
		t.Fatalf("StartFlowPlan error = %v, want interactive unsupported error", err)
	}
	if client.called {
		t.Fatalf("StartFlow should not be called for interactive daemon runtime: %#v", client.input)
	}
}

func TestModelOptionsStartFlowPlanReturnsDaemonLaunchError(t *testing.T) {
	sessionStore, planStore, _ := testArtifactStores(t)
	client := &capturingStartFlowClient{
		result: daemonclient.StartFlowResult{
			Flow:        flowstore.FlowRecord{FlowID: "flow-1", RepoPath: "/dev/alpha"},
			LaunchID:    "launch-1",
			LaunchError: "runtime unavailable",
		},
	}
	opts := modelOptionsFromConfig(config.Config{Agent: config.AgentConfig{Command: "codex"}}, nil, sessionStore, planStore, client)

	result, err := opts.StartFlowPlan(model.FlowStartRequest{
		RepoPath:     "/dev/alpha",
		Title:        "Launch Error Flow",
		Instructions: "Write the plan",
		AgentCommand: "codex",
		Headless:     true,
		PlanPhaseID:  "plan",
	})
	if err == nil || !strings.Contains(err.Error(), "runtime unavailable") {
		t.Fatalf("StartFlowPlan error = %v, want daemon launch error", err)
	}
	if result.Flow.FlowID != "flow-1" || result.LaunchID != "launch-1" || !result.DaemonLaunched {
		t.Fatalf("result = %#v, want flow metadata preserved with daemon handled flag", result)
	}
}

func TestModelOptionsStartFlowPlanReturnsBlockedPlanError(t *testing.T) {
	sessionStore, planStore, _ := testArtifactStores(t)
	client := &capturingStartFlowClient{
		result: daemonclient.StartFlowResult{Flow: flowstore.FlowRecord{
			FlowID:   "flow-1",
			RepoPath: "/dev/alpha",
			Phases: []flowstore.FlowPhase{{
				PhaseID: "plan",
				Status:  flowstore.PhaseBlocked,
				Notes:   "Bootstrap hook failed: missing env file",
			}},
		}},
	}
	opts := modelOptionsFromConfig(config.Config{Agent: config.AgentConfig{Command: "codex"}}, nil, sessionStore, planStore, client)

	result, err := opts.StartFlowPlan(model.FlowStartRequest{
		RepoPath:     "/dev/alpha",
		Title:        "Blocked Flow",
		Instructions: "Write the plan",
		AgentCommand: "codex",
		Headless:     true,
		PlanPhaseID:  "plan",
	})
	if err == nil || !strings.Contains(err.Error(), "Bootstrap hook failed") {
		t.Fatalf("StartFlowPlan error = %v, want blocked plan detail", err)
	}
	if result.Flow.FlowID != "flow-1" || !result.DaemonLaunched {
		t.Fatalf("result = %#v, want recoverable blocked flow metadata", result)
	}
}

func TestModelOptionsCreateFlowReturnsBlockedPlanError(t *testing.T) {
	sessionStore, planStore, _ := testArtifactStores(t)
	client := &capturingStartFlowClient{
		result: daemonclient.StartFlowResult{Flow: flowstore.FlowRecord{
			FlowID:   "flow-1",
			RepoPath: "/dev/alpha",
			Phases: []flowstore.FlowPhase{{
				PhaseID: "plan",
				Status:  flowstore.PhaseBlocked,
				Summary: "Worktree creation failed.",
			}},
		}},
	}
	opts := modelOptionsFromConfig(config.Config{Agent: config.AgentConfig{Command: "codex"}}, nil, sessionStore, planStore, client)

	result, err := opts.CreateFlow(model.FlowStartRequest{
		RepoPath:     "/dev/alpha",
		Title:        "Blocked Flow",
		Instructions: "Park it",
	})
	if err == nil || !strings.Contains(err.Error(), "Worktree creation failed") {
		t.Fatalf("CreateFlow error = %v, want blocked plan detail", err)
	}
	if result.Flow.FlowID != "flow-1" || !result.DaemonLaunched {
		t.Fatalf("result = %#v, want recoverable blocked flow metadata", result)
	}
}

func TestModelOptionsLaunchFlowPhaseRejectsInteractiveDaemonRuntime(t *testing.T) {
	sessionStore, planStore, _ := testArtifactStores(t)
	client := &capturingStartFlowClient{
		launchResult: daemonclient.LaunchFlowPhaseResult{
			FlowID:   "flow-1",
			PhaseID:  "implementation",
			LaunchID: "launch-1",
		},
	}
	opts := modelOptionsFromConfig(config.Config{Agent: config.AgentConfig{Command: "codex"}}, nil, sessionStore, planStore, client)

	_, err := opts.LaunchFlowPhase(model.DaemonFlowPhaseLaunchRequest{
		FlowID:     "flow-1",
		PhaseID:    "implementation",
		Headless:   false,
		AutoLaunch: true,
	})
	if err == nil || !strings.Contains(err.Error(), "interactive daemon Flow launches are not supported") {
		t.Fatalf("LaunchFlowPhase error = %v, want interactive unsupported error", err)
	}
	if client.launchInput.FlowID != "" {
		t.Fatalf("LaunchFlowPhase should not be called for interactive daemon runtime: %#v", client.launchInput)
	}
}

func TestModelOptionsAddFlowPhaseLaunchIDUsesDaemonClient(t *testing.T) {
	sessionStore, planStore, _ := testArtifactStores(t)
	client := &capturingStartFlowClient{
		addLaunchRecord: flowstore.FlowRecord{
			FlowID: "flow-1",
			Phases: []flowstore.FlowPhase{{
				PhaseID:   "implementation",
				Status:    flowstore.PhaseCompleted,
				LaunchIDs: []string{"resume-1"},
			}},
		},
	}
	opts := modelOptionsFromConfig(config.Config{Agent: config.AgentConfig{Command: "codex"}}, nil, sessionStore, planStore, client)

	record, err := opts.AddFlowPhaseLaunchID(flowstore.PhaseLaunchUpdate{
		FlowID:   "flow-1",
		PhaseID:  "implementation",
		LaunchID: "resume-1",
		Resume:   true,
	})
	if err != nil {
		t.Fatalf("AddFlowPhaseLaunchID: %v", err)
	}
	if record.FlowID != "flow-1" || client.addLaunchInput.FlowID != "flow-1" ||
		client.addLaunchInput.PhaseID != "implementation" ||
		client.addLaunchInput.LaunchID != "resume-1" ||
		!client.addLaunchInput.Resume {
		t.Fatalf("record = %#v input = %#v, want daemon-backed resume launch update", record, client.addLaunchInput)
	}
}

type capturingStartFlowClient struct {
	daemonclient.FlowClient
	called          bool
	input           daemonclient.StartFlowInput
	result          daemonclient.StartFlowResult
	launchInput     daemonclient.LaunchFlowPhaseInput
	launchResult    daemonclient.LaunchFlowPhaseResult
	addLaunchInput  flowstore.PhaseLaunchUpdate
	addLaunchRecord flowstore.FlowRecord
	addLaunchPhase  flowstore.FlowPhase
}

func (c *capturingStartFlowClient) StartFlow(_ context.Context, input daemonclient.StartFlowInput) (daemonclient.StartFlowResult, error) {
	c.called = true
	c.input = input
	return c.result, nil
}

func (c *capturingStartFlowClient) LaunchFlowPhase(_ context.Context, input daemonclient.LaunchFlowPhaseInput) (daemonclient.LaunchFlowPhaseResult, error) {
	c.launchInput = input
	return c.launchResult, nil
}

func (c *capturingStartFlowClient) AddFlowPhaseLaunchID(_ context.Context, input flowstore.PhaseLaunchUpdate) (flowstore.FlowRecord, flowstore.FlowPhase, error) {
	c.addLaunchInput = input
	return c.addLaunchRecord, c.addLaunchPhase, nil
}

func singleSessionFile(t *testing.T, root, provider, name string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "sessions", provider, "*", name))
	if err != nil {
		t.Fatalf("glob session file: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("glob returned %d matches, want 1: %#v", len(matches), matches)
	}
	return matches[0]
}

func quoteJSON(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func requireContainsAll(t *testing.T, text string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestBootstrapHookResolverMatchesConfiguredRepoPath(t *testing.T) {
	cfg := config.Config{
		Bootstrap: config.BootstrapConfig{
			TimeoutSeconds: 120,
			Hooks: []config.BootstrapHookConfig{
				{RepoPath: filepath.Clean("/dev/wtui/"), Script: ".wtui/bootstrap"},
				{RepoPath: "/dev/client-api", Script: "/bin/bootstrap-client-api", TimeoutSeconds: 300},
			},
		},
	}
	resolve := bootstrapHookResolver(cfg)

	hook, ok := resolve("/dev/wtui")
	if !ok {
		t.Fatal("expected hook for configured repo")
	}
	if hook != (actions.BootstrapHook{Script: ".wtui/bootstrap", TimeoutSeconds: 120}) {
		t.Fatalf("unexpected hook: %#v", hook)
	}

	hook, ok = resolve("/dev/client-api")
	if !ok {
		t.Fatal("expected hook for second configured repo")
	}
	if hook.TimeoutSeconds != 300 {
		t.Fatalf("expected per-hook timeout override 300, got %d", hook.TimeoutSeconds)
	}
}

func TestBootstrapHookResolverDoesNotMatchDifferentRepoPath(t *testing.T) {
	resolve := bootstrapHookResolver(config.Config{
		Bootstrap: config.BootstrapConfig{
			TimeoutSeconds: 120,
			Hooks: []config.BootstrapHookConfig{
				{RepoPath: "/dev/wtui", Script: ".wtui/bootstrap"},
			},
		},
	})

	if _, ok := resolve("/dev/wtui-other"); ok {
		t.Fatal("expected non-matching repo to have no hook")
	}
}
