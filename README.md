# Beadloom

Orchestrate parallel task execution across Claude Code sessions using [Beads](https://github.com/steveyegge/beads) task graphs.

Beadloom reads a task graph from a Beads database, computes critical paths and parallelizable work, then spawns multiple Claude Code sessions across git worktrees to execute tasks concurrently. It turns your project's issue backlog into an automated, parallel execution pipeline.

## Prerequisites

- [Go](https://go.dev/) 1.23+
- [Beads](https://github.com/steveyegge/beads) (`bd` CLI) installed and initialized in your repo
- [Claude Code](https://claude.ai/claude-code) CLI (`claude`) installed
- `ANTHROPIC_API_KEY` environment variable (required for `infer-deps` command)

## Installation

```bash
go install ./cmd/beadloom
```

Or build locally:

```bash
make build
```

## Quick Start

```bash
# 1. Make sure your repo has beads initialized
bd status

# 2. See what beadloom would do
beadloom plan

# 3. Preview without executing
beadloom run --dry-run

# 4. Execute (agents run with --dangerously-skip-permissions by default)
beadloom run

# 5. Or run in safe mode (agents will prompt for permissions)
beadloom run --safe
```

## Example Walkthrough

Suppose you have a project with these beads tasks and dependencies:

```
bd-001  Setup database schema
bd-002  Build API endpoints         (blocked by bd-001)
bd-003  Build frontend components   (blocked by bd-001)
bd-004  Write API docs              (blocked by bd-002)
bd-005  Integration tests           (blocked by bd-002, bd-003)
bd-006  Deploy pipeline             (blocked by bd-005)
```

Running `beadloom plan` produces:

```
Beadloom Execution Plan
=======================

Tasks: 6 open, 5 blocked
Critical path: bd-001 -> bd-002 -> bd-005 -> bd-006 (4 tasks, est. 4 units)
Waves: 4
Max parallelism: 4 (2 tasks in widest wave)

Wave 1 (1 task, independent):
  bd-001   Setup database schema            * critical

Wave 2 (2 tasks, after wave 1):
  bd-002   Build API endpoints              * critical
  bd-003   Build frontend components

Wave 3 (2 tasks, after wave 2):
  bd-004   Write API docs
  bd-005   Integration tests                * critical

Wave 4 (1 task, after wave 3):
  bd-006   Deploy pipeline                  * critical
```

Beadloom identifies that:
- **Wave 1** must run first (schema has no dependencies)
- **Wave 2** can run API and frontend **in parallel** (both only depend on schema)
- **Wave 3** can run docs and integration tests **in parallel**
- **Wave 4** is the final deploy step
- The **critical path** is schema -> API -> tests -> deploy (any delay here delays everything)

Running `beadloom run --max-parallel 2` then:

1. Creates a git worktree for `bd-001` via `bd worktree create`
2. Spawns a Claude Code session in that worktree with a generated prompt
3. Waits for the agent to finish and run `bd close bd-001`
4. Creates worktrees for `bd-002` and `bd-003`, spawns agents in parallel
5. Continues wave by wave until all tasks complete or a critical task fails

During execution, `beadloom status` shows:

```
Beadloom -- Wave 2/4 -- 1 of 6 tasks complete [3m 22s elapsed]

  WAVE 1 (done)
    > bd-001   Setup database schema            *  [2m 14s]

  WAVE 2 (running)
    o bd-002   Build API endpoints              *  [running 1m 08s]
    o bd-003   Build frontend components           [running 1m 05s]

  WAVE 3 (blocked)
    . bd-004   Write API docs
    . bd-005   Integration tests                *

  WAVE 4 (blocked)
    . bd-006   Deploy pipeline                  *


sample output: 


🚀 Beadloom: executing 7 tasks in 4 waves
```
🌊 Wave 1/4 (2 tasks)
  ▶ [beadloom_vizualiser-0e7.1] Setup PostgreSQL schema
  ▶ [beadloom_vizualiser-0e7] Task Visualiser Service
  [beadloom_vizualiser-0e7] 🔧 $ List project root contents
  [beadloom_vizualiser-0e7.1] 🔧 $ List project files
  [beadloom_vizualiser-0e7] 🔧 $ Check recent git history
  [beadloom_vizualiser-0e7.1] 🔧 $ Check go.mod contents
  [beadloom_vizualiser-0e7.1] 🔧 $ Mark task as in progress
  [beadloom_vizualiser-0e7] 📖 Reading /Users/joshharrison/Documents/beadloom_vizualiser/.worktrees/beadloom_vizualiser-0e7/AGENTS.md
  [beadloom_vizualiser-0e7.1] 🔧 $ Check beads status
  [beadloom_vizualiser-0e7.1] 📖 Reading /Users/joshharrison/Documents/beadloom_vizualiser/.worktrees/beadloom_vizualiser-0e7.1/AGENTS.md
  [beadloom_vizualiser-0e7] 🔧 $ Show the main task details
```

* = critical path
```

## Commands

### `beadloom plan`

Analyze the task graph and compute an execution plan:

```bash
beadloom plan                          # show execution plan
beadloom plan --max-parallel 3         # limit parallelism in plan
beadloom plan --filter "label=backend" # only include matching tasks
beadloom plan --filter "priority<=1"   # filter by priority
beadloom plan --filter "type=bug"      # filter by issue type
beadloom plan --json                   # machine-readable output
beadloom plan --output plan.json       # save plan to file
```

### `beadloom run`

Execute the plan (or plan + run in one shot):

```bash
beadloom run                           # plan and execute
beadloom run --safe                    # agents require permission approval
beadloom run --plan plan.json          # run from saved plan
beadloom run --max-parallel 2          # limit concurrent agents
beadloom run --timeout 45m             # per-task timeout
beadloom run --dry-run                 # show what would happen
```

### `beadloom status`

Monitor running sessions:

```bash
beadloom status                        # show current progress
beadloom status --watch                # auto-refresh every 5s
beadloom status --logs bd-a1b2         # show logs for a specific task
beadloom status --json                 # machine-readable output
```

### `beadloom cancel`

Abort running sessions:

```bash
beadloom cancel                        # cancel all (SIGTERM)
beadloom cancel bd-a1b2                # cancel specific task
beadloom cancel --force                # SIGKILL instead of SIGTERM
```

### `beadloom infer-deps`

Use Claude to infer task dependencies from titles:

```bash
beadloom infer-deps                    # dry-run: show inferred deps
beadloom infer-deps --apply            # write deps to beads via bd dep add
beadloom infer-deps --model claude-sonnet-4-6  # use a specific model
beadloom infer-deps --json             # machine-readable output
```

Sends open task summaries (ID, title, priority, type) to Claude, which returns dependency edges with reasons. Edges are validated (unknown IDs and self-deps are dropped) and checked for cycles before being applied. Useful for bootstrapping a dependency graph on a project with many independent tasks.

Requires the `ANTHROPIC_API_KEY` environment variable to be set.

### `beadloom viz`

Visualize the task dependency graph:

```bash
beadloom viz                           # ASCII DAG in terminal
beadloom viz --format dot > graph.dot  # Graphviz DOT format
dot -Tsvg graph.dot > graph.svg        # render with Graphviz
```

## How It Works

1. **Graph Building** -- Queries `bd list --json --status open` for all open tasks, then `bd dep list <id>` for each to build a directed acyclic graph (DAG). Detects cycles defensively.
2. **Critical Path Analysis** -- Runs the Critical Path Method (CPM): Kahn's topological sort, forward pass (earliest start/finish), backward pass (latest start/finish), slack calculation. Tasks with zero slack form the critical path.
3. **Wave Computation** -- Groups tasks by earliest start time into parallel waves. Tasks in the same wave have no inter-dependencies and can run concurrently.
4. **Execution** -- For each wave, creates git worktrees via `bd worktree create` (which sets up beads database redirects automatically), spawns Claude Code sessions with generated prompts, and waits for completion. Critical path failures halt the pipeline; non-critical failures log warnings and continue.
5. **Reporting** -- Real-time terminal status, JSON output, persistent state in `.beadloom/state.json` (works across terminal sessions).

## Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--db <path>` | auto-discover | Beads database path (passed through to `bd`) |
| `--max-parallel <n>` | `4` | Max concurrent agent sessions |
| `--safe` | `false` | Don't pass `--dangerously-skip-permissions` to Claude |
| `--timeout <duration>` | `30m` | Per-task timeout |
| `--worktree-dir <path>` | `.worktrees/` | Directory for git worktrees |
| `--prompt-template <path>` | built-in | Custom agent prompt template |
| `--json` | `false` | Machine-readable output |

## Custom Prompt Templates

Override the default agent prompt with a Go `text/template` file:

```bash
beadloom run --prompt-template my-prompt.tmpl
```

Available template variables:

| Variable | Description |
|----------|-------------|
| `{{.TaskID}}` | Beads issue ID (e.g., `bd-001`) |
| `{{.Title}}` | Issue title |
| `{{.Description}}` | Issue description |
| `{{.Acceptance}}` | Acceptance criteria |
| `{{.Design}}` | Design notes |
| `{{.Notes}}` | Additional notes |
| `{{.WorktreePath}}` | Path to the git worktree |
| `{{.BranchName}}` | Git branch name (e.g., `beadloom/bd-001`) |
| `{{.WaveIndex}}` | Wave number (0-indexed) |
| `{{.WaveSize}}` | Number of tasks in this wave |
| `{{.IsCritical}}` | Whether this task is on the critical path |

See `templates/default.prompt.tmpl` for the default template. Agents are instructed to run `bd close <id>` on completion and `bd update <id> --status blocked` if stuck.

## bd Compatibility

Beadloom is tested against `bd` v0.52+. It uses these `bd` commands:

| Command | Purpose |
|---------|---------|
| `bd list --json --status open --limit 0` | Fetch all open tasks |
| `bd dep list <id> --json` | Get task dependencies (blockers) |
| `bd dep list <id> --direction=up --json` | Get task dependents (what it blocks) |
| `bd worktree create <path> --branch <name>` | Create isolated worktree with beads redirect |
| `bd worktree remove <path>` | Clean up worktree |
| `bd worktree list --json` | List existing worktrees |
| `bd close <id> --reason "..."` | Mark task as done |
| `bd update <id> --status blocked --append-notes "..."` | Mark task as blocked |
| `bd ready --json --limit 0` | List ready (unblocked) tasks |
| `bd dep add <blocked> <blocker>` | Add a dependency edge |

## Architecture

```
beadloom/
├── cmd/beadloom/         # CLI entry point (cobra)
├── internal/
│   ├── bd/               # bd CLI wrapper (list, show, dep, worktree, close)
│   ├── claude/           # Claude API client (dependency inference)
│   ├── graph/            # Task DAG builder + cycle detection
│   ├── cpm/              # Critical Path Method analyzer
│   ├── planner/          # Execution plan + prompt generation
│   ├── worktree/         # Git worktree lifecycle management
│   ├── orchestrator/     # Wave execution engine + Claude agent spawning
│   ├── reporter/         # Terminal status display + JSON output
│   └── state/            # Persistent state (.beadloom/state.json)
└── templates/            # Default agent prompt template
```

## Edge Cases

- **Merge conflicts**: Each worktree gets its own branch (`beadloom/<task-id>`), so no conflicts during execution. Conflicts arise at merge time -- beadloom does not auto-merge; use your normal PR workflow.
- **Agent failures**: Critical path failures halt the pipeline. Non-critical failures log warnings and continue. Failed task worktrees are preserved for inspection.
- **Database contention**: All worktrees share the same beads database (via `bd worktree` redirects). `bd` uses file-level locking for writes.
- **Resource limits**: Each Claude session consumes significant memory/API quota. Default of 4 concurrent sessions is conservative.
- **Cycle detection**: Beads shouldn't allow dependency cycles, but beadloom checks defensively and errors with the cycle path if found.

## Development

```bash
make build      # build binary
make test       # run tests
make test-v     # run tests verbose
make lint       # go vet
make clean      # remove binary and temp dirs
```
