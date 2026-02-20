package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/kong"
	"github.com/radvoogh/ralph-wiggo/internal/claude"
	"github.com/radvoogh/ralph-wiggo/internal/config"
	"github.com/radvoogh/ralph-wiggo/internal/git"
	"github.com/radvoogh/ralph-wiggo/internal/planner"
	"github.com/radvoogh/ralph-wiggo/internal/prd"
	"github.com/radvoogh/ralph-wiggo/internal/progress"
	"github.com/radvoogh/ralph-wiggo/internal/prompts"
	"github.com/radvoogh/ralph-wiggo/internal/state"
	"github.com/radvoogh/ralph-wiggo/internal/web"
)

// CLI defines the top-level command structure for ralph-wiggo.
type CLI struct {
	Verbose  bool    `help:"Enable verbose output." short:"v"`
	Model    string  `help:"Claude model to use." default:"claude-sonnet-4-6"`
	MaxBudget float64 `help:"Maximum budget in USD per agent session." name:"max-budget"`
	MaxTurns int     `help:"Maximum agentic turns per story." default:"50" name:"max-turns"`
	WorkDir         string   `help:"Working directory." default:"." name:"work-dir" type:"existingdir"`
	PromptOverrides []string `help:"Override an embedded prompt file: name=path (e.g. prompt.md=/tmp/my-prompt.md)." name:"prompt-override"`

	Run     RunCmd     `cmd:"" help:"Run the agent loop on prd.json stories."`
	PRD     PRDCmd     `cmd:"" help:"Generate a PRD interactively with Claude."`
	Convert ConvertCmd `cmd:"" help:"Convert a PRD markdown file to prd.json."`
	Serve   ServeCmd   `cmd:"" help:"Start the web dashboard server."`
	Full    FullCmd    `cmd:"" help:"Full workflow: PRD generation, conversion, and agent loop."`

	// fileConfig holds settings loaded from .ralph-wiggo.yaml (not a CLI flag).
	fileConfig config.Config `kong:"-"`
}

// AfterApply loads the config file and registers prompt overrides before
// subcommands run. Config file values are applied as defaults; CLI flags
// that were explicitly set take precedence.
func (c *CLI) AfterApply() error {
	// Register prompt overrides.
	for _, override := range c.PromptOverrides {
		parts := strings.SplitN(override, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid --prompt-override format %q: expected name=path", override)
		}
		prompts.SetOverride(parts[0], parts[1])
	}

	// Load config file from working directory.
	cfg, err := config.Load(c.WorkDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading %s: %v\n", config.DefaultConfigFile, err)
		return nil
	}
	c.fileConfig = cfg

	// Apply config file values where CLI flags are still at their defaults.
	if cfg.Model != "" && c.Model == "claude-sonnet-4-6" {
		c.Model = cfg.Model
	}
	if cfg.MaxBudget != 0 && c.MaxBudget == 0 {
		c.MaxBudget = cfg.MaxBudget
	}
	if cfg.MaxTurns != 0 && c.MaxTurns == 50 {
		c.MaxTurns = cfg.MaxTurns
	}

	return nil
}

// RunCmd implements the 'run' subcommand.
type RunCmd struct {
	PRDPath       string `help:"Path to prd.json." default:"prd.json" name:"prd"`
	Parallelism   string `help:"Parallelism mode: sequential, parallel-N, or auto." default:"sequential"`
	MaxIterations int    `help:"Maximum iterations per story before skipping." default:"10" name:"max-iterations"`
	UI            bool   `help:"Start web dashboard alongside the agent loop."`
	DryRun        bool   `help:"Print what would be executed without invoking Claude." name:"dry-run"`
}

