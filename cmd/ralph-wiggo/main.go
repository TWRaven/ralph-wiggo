package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/radvoogh/ralph-wiggo/internal/claude"
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
	PRDPath     string `help:"Path to prd.json." default:"prd.json" name:"prd"`
	Parallelism string `help:"Parallelism mode: sequential, parallel-N, or auto." default:"sequential"`
	UI          bool   `help:"Start web dashboard alongside the agent loop."`
}

func (r *RunCmd) Run(globals *CLI) error {
	fmt.Println("run: not yet implemented")
	return nil
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
	fmt.Println("convert: not yet implemented")
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
