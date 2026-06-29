package agentskills

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestFlowstateSkillDocumentsAgentContract(t *testing.T) {
	root := repoRoot(t)
	skill := readFile(t, filepath.Join(root, "agent-skills", "flowstate", "SKILL.md"))

	requireContainsAll(t, "skill metadata", skill, []string{
		"name: flowstate",
		"FLOWSTATE_FLOW_ID",
		"FLOWSTATE_FLOW_PHASE_ID",
	})
	requireContainsAll(t, "flow commands", skill, []string{
		"flowstate flow read --flow-id",
		"flowstate flow phase complete",
		"flowstate flow phase block",
		"flowstate flow phase needs-attention",
		"flowstate flow phase restart",
		"flowstate flow phase set",
		"flowstate flow plan set",
		"flowstate flow pr set",
		"flowstate plan save",
		"flowstate plan phase set",
		"flowstate plan read",
	})
	requireContainsAll(t, "default phase playbooks", skill, []string{
		"plan",
		"plan-review",
		"implementation",
		"review-loop",
		"pr-creation",
		"autoreview",
		"merge",
	})
	requireContainsAll(t, "agent-facing phase statuses", skill, []string{
		"running",
		"needs_attention",
		"completed",
		"blocked",
		"skipped",
		"ready",
		"cannot set",
	})
	requireContainsAll(t, "flow outcomes and failure handling", skill, []string{
		"--outcome",
		"--summary",
		"--notes",
		"persistence failures",
		"must not be treated as successful phase progression",
	})
}

func TestFlowstateSkillMatchesImplementedFlowCLIContract(t *testing.T) {
	root := repoRoot(t)
	skill := readFile(t, filepath.Join(root, "agent-skills", "flowstate", "SKILL.md"))
	flowCLI := readFile(t, filepath.Join(root, "cmd", "flowstate", "flow.go"))
	planCLI := readFile(t, filepath.Join(root, "cmd", "flowstate", "plan.go"))
	flowStore := readFile(t, filepath.Join(root, "flowstore", "store.go"))

	if !strings.Contains(skill, "flowstate flow phase set") || !strings.Contains(flowCLI, "runFlowPhaseSet") {
		t.Fatal("skill and CLI should both expose flow phase set")
	}
	if !strings.Contains(skill, "flowstate flow phase complete") || !strings.Contains(flowCLI, `command:        "complete"`) {
		t.Fatal("skill and CLI should both expose flow phase complete")
	}
	if !strings.Contains(skill, "flowstate flow phase block") || !strings.Contains(flowCLI, `command:        "block"`) {
		t.Fatal("skill and CLI should both expose flow phase block")
	}
	if !strings.Contains(skill, "flowstate flow phase needs-attention") || !strings.Contains(flowCLI, `command:        "needs-attention"`) {
		t.Fatal("skill and CLI should both expose flow phase needs-attention")
	}
	if !strings.Contains(skill, "flowstate flow phase restart") || !strings.Contains(flowCLI, "runFlowPhaseRestart") {
		t.Fatal("skill and CLI should both expose flow phase restart")
	}
	if !strings.Contains(skill, "flowstate flow plan set") || !strings.Contains(flowCLI, "runFlowPlanSet") {
		t.Fatal("skill and CLI should both expose flow plan set")
	}
	if !strings.Contains(skill, "flowstate flow pr set") || !strings.Contains(flowCLI, "runFlowPRSet") {
		t.Fatal("skill and CLI should both expose flow pr set")
	}
	if !strings.Contains(skill, "flowstate flow merge set") || !strings.Contains(flowCLI, "runFlowMergeSet") {
		t.Fatal("skill and CLI should both expose flow merge set")
	}
	for _, flagName := range []string{
		"flow-id",
		"phase-id",
		"plan-id",
		"provider",
		"number",
		"url",
		"head",
		"base",
		"status",
		"commit",
		"merged-at",
		"outcome",
		"summary",
		"notes",
		"state-root",
	} {
		hasStringFlag := strings.Contains(flowCLI, `flags.String("`+flagName+`"`)
		hasIntFlag := strings.Contains(flowCLI, `flags.Int("`+flagName+`"`)
		if strings.Contains(skill, "--"+flagName) && !hasStringFlag && !hasIntFlag {
			t.Fatalf("skill documents --%s but flow CLI does not expose it", flagName)
		}
	}
	assertRunnableExampleFlagsExist(t, skill, flowCLI, "flow")
	assertRunnableExampleFlagsExist(t, skill, planCLI, "plan")

	for _, constant := range []string{
		"PhaseRunning",
		"PhaseNeedsAttention",
		"PhaseCompleted",
		"PhaseBlocked",
		"PhaseSkipped",
		"PhaseReady",
		"StatusPending",
		"StatusInProgress",
		"StatusNeedsAttention",
		"StatusBlocked",
		"StatusCompleted",
		"StatusMerged",
		"StatusAbandoned",
		"MergePending",
		"MergeMerged",
		"MergeBlocked",
	} {
		if !strings.Contains(flowStore, constant) {
			t.Fatalf("flowstore contract missing %s", constant)
		}
	}

	for _, unimplementedCommand := range []string{
		"flowstate flow session attach",
		"flowstate flow abandon",
	} {
		if hasRunnableCommandExample(skill, unimplementedCommand) {
			t.Fatalf("skill includes a runnable example for unimplemented command %q", unimplementedCommand)
		}
	}
}