func (r *RunCmd) Run(globals *CLI) error {
	// Apply config file overrides for subcommand-specific settings.
	cfg := globals.fileConfig
	if cfg.Parallelism != "" && r.Parallelism == "sequential" {
		r.Parallelism = cfg.Parallelism
	}

	progressPath := filepath.Join(filepath.Dir(r.PRDPath), "progress.txt")

	// Load the PRD.
	p, err := prd.LoadPRD(r.PRDPath)
	if err != nil {
		return fmt.Errorf("loading PRD: %w", err)
	}

	// Archive existing progress.txt (and prd.json snapshot) if the branch changed.
	archived, err := progress.ArchiveIfBranchChanged(
		globals.WorkDir, r.PRDPath, progressPath, p.BranchName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: archiving previous run: %v\n", err)
	}
	if archived {
		fmt.Println("Archived previous run (branch changed).")
	}

	fmt.Printf("Loaded PRD: %s (%d stories)\n", p.Project, len(p.UserStories))

	// Dry-run mode: print what would be executed without invoking Claude.
	if r.DryRun {
		fmt.Println("\n[dry-run] Would execute with the following settings:")
		fmt.Printf("  Branch:      %s\n", p.BranchName)
		fmt.Printf("  Model:       %s\n", globals.Model)
		fmt.Printf("  Max turns:   %d\n", globals.MaxTurns)
		if globals.MaxBudget > 0 {
			fmt.Printf("  Max budget:  $%.2f\n", globals.MaxBudget)
		}
		fmt.Printf("  Parallelism: %s\n", r.Parallelism)
		fmt.Printf("  Max iters:   %d\n", r.MaxIterations)
		fmt.Println("\n[dry-run] Stories to execute:")
		for _, s := range p.UserStories {
			status := "pending"
			if s.Passes {
				status = "passed"
			}
			fmt.Printf("  %s [%s] %s\n", s.ID, status, s.Title)
		}
		return nil
	}

	// Create or check out the feature branch.
	if err := git.CreateOrCheckoutBranch(p.BranchName); err != nil {
		return fmt.Errorf("switching to branch %q: %w", p.BranchName, err)
	}
	fmt.Printf("On branch: %s\n", p.BranchName)

	// Initialize progress.txt with header if it doesn't exist.
	if err := progress.InitIfNeeded(progressPath, p.Project, p.BranchName); err != nil {
		fmt.Fprintf(os.Stderr, "warning: initializing progress.txt: %v\n", err)
	}

	// Load the embedded agent prompt for --append-system-prompt.
	agentPrompt, err := prompts.Get("prompt.md")
	if err != nil {
		return fmt.Errorf("loading prompt.md: %w", err)
	}

	// Determine allowed tools (config file override or default).
	allowedTools := []string{"Bash", "Read", "Edit", "Write", "Glob", "Grep"}
	if len(globals.fileConfig.AllowedTools) > 0 {
		allowedTools = globals.fileConfig.AllowedTools
	}

	// Create state store for event tracking and persistence.
	storeDir := filepath.Join(globals.WorkDir, ".ralph-wiggo", "runs")
	store, err := state.NewMemoryStore(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: state store: %v\n", err)
		store = nil
	}

	// Create a new run in the state store.
	runID := fmt.Sprintf("run-%d", time.Now().Unix())
	if store != nil {
		run := &state.Run{
			ID:         runID,
			PRDPath:    r.PRDPath,
			BranchName: p.BranchName,
			StartTime:  time.Now(),
			Status:     state.StatusRunning,
		}
		if err := store.SaveRun(run); err != nil {
			fmt.Fprintf(os.Stderr, "warning: saving initial run state: %v\n", err)
		}
	}

	// Start web dashboard if --ui flag is set.
	if r.UI {
		uiPort := 8484
		if globals.fileConfig.Port != 0 {
			uiPort = globals.fileConfig.Port
		}
		srv, err := web.NewServer(r.PRDPath, uiPort, store)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: starting web dashboard: %v\n", err)
		} else {
			if err := srv.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: web dashboard: %v\n", err)
			}
			defer srv.Shutdown(context.Background())
		}
	}

	exec := claude.NewExecutor()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Per-story iteration tracking.
	storyIterations := make(map[string]int)
	skippedStories := make(map[string]bool)

	for {
		// Get next stories to work on.
		stories, err := planner.NextStories(ctx, p, r.Parallelism, exec)
		if err != nil {
			return fmt.Errorf("planner: %w", err)
		}

		// Filter out stories that have exceeded max iterations.
		var eligible []*prd.UserStory
		for _, s := range stories {
			if !skippedStories[s.ID] {
				eligible = append(eligible, s)
			}
		}
		if len(eligible) == 0 {
			if len(stories) > 0 {
				fmt.Println("\nRemaining stories skipped (exceeded max iterations).")
			} else {
				fmt.Println("\nAll stories pass!")
			}
			break
		}

		if len(eligible) == 1 {
			// Sequential execution — run a single agent inline.
			story := eligible[0]
			storyIterations[story.ID]++
			iterNum := storyIterations[story.ID]
			fmt.Printf("\n--- %s - %s (iteration %d/%d) ---\n", story.ID, story.Title, iterNum, r.MaxIterations)

			result := runSingleAgent(ctx, exec, story, agentPrompt, globals, r.PRDPath, store, allowedTools)

			p, err = processStoryResult(result, r.PRDPath, progressPath, p, iterNum, r.MaxIterations, storyIterations, skippedStories, store, runID)
			if err != nil {
				return err
			}
		} else {
			// Parallel execution — run agents in separate worktrees.
			results := runParallelAgents(ctx, exec, eligible, agentPrompt, globals, r.PRDPath, storyIterations, r.MaxIterations, store, allowedTools)

			p, err = processParallelResults(results, r.PRDPath, progressPath, p, r.MaxIterations, storyIterations, skippedStories, store, runID)
			if err != nil {
				return err
			}
		}
	}

	// Print final summary.
	passed, total := 0, len(p.UserStories)
	for _, s := range p.UserStories {
		if s.Passes {
			passed++
		}
	}
	fmt.Printf("\nSummary: %d/%d stories passed\n", passed, total)
	if len(skippedStories) > 0 {
		fmt.Printf("Skipped stories: ")
		first := true
		for id := range skippedStories {
			if !first {
				fmt.Print(", ")
			}
			fmt.Print(id)
			first = false
		}
		fmt.Println()
	}

	return nil
}

