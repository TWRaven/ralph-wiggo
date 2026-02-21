# ralph-wiggo

An autonomous AI agent loop that turns feature descriptions into working code. Describe what you want, and ralph-wiggo generates a PRD, breaks it into stories, and spawns [Claude Code](https://claude.ai/code) agents to implement each one — tracking progress and providing a real-time web dashboard.

Inspired by [Ralph](https://github.com/snarktank/ralph). Written in Go.

## How it works

```
Feature description
        |
        v
  PRD generation  ───>  Interactive Claude session asks clarifying questions
        |
        v
  PRD conversion  ───>  Markdown PRD → structured prd.json with user stories
        |
        v
    Agent loop    ───>  For each story: spawn Claude agent → implement → verify → commit
        |
        v
   Working code on a feature branch
```

Each story gets its own Claude agent session with full tool access (read, write, edit, bash, etc.). The agent reads the PRD, implements the story, runs quality checks, and commits. If a story fails, it retries up to a configurable number of iterations.

## Quick start

### Prerequisites

- [Go 1.26+](https://go.dev/dl/) (or use [Devbox](https://www.jetbox.io/devbox): `devbox shell`)
- [Claude Code CLI](https://claude.ai/code) installed and authenticated

### Install

```sh
go install github.com/TWRaven/ralph-wiggo/cmd/ralph-wiggo@latest
```

Or build from source:

```sh
git clone https://github.com/TWRaven/ralph-wiggo.git
cd ralph-wiggo
go install ./cmd/ralph-wiggo
```

### Usage

**Full workflow** — describe a feature, ralph-wiggo does the rest:

```sh
ralph-wiggo full "add user authentication with JWT tokens"
```

This runs three steps with confirmation prompts between each:
1. Interactive PRD generation (Claude asks clarifying questions)
2. PRD-to-JSON conversion (creates `prd.json` with structured user stories)
3. Agent loop (implements each story sequentially)

**Individual commands:**

```sh
# Generate a PRD interactively
ralph-wiggo prd "add a notification system"

# Convert an existing PRD markdown to prd.json
ralph-wiggo convert tasks/prd-notifications.md

# Run the agent loop on an existing prd.json
ralph-wiggo run prd.json

# Resume a stopped run (picks up where it left off)
ralph-wiggo run prd.json

# Start the web dashboard standalone
ralph-wiggo serve prd.json
```

## Web dashboard

Launch with `--ui` to get a real-time dashboard alongside the agent loop:

```sh
ralph-wiggo run prd.json --ui
```

The dashboard (default: `http://localhost:8484`) shows:
- Story status overview (pending / running / passed / failed)
- Live streaming output from the current agent via SSE
- Run history and logs
- Progress visualization

## Parallel execution

Run multiple stories concurrently using git worktrees:

```sh
# Up to 3 stories in parallel
ralph-wiggo run prd.json --parallelism parallel-3

# Let Claude analyze dependencies and auto-batch
ralph-wiggo run prd.json --parallelism auto
```

Each parallel story runs in an isolated worktree. Results are merged back sequentially to avoid conflicts.

## Configuration

### CLI flags

```
--model          Claude model (default: claude-opus-4-6)
--max-turns      Max agentic turns per story (default: 50)
--max-budget     Max budget in USD per agent session
--work-dir       Working directory (default: .)
--parallelism    sequential | parallel-N | auto (default: sequential)
--max-iterations Max retry iterations per story (default: 10)
--ui             Start web dashboard alongside agent loop
```

### Config file

Create `.ralph-wiggo.yaml` in your project root:

```yaml
model: claude-opus-4-6
maxTurns: 80
maxBudget: 5.00
parallelism: parallel-2
port: 8484
allowedTools:
  - Bash
  - Read
  - Edit
  - Write
  - Glob
  - Grep
```

CLI flags override config file values.

## prd.json format

The agent loop is driven by a `prd.json` file:

```json
{
  "project": "My Project",
  "branchName": "ralph/my-feature",
  "description": "Feature description",
  "userStories": [
    {
      "id": "US-001",
      "title": "Add database schema",
      "description": "As a developer, I need the schema so that...",
      "acceptanceCriteria": [
        "Migration creates users table",
        "go vet ./... passes"
      ],
      "priority": 1,
      "passes": false,
      "notes": ""
    }
  ]
}
```

Stories execute in priority order. Each story should be small enough to complete in a single agent session. The `passes` field is updated automatically as stories are completed.

## Architecture

```
cmd/ralph-wiggo/       CLI entry point (Kong framework)
internal/
  claude/              Claude CLI executor (streaming, interactive, JSON modes)
  prd/                 PRD types, validation, JSON schema
  planner/             Story scheduling (sequential, parallel, auto)
  git/                 Git operations (branches, worktrees, merge)
  prompts/             Embedded prompt/skill file loader
  progress/            Progress tracking and run archiving
  state/               In-memory state store with SSE broadcasting
  config/              YAML config loader
  web/                 Dashboard server (htmx + SSE)
embedded/              Agent prompts and skill files
```

## License

MIT