func TestFlowstateSkillKeepsPlanAndFlowStateRootsTogether(t *testing.T) {
	root := repoRoot(t)
	skill := readFile(t, filepath.Join(root, "agent-skills", "flowstate", "SKILL.md"))

	requireContainsAll(t, "shared artifact root setup", skill, []string{
		"FLOWSTATE_ARTIFACT_ROOT",
		"FLOWSTATE_FLOW_STATE_ROOT",
		"FLOWSTATE_PLAN_STATE_ROOT",
		"FLOWSTATE_SESSION_STATE_ROOT",
		"FLOW_STATE_ARGS",
		"PLAN_STATE_ARGS",
	})

	for _, block := range fencedBashBlocks(skill) {
		if strings.Contains(block, "flowstate flow ") && !strings.Contains(block, `"${FLOW_STATE_ARGS[@]}"`) {
			t.Fatalf("flow example missing FLOW_STATE_ARGS:\n%s", block)
		}
		if strings.Contains(block, "flowstate plan ") && !strings.Contains(block, `"${PLAN_STATE_ARGS[@]}"`) {
			t.Fatalf("plan example missing PLAN_STATE_ARGS:\n%s", block)
		}
	}
}

func TestFlowstateSkillPlanPhaseGuardsPersistenceFailures(t *testing.T) {
	root := repoRoot(t)
	skill := readFile(t, filepath.Join(root, "agent-skills", "flowstate", "SKILL.md"))

	requireContainsAll(t, "plan persistence guards", skill, []string{
		"if ! PLAN_ID=$(",
		"flowstate flow plan set",
		`--plan-id "$PLAN_ID"`,
		`--outcome "plan_link_failed"`,
		`--outcome "plan_save_failed"`,
		`--outcome "plan_phase_save_failed"`,
		`--outcome "plan_read_failed"`,
		"exit 1",
	})
}

func TestFlowstateSkillHandlesMissingPlanID(t *testing.T) {
	root := repoRoot(t)
	skill := readFile(t, filepath.Join(root, "agent-skills", "flowstate", "SKILL.md"))

	requireContainsAll(t, "missing plan id guidance", skill, []string{
		`if [ -z "$FLOWSTATE_PLAN_ID" ]`,
		`if ! flowstate plan read --plan-id "$FLOWSTATE_PLAN_ID" "${PLAN_STATE_ARGS[@]}"`,
		`--status blocked`,
		`--outcome "blocked"`,
		`flowstate plan read --plan-id "$FLOWSTATE_PLAN_ID" "${PLAN_STATE_ARGS[@]}"`,
	})
}