// buildStoryPrompt constructs the prompt sent to the Claude agent for a story.
func buildStoryPrompt(s *prd.UserStory) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Implement the following user story:\n\n"))
	sb.WriteString(fmt.Sprintf("**ID:** %s\n", s.ID))
	sb.WriteString(fmt.Sprintf("**Title:** %s\n", s.Title))
	sb.WriteString(fmt.Sprintf("**Description:** %s\n\n", s.Description))
	sb.WriteString("**Acceptance Criteria:**\n")
	for _, ac := range s.AcceptanceCriteria {
		sb.WriteString(fmt.Sprintf("- %s\n", ac))
	}
	if s.Notes != "" {
		sb.WriteString(fmt.Sprintf("\n**Notes:** %s\n", s.Notes))
	}
	return sb.String()
}

// printStreamEvent prints a streaming event from the Claude agent to stdout.
func printStreamEvent(evt claude.StreamEvent) {
	switch evt.Type {
	case claude.EventAssistant:
		if evt.Message != "" {
			fmt.Print(evt.Message)
		}
	case claude.EventToolUse:
		fmt.Printf("\n[tool: %s]\n", evt.ToolName)
	case claude.EventToolResult:
		// Tool results can be large; print a brief indicator.
		fmt.Println("[tool result]")
	case claude.EventError:
		fmt.Fprintf(os.Stderr, "[error] %s\n", evt.Message)
	case claude.EventInit:
		if evt.SessionID != "" {
			fmt.Printf("[session: %s]\n", evt.SessionID)
		}
	case claude.EventResult:
		fmt.Println("\n[agent finished]")
	}
}

// storyResult holds the outcome of a single agent execution.
type storyResult struct {
	storyID    string
	storyTitle string
	passed     bool
	events     []claude.StreamEvent
	// For parallel execution — the worktree branch that needs merging.
	worktreeBranch string
	worktreePath   string
	iterNum        int
}

// runSingleAgent runs a Claude agent for a single story in the current working
// directory and returns the result. Events are published to the store for SSE.
func runSingleAgent(ctx context.Context, exec *claude.Executor, story *prd.UserStory, agentPrompt string, globals *CLI, prdPath string, store *state.MemoryStore, allowedTools []string) storyResult {
	storyPrompt := buildStoryPrompt(story)

	cfg := claude.RunConfig{
		Prompt:             storyPrompt,
		Model:              globals.Model,
		MaxTurns:           globals.MaxTurns,
		MaxBudgetUSD:       globals.MaxBudget,
		WorkDir:            globals.WorkDir,
		AppendSystemPrompt: agentPrompt,
		AllowedTools:       allowedTools,
		AdditionalFlags:    []string{"--dangerously-skip-permissions"},
	}

	if store != nil {
		store.ResetBroadcast(story.ID)
	}

	events, err := exec.RunStreaming(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error starting agent for %s: %v\n", story.ID, err)
		return storyResult{storyID: story.ID, storyTitle: story.Title, passed: false}
	}

	exitedCleanly := true
	var collectedEvents []claude.StreamEvent
	for evt := range events {
		printStreamEvent(evt)
		collectedEvents = append(collectedEvents, evt)
		if store != nil {
			store.PublishEvent(story.ID, evt)
		}
		if evt.Type == claude.EventError {
			exitedCleanly = false
		}
	}

	if store != nil {
		store.CloseSubscribers(story.ID)
	}

	return storyResult{
		storyID:    story.ID,
		storyTitle: story.Title,
		passed:     exitedCleanly,
		events:     collectedEvents,
	}
}

