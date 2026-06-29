# flowstate

An agent-orchestration TUI that drives coding agents (Claude, Codex) through
structured, phase-gated **Flows** — from plan to merge — so they ship
high-quality code instead of one-shot diffs.

![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)

flowstate began as a multi-repository git-worktree TUI, and that machinery is
still here. But the centerpiece is **Flow**: a task-centric workflow engine that
takes a coding agent through plan → plan review → implementation → review loop →
PR creation → autoreview → merge, with quality gates between every step and an
honest, auditable record of what happened.

## What is a Flow?

A Flow is a persisted record of one task moving through a fixed, gated pipeline.
Each **phase** has a job, and flowstate will not let work jump ahead until the
gate in front of it is satisfied.

| Phase | Goal |
|-------|------|
| `plan` | Produce a saved plan artifact for the task. |
| `plan-review` | Review and approve (or request changes to) the plan. |
| `implementation` | Execute the approved plan in a dedicated worktree. |
| `review-loop` | Critique the implementation and drive revisions. |
| `pr-creation` | Commit, push, and open the pull request. |
| `autoreview` | Second-level review against the open PR. |
| `merge` | Resolve conflicts and merge deliberately. |

Four ideas make Flow produce quality rather than just motion:

- **Gated progression.** A phase becomes launchable only when its predecessors
  clear their gate. Plan Review must end `approved` (or `approved_with_concerns`)
  before Implementation opens; PR Creation must record real PR metadata before
  Autoreview unlocks; Implementation's child phases must finish before anything
  downstream proceeds.
- **Honest status, derived readiness.** Agents report only their own phase, using
  five statuses (`running`, `needs_attention`, `completed`, `blocked`,
  `skipped`). flowstate derives `pending`/`ready` itself — agents never decide
  what runs next, so they can't fake progress.
- **Auto-progression with human checkpoints.** With auto mode on, completing a
  phase launches the next ready one automatically. Automation deliberately stops
  before **Merge**, and PR metadata must be recorded by hand, so the
  irreversible steps stay in human hands.
- **Recorded outcomes.** Review phases capture structured outcomes
  (`approved`, `changes_requested`, `passed`, `blocked`, …) and a linked plan
  syncs as phases complete, leaving an auditable trail.

The canonical phase semantics — statuses, the transition table, gating, and
derived readiness — live in [docs/flow-phases.md](docs/flow-phases.md).

## Install

Build from source:

```bash
git clone <your fork of flowstate>
cd flowstate
make build
```

The binary is built to `bin/flowstate`.

## Quickstart

```bash
# Run with default scan root (~/dev)
./bin/flowstate

# Run with a custom root
WORKTREE_ROOT=~/projects ./bin/flowstate
```

flowstate starts in the **Flows** pane (mode `8`). A typical loop:

1. Press `n` to create a new Flow: give it a title and instructions. Leave
   **Plan Now** checked to launch the Plan phase immediately, or uncheck it to
   park the Flow with its worktree and branch ready.
2. flowstate creates a dedicated `flow/<slug>` branch and worktree, then launches
   the coding agent for the ready phase. CLI agents (`codex`, `claude`) run in a
   runtime-only **embedded terminal** inside the pane.