func TestFlowstateSkillDocumentsPlanReviewGateOutcomes(t *testing.T) {
	root := repoRoot(t)
	skill := readFile(t, filepath.Join(root, "agent-skills", "flowstate", "SKILL.md"))

	requireContainsAll(t, "plan review outcome contract", skill, []string{
		"approved",
		"approved_with_concerns",
		"changes_requested",
		"blocked",
		"flowstate derives all phase readiness",
		`flowstate flow phase needs-attention --notes "..."`,
		`flowstate flow phase complete --outcome "approved_with_concerns" --notes "..."`,
		`flowstate flow phase block --notes "..."`,
	})
}

func TestFlowstateInstallationDocs(t *testing.T) {
	root := repoRoot(t)
	readme := readFile(t, filepath.Join(root, "README.md"))
	configDocs := readFile(t, filepath.Join(root, "docs", "config.md"))

	requireContainsAll(t, "README installation docs", readme, []string{
		"agent-skills/flowstate/",
		"symlink",
	})
	requireContainsAll(t, "config installation docs", configDocs, []string{
		"agent-skills/flowstate/",
		"symlink",
	})
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func requireContainsAll(t *testing.T, label, haystack string, needles []string) {
	t.Helper()
	normalized := strings.Join(strings.Fields(haystack), " ")
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) && !strings.Contains(normalized, needle) {
			t.Fatalf("%s missing %q", label, needle)
		}
	}
}

func hasRunnableCommandExample(markdown, command string) bool {
	for _, block := range fencedBashBlocks(markdown) {
		for _, line := range strings.Split(block, "\n") {
			if strings.Contains(line, command) {
				return true
			}
		}
	}
	return false
}

func assertRunnableExampleFlagsExist(t *testing.T, markdown, cliSource, command string) {
	t.Helper()
	for _, use := range runnableCommandFlagUses(markdown, command) {
		source, ok := commandFlagSource(cliSource, append([]string{command}, use.Subcommands...))
		if !ok {
			t.Fatalf("runnable %s example has no CLI contract mapping", use.Command())
		}
		if !cliHasFlag(source, use.FlagName) {
			t.Fatalf("runnable %s example documents --%s but that CLI command does not expose it", use.Command(), use.FlagName)
		}
	}
}

type runnableCommandFlagUse struct {
	TopCommand  string
	Subcommands []string
	FlagName    string
}

func (u runnableCommandFlagUse) Command() string {
	return "flowstate " + strings.Join(append([]string{u.TopCommand}, u.Subcommands...), " ")
}