// processStoryResult handles the result of a single story execution: updates
// PRD, appends progress, persists iteration to state store, and commits if
// passed. Returns the reloaded PRD.
func processStoryResult(result storyResult, prdPath, progressPath string, p *prd.PRD, iterNum, maxIterations int, storyIterations map[string]int, skippedStories map[string]bool, store *state.MemoryStore, runID string) (*prd.PRD, error) {
	// Reload PRD to pick up any changes the agent may have made.
	p, err := prd.LoadPRD(prdPath)
	if err != nil {
		return nil, fmt.Errorf("reloading PRD after iteration: %w", err)
	}

	// Persist iteration to state store.
	if store != nil {
		iterStatus := state.StatusFailed
		if result.passed {
			iterStatus = state.StatusPassed
		}
		iter := state.Iteration{
			RunID:   runID,
			StoryID: result.storyID,
			Number:  iterNum,
			EndTime: time.Now(),
			Status:  iterStatus,
			Events:  result.events,
		}
		if err := store.AddIteration(runID, iter); err != nil {
			fmt.Fprintf(os.Stderr, "warning: saving iteration: %v\n", err)
		}
	}

	if result.passed {
		for i := range p.UserStories {
			if p.UserStories[i].ID == result.storyID {
				p.UserStories[i].Passes = true
				break
			}
		}
		if err := prd.SavePRD(prdPath, p); err != nil {
			return nil, fmt.Errorf("saving PRD after %s passed: %w", result.storyID, err)
		}
		if err := progress.AppendEntry(progressPath, result.storyID, true, result.events); err != nil {
			fmt.Fprintf(os.Stderr, "warning: updating progress.txt: %v\n", err)
		}
		commitMsg := fmt.Sprintf("ralph-wiggo: %s %s [passed]", result.storyID, result.storyTitle)
		if err := git.CommitAll(commitMsg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: commit failed for %s: %v\n", result.storyID, err)
		}
		fmt.Printf("[%s] PASS (iteration %d/%d)\n", result.storyID, iterNum, maxIterations)
	} else {
		if err := progress.AppendEntry(progressPath, result.storyID, false, result.events); err != nil {
			fmt.Fprintf(os.Stderr, "warning: updating progress.txt: %v\n", err)
		}
		fmt.Printf("[%s] FAIL (iteration %d/%d)\n", result.storyID, iterNum, maxIterations)
		if iterNum >= maxIterations {
			skippedStories[result.storyID] = true
			fmt.Printf("[%s] Skipping — exceeded max iterations (%d)\n", result.storyID, maxIterations)
		}
	}
	return p, nil
}