3. The agent does the phase's work and marks the phase done through the
   `flowstate flow` CLI (via the `flowstate` skill — see [Agent skills](#agent-skills)).
4. With auto mode on (`m` toggles it), the next ready phase launches
   automatically. Press `g` to launch the next launchable phase manually.
5. Press `h` to choose headless (`codex exec` / `claude --print`) or interactive
   launches; `E` to set reasoning effort; `tab` to focus the embedded terminal.

The rest of this document covers the Flow views in depth, how agents drive Flows,
and the supporting git tooling Flow runs on.

## Flows view (mode 8)

Browse persisted Flow records for the selected repo. Rows show status, branch or
worktree basename, phase progress plus the current phase state, linked plan ID
when present, PR number or label, updated date, and title. Use `/` to filter by
title, instructions, status, branch, worktree basename, plan metadata, PR
metadata, phase titles/statuses/summaries, and linked session metadata.

Press `n` to create a new Flow with one form for title, multiline instructions,
and optional base ref plus Headless and Plan Now checkboxes; use `alt+enter` for
instruction newlines. Plan Now is checked by default and immediately launches the
initial Plan phase after creating the Flow. Uncheck it to create a parked Flow
with its instructions, worktree, branch, and start commit saved; the ready Plan
phase can be launched later from the Flow row.

On a Flow row, `enter` expands or collapses phase detail rows; `o` pages the
linked plan body in `less -R`, and flowstate shows a status message when the
selected Flow has no linked plan. With destructive mode enabled (`D`), `d`
deletes only the selected top-level Flow record under the Flow artifact store; it
does not remove repositories, worktrees, branches, checked-out code, linked
plans, sessions, transcripts, or active embedded terminals. Expanded phase rows
cannot be deleted with this action.

Press `g` to launch the first launchable phase in the selected Flow's canonical
phase order. This action uses the selected Flow, so a highlighted pending phase
row can still launch an earlier ready sibling, and nothing is persisted when no
phase is launchable. Press `y` to copy the selected Flow worktree path from
either a Flow row or one of its expanded phase rows. Press `r` on an expanded
phase row with an attached provider session to resume that session; CLI resumes
are recorded as a fresh Flow phase launch attempt, while `codex-app` resumes
navigate to the existing app thread without extra launch tracking.

Press `m` on a Flow row or expanded phase row to toggle per-Flow auto mode, which
is on by default for new Flows and persisted on that Flow record. Flows created
before this field existed remain manual until auto mode is toggled on. When auto
mode is on, a successful completed phase transition launches the next ready
non-merge phase in that same Flow through the same launch path as pressing `g`.
For CLI phases running in an embedded Flow terminal, flowstate waits until the
completed phase's terminal exits normally and auto-closes before launching the
next phase; that exit also triggers a Flow refresh so completions recorded after
the last refresh are picked up promptly. Skipped, blocked, needs-attention,
failed-launch, or missing-PR-metadata states do not auto-launch. Automation stops
before Merge: if Autoreview completes and Merge becomes ready, flowstate keeps
auto mode on and requires the existing manual Merge launch.

### Headless vs. interactive launches

Flow headless mode is on by default: selected CLI `codex` and `claude` phase
launches run in a runtime-only embedded terminal inside the flows pane. Press `h`
to choose the CLI command mode: headless runs `codex exec` or `claude --print`,
while headless off runs interactive `codex` or `claude` in the same embedded Flow
terminal. Headless-off Flow launches prefill the phase prompt without submitting
it, then focus the Flow terminal in input mode so you can review or edit it
before pressing enter. Headless launches keep focus on the Flow list.

Creating a new Flow has its own default-on Headless checkbox for the initial Plan
launch; uncheck it for an interactive initial Plan launch. That checkbox is
ignored when Plan Now is off and does not change the selected-phase `h` setting.
Manual phase launches, auto-launched phases, and new Flow Plan launches all use
the configured agent and that agent's configured effort. Press `E` to choose the
selected CLI agent's reasoning effort; the shortcut pane shows the current value.
Codex CLI launches use `--config model_reasoning_effort=<effort>`, Claude
launches use `--effort <effort>`, and session resumes do not receive effort
flags. `codex-app` always uses the external deep-link route and keeps
app-side/default reasoning.

Embedded headless output is readable terminal text, not raw JSON events: `codex
exec` streams progress as it works, while `claude --print` prints its result when
the run completes, so a Claude phase can show an empty terminal until it finishes
(the terminal tab still shows `running`). While a Flow terminal is open, the Flow
list uses a smaller top panel and the terminal uses a bottom panel; `tab`
switches focus between them while the right pane remains active. Manually tabbing
into Flow terminal focus starts in flowstate command mode: `left`/`right` cycle
Flow terminals, `1`-`9` switches by number, `x` closes, `d` detaches to tmux when
available and opens the detached session in an external terminal, `q`/`esc`
quits, unknown ordinary keys do not pass through to the PTY, `ctrl+]` sends a
literal `ctrl+]`, and `i` enters terminal input mode. In input mode, keys pass
through to the PTY (including agent shortcuts like `ctrl+g`) and `ctrl+]` returns
to command mode.

When Implementation is still gated by Plan Review, flowstate reports the Plan
Review state and notes instead of launching. When PR Creation is complete but
structured PR metadata is missing, Autoreview remains pending and the Flow row
shows `autoreview:missing-pr`. Expanded phase rows group child implementation
phases directly under Implementation.

### Recovery labels

Flow rows surface recoverable partial states so they are not confused with
ordinary empty or pending work. A saved Flow with no branch/worktree metadata
shows `recover-worktree`, a running phase with a recorded launch but no attached
session yet shows `await-session`, a phase with an attached session whose launch
ID does not match the phase's launch attempts shows `session-mismatch`, and an
attached session that lacks a provider session ID shows `missing-session-id`. A
pending Autoreview phase whose PR Creation predecessor completed without
structured PR metadata shows `missing-pr`.

When an expanded phase row shows `await-session`, and no running or starting
embedded Flow terminal is attached to that same Flow phase, the selected phase
row exposes `x reset ready`. Confirming the prompt removes the newest orphan
launch attempt and lets flowstate derive the phase back to `ready`. This is TUI
recovery for an abandoned launch attempt, not a new agent transition; `ready`
still cannot be set through `flowstate flow phase set`.

## Active Flows view (mode 9)

Browse active Flow records across all repos. This view hides merged Flow records;
moving focus to the left repo pane temporarily filters the visible active rows to
the selected repo, and returning focus to the middle pane restores the global
list. Normal Flow actions, phase launches, attached-session resumes, auto-mode
toggles, and embedded Flow terminals work from the visible active Flow rows.

## Driving Flows from agents

Flows are task-centric workflow records stored beside sessions and plans under
`<sessions root>/flows/<flow-id>/meta.json`. The TUI can create a new Flow and
record a launch for the next launchable phase; agents perform normal phase
progression through the `flowstate flow` CLI (these load config to resolve the
artifact root but never scan repositories or start the TUI):

```bash
# Create a flow; --repo-path must be absolute and --json is required in v1.
flowstate flow create --title "Ship saved plans" \
  --instructions "Plan, implement, review, open a PR, and merge." \
  --repo-path "$REPO" --json

# List or read flows.
flowstate flow list --repo-path "$REPO" --json
flowstate flow read --flow-id "$FLOW_ID"

# Link a saved plan artifact back to a flow.
flowstate flow plan set --flow-id "$FLOW_ID" --plan-id "$PLAN_ID"

# Record common phase outcomes without hand-assembling --status. These commands
# print JSON with the updated phase and the next actionable phase state. For
# Plan Review, complete defaults to approved, needs-attention defaults to
# changes_requested, and block defaults to blocked unless --outcome is supplied.
# Autoreview defaults are passed, needs_attention, and blocked.
flowstate flow phase complete --flow-id "$FLOW_ID" --phase-id plan --summary "Saved plan"
flowstate flow phase needs-attention --flow-id "$FLOW_ID" --phase-id plan-review \
  --notes "Revise the rollout section"
flowstate flow phase block --flow-id "$FLOW_ID" --phase-id implementation \
  --notes "Waiting on review"

# The lower-level phase set command remains available for explicit status,
# outcome, summary, and notes updates. approved_with_concerns,
# changes_requested, and blocked Plan Review outcomes require --notes.
flowstate flow phase set --flow-id "$FLOW_ID" --phase-id plan-review \
  --status completed --outcome approved_with_concerns --notes "Watch rollout risk"

# Split Implementation into ordered child phases. Re-running the same command
# updates the stable child phase without duplicating it.
flowstate flow phase add-child --flow-id "$FLOW_ID" \
  --parent-phase-id implementation \
  --phase-id implementation-api \
  --title "API integration" \
  --order 10

# Record structured PR metadata after PR Creation opens or updates a PR.
flowstate flow pr set --flow-id "$FLOW_ID" \
  --provider github \
  --number 123 \
  --url "https://github.com/owner/repo/pull/123" \
  --head "$FLOW_BRANCH" \
  --base main \
  --status open

# Record structured merge metadata after the explicit merge action succeeds.
flowstate flow phase set --flow-id "$FLOW_ID" \
  --phase-id merge \
  --status completed \
  --outcome merged \
  --summary "Merged PR at $MERGE_COMMIT."

flowstate flow merge set --flow-id "$FLOW_ID" \
  --status merged \
  --commit "$MERGE_COMMIT" \
  --merged-at "2026-06-08T15:04:05Z"
```

When a Flow is linked to a saved plan, transitioning a Flow phase to `completed`
also marks a matching saved-plan phase with the same normalized phase ID as
`completed`. Missing saved-plan phases are ignored. If that sync fails, flowstate
marks the Flow phase `needs_attention` and reports the persistence error.
Repeating `completed` for an already-completed Flow phase preserves that
completed state even if the linked-plan sync later fails.

Child implementation phases gate downstream readiness in phase order: review loop
and PR creation remain pending until required implementation children are
completed or explicitly skipped with notes. Flow phase launch prompts stay
minimal: Plan Review and Implementation point to the saved plan artifact, while
Review Loop and PR Creation include only the worktree, branch, and start commit
metadata needed to inspect the changes. Built-in prompts tell Plan to produce
only a plan, Plan Review to use the review-loop skill with max 6 loops,
Implementation to use the `commit` skill, Review Loop to use the review-loop
workflow with goal `review-and-revise` and `commit` when revisions are made, PR
Creation to use the `ship` skill, and Autoreview to use `ship` when fixes require
commits or pushes without embedding phase-restart recipes. All Flow phase launch
prompts also end with:
`After completing this phase goal, mark this Flow phase done with flowstate.`
Use `flowstate flow phase restart` to rerun a blocked or needs-attention phase as
`running`; if notes are omitted, flowstate records a standard rerun note.

For example, after addressing Autoreview findings:

```bash
flowstate flow phase restart --flow-id "$FLOW_ID" --phase-id autoreview
```

Autoreview is ready only after PR Creation is complete and `flowstate flow pr
set` has recorded provider, PR number, URL, head branch, and base branch
metadata. Merge stays an explicit phase: agents must record both the Merge phase
update and structured merge metadata through `flowstate flow merge set`;
`--status merged` requires existing PR metadata, a merge commit, and an RFC3339
merge timestamp. If merge is blocked, record a blocked Merge phase with notes
before setting structured merge status to `blocked`. The canonical phase
transition table, derived-readiness rules, and the on-disk compatibility story
are documented in [docs/flow-phases.md](docs/flow-phases.md).

The flow state root is resolved with this precedence: `--state-root` >
`FLOWSTATE_FLOW_STATE_ROOT` > `FLOWSTATE_PLAN_STATE_ROOT` > `FLOWSTATE_SESSION_STATE_ROOT` >
`[sessions].root` > the user state default. In TUI startup,
`FLOWSTATE_FLOW_STATE_ROOT`, `FLOWSTATE_PLAN_STATE_ROOT`, or `FLOWSTATE_SESSION_STATE_ROOT`
relocates the shared artifact root for sessions, plans, and flows.

## Agent skills

flowstate ships an agent-facing skill that drives a coding agent through a single
Flow phase. Its canonical source lives under `agent-skills/flowstate/` and is
intentionally outside Codex and Claude's repo auto-discovery directories, so it
can be symlinked into your user-level skill directory for use across repos:

- `agent-skills/flowstate/` (`flowstate`) — activates when `FLOWSTATE_FLOW_ID` and
  `FLOWSTATE_FLOW_PHASE_ID` are present, reads the active flow before updates, and
  uses the implemented `flowstate flow` and `flowstate plan` commands to persist
  phase progress, link saved plans, and record PR and merge metadata.

Install or symlink it into the user-level skill directory for your agent, such as
`~/.codex/skills/flowstate` for Codex, or the equivalent Claude skills directory.

## Saved plans (plans view, mode 7)

Browse saved agent plans for the selected repo. Rows show status, branch, phase
progress (`completed/total`), the updated date, and the title. Use `/` to filter
plans by title, summary, status, branch, worktree basename, provider, session ID,
launch ID, and phase titles/statuses. Press `x` to expand or collapse the
selected plan's phase rows, `o` to page the plan Markdown in `less -R`, `e` to
edit the plan Markdown, and `y` to copy the plan Markdown path. The edit action
opens `[editor].command` when configured, otherwise `$EDITOR`, and refreshes the
plans pane when that command exits; use wait flags such as `code --wait` for GUI
editors that detach by default. Press `a` to edit launch instructions for the
selected plan or selected phase, then `enter` to launch the selected agent or
`esc` to cancel; blank instructions are rejected. `enter` still toggles phase
rows, and `i` still opens plan launch instructions as compatibility aliases.

Plans are persisted explicitly by agents through the `flowstate plan` CLI rather
than captured from hooks. Plans share the agent-artifact root with sessions: they
are stored under `<sessions root>/plans/<plan-id>/` (`meta.json` plus `plan.md`),
that is `$XDG_STATE_HOME/flowstate/sessions/v1/plans/...` or
`~/.local/state/flowstate/sessions/v1/plans/...` by default. **Because plans live
beside sessions, moving or cleaning the sessions root (including via
`FLOWSTATE_PLAN_STATE_ROOT` or the TUI-level `FLOWSTATE_FLOW_STATE_ROOT`) also moves or
removes your saved plans.**

```bash
# Save (or update with --plan-id) a plan; reads Markdown from --file or stdin,
# prints only the plan_id.
printf '%s' "$PLAN_MD" | flowstate plan save --title "Persist plans" --status draft

# Record per-phase progress. Phase ids are trimmed and lowercased, so
# re-running phase set with the same logical id updates the phase in place.
flowstate plan phase set --plan-id "$PLAN_ID" --phase-id store --title "Store" --status completed --order 1

# Read plans back.
flowstate plan list --repo-path "$REPO" --json   # requires --json in v1
flowstate plan read --plan-id "$PLAN_ID"          # prints Markdown only
```

The plan state root is resolved with this precedence: `--state-root` >
`FLOWSTATE_PLAN_STATE_ROOT` > `FLOWSTATE_SESSION_STATE_ROOT` > `[sessions].root` > the user
state default. CLI-launched agents get `FLOWSTATE_SESSION_STATE_ROOT`,
`FLOWSTATE_PLAN_STATE_ROOT`, and `FLOWSTATE_FLOW_STATE_ROOT` set to the same resolved
artifact root. Omitted metadata is filled first from `FLOWSTATE_AGENT`,
`FLOWSTATE_LAUNCH_ID`, `FLOWSTATE_REPO_PATH`, `FLOWSTATE_WORKTREE_PATH`, `FLOWSTATE_BRANCH`, and
`FLOWSTATE_COMMIT`; for new plans, and for updates that provide a repo or worktree
location, flowstate also resolves best-effort repo, worktree, branch, and start
commit metadata from git. `codex-app` launches use macOS `open`, so they do not
inherit `FLOWSTATE_*` shell environment variables; flowstate uses the repo path as the
deep-link project path and includes worktree, state-root, plan, and flow values
as prompt-only launch metadata. That metadata includes copyable `flowstate plan
list --json --state-root ...` and `flowstate flow list --json --state-root ...`
examples that show where to pass the state root for subsequent plan and flow
commands. v1 has no TUI plan deletion.

## Supporting git views

Flow runs on top of flowstate's multi-repo worktree tooling. The UI has two
panes: repos on the left, content on the right. Press `enter` or `tab` on a
selected repo to focus the content pane; from the content pane, `bksp` returns
focus to the repo pane. Press `f2` to open the prompt-template editor for plan
and Flow launch prompts. The active pane is highlighted with a blue border.

**Destructive mode:** The app starts in read-only mode — deletion keys are
disabled. Press `D` (Shift+D) to toggle destructive mode on/off. When active, the
right pane border turns red and delete/drop hints appear in red as a visual
warning.

**Fuzzy filter:** Press `/` in the active pane to type-ahead filter repos or
right-pane items. `enter` keeps the filter, `esc` clears it, `backspace` edits
it.

Press `1`–`9` or use arrow keys to switch the right pane between worktrees,
branches, stashes, history, reflog, sessions, plans, flows, and active flows.
Horizontal view switching wraps between worktrees and active flows. Press `V` to
choose which numbered view flowstate opens on future launches; leaving it unset
keeps the built-in startup default of Flows.

- **Worktrees (mode 1)** — all worktree checkouts for the repo; the root worktree
  is pinned first with a blue `[root]` annotation. `a` launches the selected
  agent in a worktree, `N` creates a worktree and launches an agent, `n` creates
  one without launching, `P` creates a review worktree from a GitHub PR number or
  URL, `m` moves/renames a linked worktree, `f`/`F` fetch/pull, `u` unlocks a
  locked worktree, and `x` shows captured agent sessions inline. Locked worktrees
  are never moved, deleted, or pruned.
- **Branches (mode 2)** — non-worktree branches plus the pinned root branch;
  worktree branches are hidden here. `n` creates a branch, `d` deletes a
  non-worktree branch (destructive mode), `f`/`F` fetch/pull for checked-out
  branches, and ahead branches preview unpushed commits.
- **Stashes (mode 3)** — `enter` pages a stash diff, `d` drops it (destructive
  mode).
- **History (mode 4)** — recent commits; `enter` pages a diff, `y` copies the
  hash, `t`/`c` open a session/VSCode.
- **Reflog (mode 5)** — HEAD reflog entries; `enter` pages the entry diff, `y`
  copies the hash.
- **Sessions (mode 6)** — captured Claude Code and Codex sessions for the repo;
  `o`/`enter` page the transcript, `s` pages the summary, `r` resumes (CLI agents
  embed in-pane), `y` copies the raw provider session ID.

When the left repo pane is focused, `f` runs `git fetch --prune` for the visible
repos, and `n` creates a new repository under the resolved scan root (optionally
creating a GitHub repo and wiring `origin` via `gh`).

### Embedded session terminals

Resuming a CLI `codex` or `claude` session from the sessions view opens a
runtime-only embedded terminal in the sessions pane. Keys go directly to the
active PTY (including agent shortcuts like `ctrl+g`); press `ctrl+]` for flowstate
commands: `ctrl+] 1`-`9` switches terminals, `ctrl+] l` opens a saved-session
picker, `ctrl+] d` detaches a tmux-backed terminal and opens a new external
terminal attached to that tmux session, `ctrl+] x` dismisses an exited terminal
or confirms termination of a running one, `ctrl+] q` or `ctrl+] esc` quits with
cleanup, and `ctrl+] ctrl+]` sends a literal `ctrl+]` to the agent.

When `tmux` is available at launch time, embedded CLI terminals start inside a
per-launch tmux session so detach can close flowstate's embedded client while the
agent keeps running in tmux. Quitting flowstate while embedded terminals are
running asks for confirmation and terminates them first. Embedded terminals are
not restored after flowstate restarts.

Session data is stored under the user state directory by default:
`$XDG_STATE_HOME/flowstate/sessions/v1`, or `~/.local/state/flowstate/sessions/v1` when
`XDG_STATE_HOME` is unset. Transcripts may contain secrets or private prompts;
flowstate keeps them outside repositories and uses restrictive file permissions
for created session files. Provider session IDs are stored in hashed directory
names instead of raw path components. Sessions missing a provider session ID
cannot be resumed; flowstate reports this in the status line instead of starting
a fresh provider session.

## Keys

**Left pane (repos)**

| Key | Action |
|-----|--------|
| `↑`/`k` · `↓`/`j` | Select previous / next repo |
| `/` | Fuzzy filter repos |
| `A` | Choose and persist the coding agent (`codex`, `codex-app`, or `claude`) |
| `V` | Choose and persist the startup default view (`1`–`9`) |
| `D` | Toggle destructive mode |
| `f` | Fetch all currently visible repos with `--prune` |
| `n` | Create a new local repo under the scan root |
| `enter`/`tab` | Switch focus to right pane |
| `f2` | Edit prompt templates |
| `q`/`esc` | Quit |

**Right pane (content)**

| Key | Action |
|-----|--------|
| `↑`/`k` · `↓`/`j` | Move selection up / down |
| `/` | Fuzzy filter the current item list |
| `1`–`9` | Switch to worktrees / branches / stashes / history / reflog / sessions / plans / flows / active flows |
| `←`/`→`/`l` | Switch views (wraps between worktrees and active flows; `h` is a flows-view toggle) |
| `h` | Previous view outside flows view; toggle Flow headless/interactive mode in flows view |
| `E` | Choose and persist reasoning effort for the selected CLI agent (flows view) |
| `enter` | Page a diff/transcript in `less`, resume an inline worktree session, or expand/collapse plan or Flow phases |
| `g` | Launch the next launchable phase for the selected Flow (flows view) |
| `n` | New worktree (worktrees), new branch (branches), or new Flow (flows) |
| `P` | Create a review worktree from a GitHub PR number or URL |
| `N` | Create a new worktree and launch the selected coding agent |
| `m` | Move/rename a linked worktree (worktrees), or toggle auto mode (flows) |
| `A` | Choose and persist the coding agent (`codex`, `codex-app`, or `claude`) |
| `V` | Choose and persist the startup default view (`1`–`9`) |
| `a` | Launch the selected agent in the worktree, or launch the selected plan/plan phase |
| `d` | Delete worktree/branch, drop stash, or delete Flow data — requires destructive mode |
| `p` | Prune stale worktree — requires destructive mode (worktrees view) |
| `u` | Unlock a locked worktree (worktrees view) |
| `f`/`F` | Fetch with `--prune` / Pull with `--ff-only` |
| `t` | Open or attach to a tmux/Zellij session for the worktree |
| `c` | Open VSCode at worktree path |
| `x` | Show/hide worktree sessions, expand/collapse plan phases, or reset an `await-session` Flow phase |
| `y` | Copy hash / session ID / plan path / Flow worktree path (view-dependent) |
| `r` | Resume selected agent session or attached Flow phase session |
| `s` | Page selected agent session summary (sessions view) |
| `o` | Page transcript, plan Markdown, or linked plan body (view-dependent) |
| `e` | Edit selected plan Markdown (plans view) |
| `i` | Alias for plan implementation launch |
| `D` | Toggle destructive mode |
| `bksp` | Switch focus to left pane |
| `f2` | Edit prompt templates |
| `q`/`esc` | Close a prompt/dialog or quit |

## Configuration

flowstate reads an optional TOML config file before scanning repositories:

```text
$XDG_CONFIG_HOME/flowstate/config.toml
~/.config/flowstate/config.toml
```

Example:

```toml
[scan]
root = "~/projects"
max_depth = 2

[agent]
command = "codex"
codex_reasoning_effort = "high"
claude_reasoning_effort = "max"
plan_prompt = "Implement the saved plan {title} (ID: {plan_id}) at {plan_path}. Read the plan file, then begin implementation."

[ui]
default_view = 8

[flow_prompts]
implementation = "Implement {plan_path} from {worktree_path}, then use the commit skill before completing."
pr_creation = "Use the ship skill for {branch}, then record PR metadata for flow {flow_id}."

[sessions]
root = "~/.local/state/flowstate/sessions/v1"
copy_raw_transcripts = false

[bootstrap]
timeout_seconds = 120

[[bootstrap.hooks]]
repo_path = "~/projects/flowstate"
script = ".flowstate/bootstrap"
```

`WORKTREE_ROOT` overrides `[scan].root` when both are set. The scan root is
cleaned before scanning; explicit relative roots preserve relative repo paths for
compatibility. `[agent].codex_reasoning_effort` and
`[agent].claude_reasoning_effort` configure provider-specific effort for new CLI
agent launches; empty or `default` keeps provider defaults. `[ui].default_view`
accepts `1` through `9` and controls the startup view; omitting it keeps the
built-in Flows default. `[agent].plan_prompt` customizes the editable
instructions shown before launching an agent from the plans pane, while
`[flow_prompts]` customizes Flow phase launch templates. flowstate appends `After
completing this phase goal, mark this Flow phase done with flowstate.` to
configured Flow templates unless the template already ends with that exact
standalone instruction. `[editor].command` customizes the editor used by the
plans pane edit action. See [docs/config.md](docs/config.md) for the full config
reference.

| Env var | Default | Description |
|---------|---------|-------------|
| `WORKTREE_ROOT` | `[scan].root` or `~/dev` | Root directory to scan for git repos and create new repos under; depth defaults to 2 and can be reduced with `[scan].max_depth` |
| `TERMINAL` | unset | Terminal command to use when `t` opens a worktree outside tmux/Zellij |
| `FLOWSTATE_SESSION_STATE_ROOT` | `[sessions].root` or user state default | Session hook storage root; normally set automatically for agents launched by flowstate |
| `FLOWSTATE_PLAN_STATE_ROOT` | `FLOWSTATE_SESSION_STATE_ROOT`, `[sessions].root`, or user state default | Saved-plan artifact root for `flowstate plan`. In the TUI it relocates the whole artifact root, moving sessions, plans, and flows |
| `FLOWSTATE_FLOW_STATE_ROOT` | `FLOWSTATE_PLAN_STATE_ROOT`, `FLOWSTATE_SESSION_STATE_ROOT`, `[sessions].root`, or user state default | Flow artifact root for `flowstate flow`. In the TUI it has highest precedence for the shared sessions/plans/flows artifact root |

### Agent session hooks

CLI agents launched from flowstate with `a`, `N`, Flow `g`, or session resume `r`
are wired automatically: flowstate passes Claude Code or Codex a session-end hook
that calls the current flowstate binary, and it exports `FLOWSTATE_*` metadata so hook
records can be associated with the repo, worktree, branch, and launch. `codex-app`
opens via macOS deep link instead; flowstate scrubs inherited `FLOWSTATE_*` from `open`
and includes prompt-only launch metadata with copyable `--state-root` command
examples.

For manual agent sessions that are not launched by flowstate, configure Claude
Code or Codex hooks to call flowstate:

```bash
flowstate session-hook --provider claude
flowstate session-hook --provider codex
```

For local testing, use `--state-root /tmp/flowstate-sessions-test`.

`session-hook` loads the normal flowstate config, so `[sessions].root` and
`copy_raw_transcripts` apply to hook ingestion. `--state-root` overrides the
configured sessions root for one hook invocation. Raw provider transcript copies
are off by default; set `copy_raw_transcripts = true` to also preserve
provider-native `raw.jsonl` alongside normalized transcript events.

## Development

```bash
make build   # Build binary to bin/flowstate
make test    # Run all tests
make run     # Build and run with optional ignored repo-local .config/
make tidy    # go mod tidy
make clean   # Remove bin/
```

CI requires a clean `gofmt -l .`, `make test`, and `make build`.

## Requirements

- Go 1.26+
- Git 2.15+ (worktree support)
- macOS clipboard: `pbcopy` (included with macOS)
- Linux clipboard: install one of `wl-copy`, `xclip`, or `xsel`
- Linux terminal launch: set `TERMINAL` to your terminal emulator; when no
  tmux/Zellij/`TERMINAL` launch is available, flowstate falls back to launching
  `$SHELL` in the worktree directory