func runnableCommandFlagUses(markdown, command string) []runnableCommandFlagUse {
	var uses []runnableCommandFlagUse
	seen := map[string]bool{}
	flagPattern := regexp.MustCompile(`--([A-Za-z0-9][A-Za-z0-9-]*)`)

	for _, block := range fencedBashBlocks(markdown) {
		var activeSubcommands []string
		continues := false
		for _, line := range strings.Split(block, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				if !continues {
					activeSubcommands = nil
				}
				continue
			}

			if subcommands, ok := runnableFlowstateSubcommands(trimmed, command); ok {
				activeSubcommands = subcommands
			} else if !continues {
				activeSubcommands = nil
			}

			if len(activeSubcommands) > 0 {
				unquoted := stripShellQuotedSpans(trimmed)
				for _, match := range flagPattern.FindAllStringSubmatch(unquoted, -1) {
					flagName := match[1]
					key := strings.Join(activeSubcommands, " ") + "\x00" + flagName
					if !seen[key] {
						uses = append(uses, runnableCommandFlagUse{
							TopCommand:  command,
							Subcommands: append([]string(nil), activeSubcommands...),
							FlagName:    flagName,
						})
						seen[key] = true
					}
				}
			}

			continues = strings.HasSuffix(trimmed, `\`)
		}
	}

	return uses
}

func stripShellQuotedSpans(line string) string {
	var b strings.Builder
	var quote rune
	escaped := false
	for _, r := range line {
		switch {
		case escaped:
			b.WriteRune(' ')
			escaped = false
		case quote != '\'' && r == '\\':
			b.WriteRune(' ')
			escaped = true
		case quote != 0:
			b.WriteRune(' ')
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			b.WriteRune(' ')
			quote = r
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func runnableFlowstateSubcommands(line, command string) ([]string, bool) {
	pattern := "flowstate " + command + " "
	index := strings.Index(line, pattern)
	if index < 0 {
		return nil, false
	}

	if index > 0 {
		prefix := line[:index]
		startsRunnableCommand := false
		for _, marker := range []string{"| ", "$(", "! "} {
			if strings.HasSuffix(prefix, marker) {
				startsRunnableCommand = true
				break
			}
		}
		if !startsRunnableCommand {
			return nil, false
		}
	}

	fields := strings.Fields(line[index:])
	if len(fields) < 3 || fields[0] != "flowstate" || fields[1] != command {
		return nil, false
	}
	var subcommands []string
	for _, field := range fields[2:] {
		field = strings.Trim(field, `"'();`)
		if field == "" || field == `\` || strings.HasPrefix(field, "-") || strings.HasPrefix(field, "$") || strings.ContainsAny(field, "[]{}") {
			break
		}
		subcommands = append(subcommands, field)
	}
	if len(subcommands) == 0 {
		return nil, false
	}
	return subcommands, true
}

func commandFlagSource(cliSource string, commandParts []string) (string, bool) {
	functionName, ok := commandFunctionName(commandParts)
	if !ok {
		return "", false
	}
	return functionSource(cliSource, functionName)
}

func commandFunctionName(commandParts []string) (string, bool) {
	key := strings.Join(commandParts, " ")
	functions := map[string]string{
		"flow create":                "runFlowCreate",
		"flow read":                  "runFlowRead",
		"flow phase set":             "runFlowPhaseSet",
		"flow phase complete":        "runFlowPhaseAction",
		"flow phase block":           "runFlowPhaseAction",
		"flow phase needs-attention": "runFlowPhaseAction",
		"flow phase restart":         "runFlowPhaseRestart",
		"flow phase add-child":       "runFlowPhaseAddChild",
		"flow plan set":              "runFlowPlanSet",
		"flow pr set":                "runFlowPRSet",
		"flow merge set":             "runFlowMergeSet",
		"plan save":                  "runPlanSave",
		"plan read":                  "runPlanRead",
		"plan phase set":             "runPlanPhase",
	}
	functionName, ok := functions[key]
	return functionName, ok
}

func functionSource(source, functionName string) (string, bool) {
	start := strings.Index(source, "func "+functionName+"(")
	if start < 0 {
		return "", false
	}
	bodyStart := strings.Index(source[start:], "{")
	if bodyStart < 0 {
		return "", false
	}
	bodyStart += start
	depth := 0
	for i := bodyStart; i < len(source); i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[start : i+1], true
			}
		}
	}
	return "", false
}

func cliHasFlag(cliSource, flagName string) bool {
	for _, flagType := range []string{"String", "Int", "Bool"} {
		if strings.Contains(cliSource, `flags.`+flagType+`("`+flagName+`"`) {
			return true
		}
	}
	return false
}

func fencedBashBlocks(markdown string) []string {
	var blocks []string
	var current []string
	inBash := false
	for _, line := range strings.Split(markdown, "\n") {
		switch {
		case strings.TrimSpace(line) == "```bash":
			inBash = true
			current = nil
		case inBash && strings.TrimSpace(line) == "```":
			blocks = append(blocks, strings.Join(current, "\n"))
			inBash = false
		case inBash:
			current = append(current, line)
		}
	}
	return blocks
}