// runParallelAgents runs Claude agents concurrently in separate git worktrees,
// one per story. Returns all results after all agents complete.
func runParallelAgents(ctx context.Context, exec *claude.Executor, stories []*prd.UserStory, agentPrompt string, globals *CLI, prdPath string, storyIterations map[string]int, maxIterations int, store *state.MemoryStore, allowedTools []string) []storyResult {
	worktreeBase := filepath.Join(globals.WorkDir, ".ralph-wiggo", "worktrees")

	fmt.Printf("\n=== Parallel batch: %d stories ===\n", len(stories))
	for _, s := range stories {
		fmt.Printf("  - %s: %s\n", s.ID, s.Title)
	}

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results []storyResult
	)

	// Ensure the worktree base directory exists.
	if err := os.MkdirAll(worktreeBase, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error creating worktree directory: %v\n", err)
		// Fall back to sequential-style result with all failures.
		for _, s := range stories {
			results = append(results, storyResult{storyID: s.ID, storyTitle: s.Title, passed: false})
		}
		return results
	}

	// Track all worktree paths for cleanup on context cancellation.
	type worktreeInfo struct {
		path   string
		branch string
	}
	var (
		wtMu      sync.Mutex
		worktrees []worktreeInfo
	)

	for _, story := range stories {
		storyIterations[story.ID]++
		iterNum := storyIterations[story.ID]

		wtPath := filepath.Join(worktreeBase, story.ID)
		wtBranch := fmt.Sprintf("worktree-%s", story.ID)

		fmt.Printf("\n--- %s - %s (iteration %d/%d) [parallel] ---\n", story.ID, story.Title, iterNum, maxIterations)

		// Create worktree for this story.
		if err := git.WorktreeAdd(wtPath, wtBranch); err != nil {
			fmt.Fprintf(os.Stderr, "error creating worktree for %s: %v\n", story.ID, err)
			mu.Lock()
			results = append(results, storyResult{
				storyID: story.ID, storyTitle: story.Title, passed: false,
				iterNum: iterNum,
			})
			mu.Unlock()
			continue
		}

		wtMu.Lock()
		worktrees = append(worktrees, worktreeInfo{path: wtPath, branch: wtBranch})
		wtMu.Unlock()

		wg.Add(1)
		go func(s *prd.UserStory, wtDir, branch string, iter int) {
			defer wg.Done()

			if store != nil {
				store.ResetBroadcast(s.ID)
			}

			storyPrompt := buildStoryPrompt(s)
			cfg := claude.RunConfig{
				Prompt:             storyPrompt,
				Model:              globals.Model,
				MaxTurns:           globals.MaxTurns,
				MaxBudgetUSD:       globals.MaxBudget,
				WorkDir:            wtDir,
				AppendSystemPrompt: agentPrompt,
				AllowedTools:       allowedTools,
				AdditionalFlags:    []string{"--dangerously-skip-permissions"},
			}

			events, err := exec.RunStreaming(ctx, cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error starting agent for %s: %v\n", s.ID, err)
				mu.Lock()
				results = append(results, storyResult{
					storyID: s.ID, storyTitle: s.Title, passed: false,
					worktreeBranch: branch, worktreePath: wtDir, iterNum: iter,
				})
				mu.Unlock()
				return
			}

			exitedCleanly := true
			var collectedEvents []claude.StreamEvent
			for evt := range events {
				// In parallel mode, prefix output with story ID for clarity.
				printParallelEvent(s.ID, evt)
				collectedEvents = append(collectedEvents, evt)
				if store != nil {
					store.PublishEvent(s.ID, evt)
				}
				if evt.Type == claude.EventError {
					exitedCleanly = false
				}
			}

			if store != nil {
				store.CloseSubscribers(s.ID)
			}

			mu.Lock()
			results = append(results, storyResult{
				storyID:        s.ID,
				storyTitle:     s.Title,
				passed:         exitedCleanly,
				events:         collectedEvents,
				worktreeBranch: branch,
				worktreePath:   wtDir,
				iterNum:        iter,
			})
			mu.Unlock()
		}(story, wtPath, wtBranch, iterNum)
	}

	// Wait for all agents to complete.
	wg.Wait()

	// Cleanup worktrees (always, regardless of success/failure).
	defer func() {
		for _, wt := range worktrees {
			if err := git.WorktreeRemove(wt.path); err != nil {
				fmt.Fprintf(os.Stderr, "warning: removing worktree %s: %v\n", wt.path, err)
			}
			if err := git.DeleteBranch(wt.branch); err != nil {
				fmt.Fprintf(os.Stderr, "warning: deleting branch %s: %v\n", wt.branch, err)
			}
		}
		// Clean up the worktree base directory if empty.
		_ = os.Remove(worktreeBase)
	}()

	return results
}

