# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

ralph-wiggo — A Go port of the [Ralph](https://github.com/snarktank/ralph) autonomous AI agent loop. It reads a PRD (prd.json), plans story execution order, spawns Claude CLI agents to implement each story, tracks progress, and provides a real-time web dashboard. Supports sequential and parallel execution via git worktrees.

## Dev Environment

This project uses [Devbox](https://www.jetbox.io/devbox) to manage the Go toolchain. The `devbox.json` pins Go 1.26 and `.envrc` integrates with direnv for automatic shell activation.

- **Setup**: `devbox install` (one-time)
- **Shell**: `devbox shell` or use direnv (`direnv allow`)

## Build & Test Commands

- **Build**: `go build ./...`
- **Vet**: `go vet ./...`
- **Run tests**: `go test ./...`
- **Run single package tests**: `go test ./internal/prd/...`
- **Build binary**: `go build -o ralph-wiggo ./cmd/ralph-wiggo`
- **Install to GOPATH**: `go install ./cmd/ralph-wiggo`

## Architecture

### Entry Point
- `cmd/ralph-wiggo/main.go` — CLI entry point using [Kong](https://github.com/alecthomas/kong) framework. Subcommands are struct fields on the `CLI` type, each with a `Run(globals *CLI) error` method.

### Internal Packages
- `internal/claude` — Executor wrapping the `claude` CLI. Three modes: `RunStreaming` (NDJSON streaming), `RunInteractive` (terminal passthrough), `RunJSON` (structured output with `--json-schema`).
- `internal/prd` — PRD and UserStory types with JSON serialization, validation, and `JSONSchema` constant for Claude structured output.
- `internal/planner` — Story scheduling. Modes: `sequential` (one at a time), `parallel-N` (up to N concurrent), `auto` (Claude dependency analysis). Returns `[]*prd.UserStory` pointers for in-place mutation.
- `internal/git` — Git operations via `os/exec`: branch management, worktree add/remove, commit, merge.
- `internal/prompts` — Reads embedded prompt/skill files via `prompts.Get(name)`. Supports runtime overrides via `SetOverride(name, path)`.
- `internal/progress` — Progress.txt management and run archiving. Tracks iterations and archives old runs when the branch changes.
- `internal/state` — `MemoryStore` (implements `RunStore`) — in-memory map with `sync.RWMutex`, persisted as JSON in `.ralph-wiggo/runs/`. Supports SSE broadcasting via `PublishEvent`/`Subscribe`.
- `internal/config` — YAML config loader for `.ralph-wiggo.yaml`. CLI flags override config values (heuristic: if flag == default, config wins).
- `internal/web` — HTTP server with embedded templates (go:embed) and htmx. Dashboard polls every 2s, story detail uses SSE for real-time streaming.

### Embedded Assets
- `embedded/` — Contains `prompt.md`, `prd-skill.md`, `ralph-skill.md` with `go:embed` in `embedded/embed.go`. Imported by `internal/prompts`.
- `internal/web/templates/` — HTML templates (dashboard, stories, story detail, history views).
- `internal/web/static/` — CSS, htmx.min.js, sse.js.

## CLI Subcommands

- `ralph-wiggo run <prd.json>` — Main agent loop. Reads PRD, creates feature branch, iterates through stories with Claude agents.
- `ralph-wiggo prd <description>` — Generate a PRD markdown file via interactive Claude session.
- `ralph-wiggo convert <prd.md>` — Convert markdown PRD to prd.json via Claude with JSON schema.
- `ralph-wiggo serve <prd.json>` — Start the web dashboard standalone.
- `ralph-wiggo full <description>` — End-to-end: PRD generation → conversion → agent loop with confirmation prompts.

## Key Patterns

- **Kong CLI**: Subcommands as embedded structs with `cmd:""` tag. Global flags on parent `CLI` struct. `AfterApply()` for pre-command setup.
- **Agent invocation**: Uses `--dangerously-skip-permissions` and `--allowedTools` (configurable via YAML). Prompt built from embedded `prompt.md` + story details.
- **Streaming**: `RunStreaming` parses NDJSON from `claude --output-format stream-json`. Events sent to channel, consumed by print helpers and state store.
- **Parallel execution**: Worktrees created at `.ralph-wiggo/worktrees/<story-id>/`, results merged back sequentially to avoid concurrent git ops.
- **PRD reload**: After each iteration, prd.json is reloaded from disk. Previous `*prd.UserStory` pointers become stale — capture IDs before reload.
- **Config precedence**: CLI flag (if non-default) > `.ralph-wiggo.yaml` > hardcoded default.

## Git Workflow

- Use `zsh` as the shell
- Create branches from the previous branch using `gb <branch-name>`
- Use `gsn` to rebase all branches and create merge requests
- Use `glab` for GitLab interaction
