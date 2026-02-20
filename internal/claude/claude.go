// Package claude provides an interface for invoking the Claude CLI and parsing
// its streaming JSON output.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
)

// RunConfig holds all configuration for a single Claude CLI invocation.
type RunConfig struct {
	Prompt             string
	AllowedTools       []string
	MaxTurns           int
	MaxBudgetUSD       float64
	Model              string
	WorkDir            string
	SystemPrompt       string
	AppendSystemPrompt string
	ResumeSessionID    string
	AdditionalFlags    []string
}

// EventType represents the type of a streaming event from the Claude CLI.
type EventType string

const (
	EventInit       EventType = "init"
	EventAssistant  EventType = "assistant"
	EventToolUse    EventType = "tool_use"
	EventToolResult EventType = "tool_result"
	EventError      EventType = "error"
	EventResult     EventType = "result"
	EventSystem     EventType = "system"
)

// StreamEvent represents a single NDJSON event from claude --output-format stream-json.
type StreamEvent struct {
	Type      EventType       `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Message   string          `json:"message,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	ToolID    string          `json:"tool_id,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Raw       json.RawMessage `json:"-"`
}

// Executor shells out to the Claude CLI.
type Executor struct {
	// ClaudePath is the path to the claude binary. Defaults to "claude".
	ClaudePath string
}

// NewExecutor creates an Executor with default settings.
func NewExecutor() *Executor {
	return &Executor{ClaudePath: "claude"}
}

// buildArgs constructs the CLI arguments for a streaming invocation.
func (e *Executor) buildStreamingArgs(cfg RunConfig) []string {
	args := []string{
		"-p", cfg.Prompt,
		"--output-format", "stream-json",
		"--verbose",
	}

	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(cfg.MaxTurns))
	}
	if cfg.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget", strconv.FormatFloat(cfg.MaxBudgetUSD, 'f', -1, 64))
	}
	if cfg.SystemPrompt != "" {
		args = append(args, "--system-prompt", cfg.SystemPrompt)
	}
	if cfg.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", cfg.AppendSystemPrompt)
	}
	if cfg.ResumeSessionID != "" {
		args = append(args, "--resume", cfg.ResumeSessionID)
	}
	if len(cfg.AllowedTools) > 0 {
		for _, tool := range cfg.AllowedTools {
			args = append(args, "--allowedTools", tool)
		}
	}
	args = append(args, cfg.AdditionalFlags...)
	return args
}

// RunStreaming spawns the claude CLI with --output-format stream-json and
// returns a channel of parsed StreamEvent values. The channel is closed when
// the subprocess exits. Context cancellation kills the subprocess.
func (e *Executor) RunStreaming(ctx context.Context, cfg RunConfig) (<-chan StreamEvent, error) {
	bin := e.ClaudePath
	if bin == "" {
		bin = "claude"
	}

	args := e.buildStreamingArgs(cfg)
	cmd := exec.CommandContext(ctx, bin, args...)

	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude: start: %w", err)
	}

	ch := make(chan StreamEvent, 64)

	go func() {
		defer close(ch)

		// Drain stderr in the background to prevent blocking.
		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				// stderr is intentionally discarded; verbose logging goes here.
			}
		}()

		scanner := bufio.NewScanner(stdout)
		// Increase buffer for potentially large JSON lines (e.g. tool outputs).
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var evt StreamEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				// Emit unparseable lines as error events.
				evt = StreamEvent{
					Type:    EventError,
					Message: fmt.Sprintf("failed to parse stream event: %s", string(line)),
				}
			}
			evt.Raw = json.RawMessage(append([]byte(nil), line...))

			select {
			case ch <- evt:
			case <-ctx.Done():
				return
			}
		}

		// Wait for process to exit.
		_ = cmd.Wait()
	}()

	return ch, nil
}