// processParallelResults handles the results of parallel story executions:
// merges worktree branches, updates PRD, appends progress, persists iterations,
// and commits.
func processParallelResults(results []storyResult, prdPath, progressPath string, p *prd.PRD, maxIterations int, storyIterations map[string]int, skippedStories map[string]bool, store *state.MemoryStore, runID string) (*prd.PRD, error) {
	for _, result := range results {
		if result.passed && result.worktreeBranch != "" {
			// Merge the worktree branch into the current branch.
			if err := git.MergeFrom(result.worktreeBranch); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] merge conflict — marking as failed: %v\n", result.storyID, err)
				// Abort the merge to restore working tree.
				_ = git.AbortMerge()
				result.passed = false
			}
		}

		// Reload PRD after merge to pick up changes.
		var err error
		p, err = prd.LoadPRD(prdPath)
		if err != nil {
			return nil, fmt.Errorf("reloading PRD after %s: %w", result.storyID, err)
		}

		// Persist iteration to state store.
		if store != nil {
			iterStatus := state.StatusFailed
			if result.passed {
				iterStatus = state.StatusPassed
			}
			iter := state.Iteration{
				RunID:   runID,
				StoryID: result.storyID,
				Number:  result.iterNum,
				EndTime: time.Now(),
				Status:  iterStatus,
				Events:  result.events,
			}
			if err := store.AddIteration(runID, iter); err != nil {
				fmt.Fprintf(os.Stderr, "warning: saving iteration: %v\n", err)
			}
		}

		if result.passed {
			for i := range p.UserStories {
				if p.UserStories[i].ID == result.storyID {
					p.UserStories[i].Passes = true
					break
				}
			}
			if err := prd.SavePRD(prdPath, p); err != nil {
				return nil, fmt.Errorf("saving PRD after %s passed: %w", result.storyID, err)
			}
			if err := progress.AppendEntry(progressPath, result.storyID, true, result.events); err != nil {
				fmt.Fprintf(os.Stderr, "warning: updating progress.txt: %v\n", err)
			}
			commitMsg := fmt.Sprintf("ralph-wiggo: %s %s [passed]", result.storyID, result.storyTitle)
			if err := git.CommitAll(commitMsg); err != nil {
				fmt.Fprintf(os.Stderr, "warning: commit failed for %s: %v\n", result.storyID, err)
			}
			fmt.Printf("[%s] PASS (iteration %d/%d)\n", result.storyID, result.iterNum, maxIterations)
		} else {
			if err := progress.AppendEntry(progressPath, result.storyID, false, result.events); err != nil {
				fmt.Fprintf(os.Stderr, "warning: updating progress.txt: %v\n", err)
			}
			fmt.Printf("[%s] FAIL (iteration %d/%d)\n", result.storyID, result.iterNum, maxIterations)
			if result.iterNum >= maxIterations {
				skippedStories[result.storyID] = true
				fmt.Printf("[%s] Skipping — exceeded max iterations (%d)\n", result.storyID, maxIterations)
			}
		}
	}
	return p, nil
}

// printParallelEvent prints a streaming event prefixed with the story ID.
func printParallelEvent(storyID string, evt claude.StreamEvent) {
	prefix := fmt.Sprintf("[%s] ", storyID)
	switch evt.Type {
	case claude.EventAssistant:
		if evt.Message != "" {
			fmt.Printf("%s%s", prefix, evt.Message)
		}
	case claude.EventToolUse:
		fmt.Printf("%s[tool: %s]\n", prefix, evt.ToolName)
	case claude.EventToolResult:
		fmt.Printf("%s[tool result]\n", prefix)
	case claude.EventError:
		fmt.Fprintf(os.Stderr, "%s[error] %s\n", prefix, evt.Message)
	case claude.EventInit:
		if evt.SessionID != "" {
			fmt.Printf("%s[session: %s]\n", prefix, evt.SessionID)
		}
	case claude.EventResult:
		fmt.Printf("%s[agent finished]\n", prefix)
	}
}

// PRDCmd implements the 'prd' subcommand.
type PRDCmd struct {
	Description string `arg:"" help:"Feature description for PRD generation."`
	Output      string `help:"Output path for generated PRD." default:""`
}

