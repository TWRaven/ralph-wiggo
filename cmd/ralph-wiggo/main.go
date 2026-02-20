package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/radvoogh/ralph-wiggo/internal/claude"
	"github.com/radvoogh/ralph-wiggo/internal/git"
	"github.com/radvoogh/ralph-wiggo/internal/planner"
	"github.com/radvoogh/ralph-wiggo/internal/prd"
	"github.com/radvoogh/ralph-wiggo/internal/progress"
	"github.com/radvoogh/ralph-wiggo/internal/prompts"
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
}

// AfterApply registers any prompt overrides before subcommands run.
func (c *CLI) AfterApply() error {
	for _, override := range c.PromptOverrides {
		parts := strings.SplitN(override, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid --prompt-override format %q: expected name=path", override)
		}
		prompts.SetOverride(parts[0], parts[1])
	}
	return nil
}

// RunCmd implements the 'run' subcommand.
type RunCmd struct {
	PRDPath       string `help:"Path to prd.json." default:"prd.json" name:"prd"`
	Parallelism   string `help:"Parallelism mode: sequential, parallel-N, or auto." default:"sequential"`
	MaxIterations int    `help:"Maximum iterations per story before skipping." default:"10" name:"max-iterations"`
	UI            bool   `help:"Start web dashboard alongside the agent loop."`
}

func (r *RunCmd) Run(globals *CLI) error {
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

		// In sequential mode, there is exactly one story.
		story := eligible[0]
		storyIterations[story.ID]++
		iterNum := storyIterations[story.ID]
		fmt.Printf("\n--- %s - %s (iteration %d/%d) ---\n", story.ID, story.Title, iterNum, r.MaxIterations)

		// Build the story prompt.
		storyPrompt := buildStoryPrompt(story)

		cfg := claude.RunConfig{
			Prompt:             storyPrompt,
			Model:              globals.Model,
			MaxTurns:           globals.MaxTurns,
			MaxBudgetUSD:       globals.MaxBudget,
			WorkDir:            globals.WorkDir,
			AppendSystemPrompt: agentPrompt,
			AllowedTools:       []string{"Bash", "Read", "Edit", "Write", "Glob", "Grep"},
			AdditionalFlags:    []string{"--dangerously-skip-permissions"},
		}

		events, err := exec.RunStreaming(ctx, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error starting agent for %s: %v\n", story.ID, err)
			fmt.Printf("[%s] FAIL (iteration %d/%d) — could not start agent\n", story.ID, iterNum, r.MaxIterations)
			if iterNum >= r.MaxIterations {
				skippedStories[story.ID] = true
				fmt.Printf("[%s] Skipping — exceeded max iterations (%d)\n", story.ID, r.MaxIterations)
			}
			continue
		}

		// Print streaming events as they arrive and collect them for progress.
		exitedCleanly := true
		var collectedEvents []claude.StreamEvent
		for evt := range events {
			printStreamEvent(evt)
			collectedEvents = append(collectedEvents, evt)
			if evt.Type == claude.EventError {
				exitedCleanly = false
			}
		}

		// Capture story info before reloading PRD (the pointer becomes stale).
		storyID := story.ID
		storyTitle := story.Title

		// Reload PRD to pick up any changes the agent may have made.
		p, err = prd.LoadPRD(r.PRDPath)
		if err != nil {
			return fmt.Errorf("reloading PRD after iteration: %w", err)
		}

		if exitedCleanly {
			// Agent exited with code 0: find the story in the reloaded PRD and mark as passed.
			for i := range p.UserStories {
				if p.UserStories[i].ID == storyID {
					p.UserStories[i].Passes = true
					break
				}
			}
			if err := prd.SavePRD(r.PRDPath, p); err != nil {
				return fmt.Errorf("saving PRD after %s passed: %w", storyID, err)
			}

			// Append progress entry before committing.
			if err := progress.AppendEntry(progressPath, storyID, true, collectedEvents); err != nil {
				fmt.Fprintf(os.Stderr, "warning: updating progress.txt: %v\n", err)
			}

			// Commit all changes including the prd.json update.
			commitMsg := fmt.Sprintf("ralph-wiggo: %s %s [passed]", storyID, storyTitle)
			if err := git.CommitAll(commitMsg); err != nil {
				fmt.Fprintf(os.Stderr, "warning: commit failed for %s: %v\n", storyID, err)
			}

			fmt.Printf("[%s] PASS (iteration %d/%d)\n", storyID, iterNum, r.MaxIterations)
		} else {
			// Agent exited with non-zero: leave passes = false.
			// Append progress entry for the failure.
			if err := progress.AppendEntry(progressPath, storyID, false, collectedEvents); err != nil {
				fmt.Fprintf(os.Stderr, "warning: updating progress.txt: %v\n", err)
			}

			fmt.Printf("[%s] FAIL (iteration %d/%d)\n", storyID, iterNum, r.MaxIterations)
			if iterNum >= r.MaxIterations {
				skippedStories[storyID] = true
				fmt.Printf("[%s] Skipping — exceeded max iterations (%d)\n", storyID, r.MaxIterations)
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
	Port int `help:"Port for the web dashboard." default:"8484"`
}

func (s *ServeCmd) Run(globals *CLI) error {
	fmt.Println("serve: not yet implemented")
	return nil
}

// FullCmd implements the 'full' subcommand.
type FullCmd struct {
	Description string `arg:"" help:"Feature description for the full workflow."`
	Output      string `help:"Output path for generated PRD." default:""`
	UI          bool   `help:"Start web dashboard during the run phase."`
}

func (f *FullCmd) Run(globals *CLI) error {
	fmt.Println("full: not yet implemented")
	return nil
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