func (p *PRDCmd) Run(globals *CLI) error {
	skillContent, err := prompts.Get("prd-skill.md")
	if err != nil {
		return fmt.Errorf("loading prd-skill.md: %w", err)
	}

	// Determine output path. Default to tasks/prd-<feature>.md using a
	// kebab-case slug derived from the description.
	outputPath := p.Output
	if outputPath == "" {
		outputPath = "tasks/prd-" + slugify(p.Description) + ".md"
	}

	// Build a prompt that includes the description and output location.
	prompt := fmt.Sprintf(
		"Generate a PRD for the following feature:\n\n%s\n\nSave the PRD to: %s",
		p.Description, outputPath,
	)

	exec := claude.NewExecutor()
	cfg := claude.RunConfig{
		Prompt:             prompt,
		Model:              globals.Model,
		MaxTurns:           globals.MaxTurns,
		MaxBudgetUSD:       globals.MaxBudget,
		WorkDir:            globals.WorkDir,
		AppendSystemPrompt: skillContent,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := exec.RunInteractive(ctx, cfg); err != nil {
		return fmt.Errorf("prd generation: %w", err)
	}
	return nil
}

// ConvertCmd implements the 'convert' subcommand.
type ConvertCmd struct {
	PRDFile string `arg:"" help:"Path to PRD markdown file to convert."`
	Output  string `help:"Output path for prd.json." default:"prd.json"`
}

func (c *ConvertCmd) Run(globals *CLI) error {
	// Load the ralph-skill.md content for the system prompt.
	skillContent, err := prompts.Get("ralph-skill.md")
	if err != nil {
		return fmt.Errorf("loading ralph-skill.md: %w", err)
	}

	// Read the PRD markdown file.
	prdContent, err := os.ReadFile(c.PRDFile)
	if err != nil {
		return fmt.Errorf("reading PRD file %q: %w", c.PRDFile, err)
	}

	// Build a prompt with the PRD content.
	prompt := fmt.Sprintf(
		"Convert the following PRD markdown into the prd.json format.\n\n%s",
		string(prdContent),
	)

	exec := claude.NewExecutor()
	cfg := claude.RunConfig{
		Prompt:             prompt,
		Model:              globals.Model,
		MaxTurns:           globals.MaxTurns,
		MaxBudgetUSD:       globals.MaxBudget,
		WorkDir:            globals.WorkDir,
		AppendSystemPrompt: skillContent,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	result, err := exec.RunJSON(ctx, cfg, prd.JSONSchema)
	if err != nil {
		return fmt.Errorf("prd conversion: %w", err)
	}

	// Parse the result into a PRD struct to validate it.
	var parsedPRD prd.PRD
	if err := json.Unmarshal(result, &parsedPRD); err != nil {
		return fmt.Errorf("parsing conversion result: %w", err)
	}

	// Validate and print warnings (but don't fail).
	if err := prd.Validate(&parsedPRD); err != nil {
		fmt.Fprintf(os.Stderr, "warning: validation issue: %v\n", err)
	}

	// Save the validated PRD.
	if err := prd.SavePRD(c.Output, &parsedPRD); err != nil {
		return fmt.Errorf("saving prd.json: %w", err)
	}

	fmt.Printf("Wrote %s (%d stories)\n", c.Output, len(parsedPRD.UserStories))
	return nil
}

// ServeCmd implements the 'serve' subcommand.
type ServeCmd struct {
	Port    int    `help:"Port for the web dashboard." default:"8484"`
	PRDPath string `help:"Path to prd.json." default:"prd.json" name:"prd"`
}

func (s *ServeCmd) Run(globals *CLI) error {
	// Apply config file override for port.
	if globals.fileConfig.Port != 0 && s.Port == 8484 {
		s.Port = globals.fileConfig.Port
	}

	// Load state store from disk for historical event data.
	storeDir := filepath.Join(globals.WorkDir, ".ralph-wiggo", "runs")
	store, err := state.NewMemoryStore(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading state: %v\n", err)
		store = nil
	}

	srv, err := web.NewServer(s.PRDPath, s.Port, store)
	if err != nil {
		return fmt.Errorf("starting web server: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	return srv.ListenAndServe()
}

// FullCmd implements the 'full' subcommand.
type FullCmd struct {
	// PRD generation flags.
	Description string `arg:"" help:"Feature description for the full workflow."`
	Output      string `help:"Output path for generated PRD markdown." default:""`

	// Convert flags.
	JSONOutput string `help:"Output path for prd.json." default:"prd.json" name:"json-output"`

	// Run flags.
	Parallelism   string `help:"Parallelism mode: sequential, parallel-N, or auto." default:"sequential"`
	MaxIterations int    `help:"Maximum iterations per story before skipping." default:"10" name:"max-iterations"`
	UI            bool   `help:"Start web dashboard during the run phase."`
}

func (f *FullCmd) Run(globals *CLI) error {
	// Step 1: PRD generation (interactive).
	prdOutputPath := f.Output
	if prdOutputPath == "" {
		prdOutputPath = "tasks/prd-" + slugify(f.Description) + ".md"
	}

	fmt.Println("=== Step 1: PRD Generation ===")
	fmt.Printf("Generating PRD for: %s\n", f.Description)
	fmt.Printf("Output: %s\n\n", prdOutputPath)

	skillContent, err := prompts.Get("prd-skill.md")
	if err != nil {
		return fmt.Errorf("loading prd-skill.md: %w", err)
	}

	exec := claude.NewExecutor()

	prompt := fmt.Sprintf(
		"Generate a PRD for the following feature:\n\n%s\n\nSave the PRD to: %s",
		f.Description, prdOutputPath,
	)

	cfg := claude.RunConfig{
		Prompt:             prompt,
		Model:              globals.Model,
		MaxTurns:           globals.MaxTurns,
		MaxBudgetUSD:       globals.MaxBudget,
		WorkDir:            globals.WorkDir,
		AppendSystemPrompt: skillContent,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := exec.RunInteractive(ctx, cfg); err != nil {
		return fmt.Errorf("prd generation: %w", err)
	}

	// Confirm before proceeding to conversion.
	fmt.Printf("\nPRD generated at: %s\n", prdOutputPath)
	if !confirmPrompt("Proceed with conversion to prd.json?") {
		fmt.Println("Aborted.")
		return nil
	}

	// Step 2: Convert PRD to prd.json.
	fmt.Println("\n=== Step 2: PRD Conversion ===")

	ralphSkill, err := prompts.Get("ralph-skill.md")
	if err != nil {
		return fmt.Errorf("loading ralph-skill.md: %w", err)
	}

	prdContent, err := os.ReadFile(prdOutputPath)
	if err != nil {
		return fmt.Errorf("reading PRD file %q: %w", prdOutputPath, err)
	}

	convertPrompt := fmt.Sprintf(
		"Convert the following PRD markdown into the prd.json format.\n\n%s",
		string(prdContent),
	)

	convertCfg := claude.RunConfig{
		Prompt:             convertPrompt,
		Model:              globals.Model,
		MaxTurns:           globals.MaxTurns,
		MaxBudgetUSD:       globals.MaxBudget,
		WorkDir:            globals.WorkDir,
		AppendSystemPrompt: ralphSkill,
	}

	result, err := exec.RunJSON(ctx, convertCfg, prd.JSONSchema)
	if err != nil {
		return fmt.Errorf("prd conversion: %w", err)
	}

	var parsedPRD prd.PRD
	if err := json.Unmarshal(result, &parsedPRD); err != nil {
		return fmt.Errorf("parsing conversion result: %w", err)
	}

	if err := prd.Validate(&parsedPRD); err != nil {
		fmt.Fprintf(os.Stderr, "warning: validation issue: %v\n", err)
	}

	if err := prd.SavePRD(f.JSONOutput, &parsedPRD); err != nil {
		return fmt.Errorf("saving prd.json: %w", err)
	}

	fmt.Printf("Wrote %s (%d stories, branch: %s)\n", f.JSONOutput, len(parsedPRD.UserStories), parsedPRD.BranchName)

	// Confirm before proceeding to run.
	if !confirmPrompt("Proceed with agent loop?") {
		fmt.Println("Aborted.")
		return nil
	}

	// Step 3: Run the agent loop.
	fmt.Println("\n=== Step 3: Agent Loop ===")
	runCmd := RunCmd{
		PRDPath:       f.JSONOutput,
		Parallelism:   f.Parallelism,
		MaxIterations: f.MaxIterations,
		UI:            f.UI,
	}
	return runCmd.Run(globals)
}

// confirmPrompt prints a y/n prompt and returns true if the user confirms.
func confirmPrompt(message string) bool {
	fmt.Printf("%s (y/n): ", message)
	var response string
	fmt.Scanln(&response)
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// slugify converts a string to a kebab-case slug suitable for filenames.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prev := '-'
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prev = r
		default:
			if prev != '-' {
				b.WriteRune('-')
				prev = '-'
			}
		}
	}
	result := b.String()
	return strings.Trim(result, "-")
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("ralph-wiggo"),
		kong.Description("Autonomous AI agent loop with web dashboard, parallel execution, and streaming output."),
		kong.UsageOnError(),
	)
	err := ctx.Run(&cli)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
