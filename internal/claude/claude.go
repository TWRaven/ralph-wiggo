// Package claude provides an interface for invoking the Claude CLI and parsing
// its streaming JSON output.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// StreamEvent represents a parsed event from claude --output-format stream-json.
// The Claude CLI emits NDJSON lines with nested message objects; this struct
// is the flattened representation consumed by callers.
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

// parseStreamLine parses a raw NDJSON line from the Claude CLI stream-json
// output into one or more StreamEvents. A single line may contain multiple
// content blocks (e.g. text + tool_use), each producing a separate event.
func parseStreamLine(line []byte) []StreamEvent {
	raw := json.RawMessage(append([]byte(nil), line...))

	// First pass: extract top-level fields with message as raw JSON.
	var top struct {
		Type      EventType       `json:"type"`
		SessionID string          `json:"session_id,omitempty"`
		Message   json.RawMessage `json:"message,omitempty"`
	}
	if err := json.Unmarshal(line, &top); err != nil {
		return []StreamEvent{{
			Type:    EventError,
			Message: fmt.Sprintf("failed to parse stream JSON: %v", err),
			Raw:     raw,
		}}
	}

	switch top.Type {
	case "assistant", "user":
		return parseMessageBlocks(top.Type, top.SessionID, top.Message, raw)

	case EventResult:
		return []StreamEvent{{Type: EventResult, SessionID: top.SessionID, Raw: raw}}

	default:
		// For init, error, system, etc. — try message as a plain string.
		var msg string
		_ = json.Unmarshal(top.Message, &msg)
		return []StreamEvent{{
			Type:      top.Type,
			SessionID: top.SessionID,
			Message:   msg,
			Raw:       raw,
		}}
	}
}

// parseMessageBlocks extracts content blocks from an assistant or user message
// envelope and returns the corresponding flattened StreamEvents.
func parseMessageBlocks(evtType EventType, sessionID string, msgRaw json.RawMessage, raw json.RawMessage) []StreamEvent {
	var msg struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text,omitempty"`
			Name      string          `json:"name,omitempty"`
			ID        string          `json:"id,omitempty"`
			ToolUseID string          `json:"tool_use_id,omitempty"`
			Input     json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		// Message isn't an object with content blocks — skip silently.
		return nil
	}

	var events []StreamEvent
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			events = append(events, StreamEvent{
				Type:      EventAssistant,
				SessionID: sessionID,
				Message:   block.Text,
				Raw:       raw,
			})
		case "tool_use":
			events = append(events, StreamEvent{
				Type:      EventToolUse,
				SessionID: sessionID,
				ToolName:  block.Name,
				ToolID:    block.ID,
				Input:     block.Input,
				Raw:       raw,
			})
		case "tool_result":
			events = append(events, StreamEvent{
				Type:      EventToolResult,
				SessionID: sessionID,
				ToolID:    block.ToolUseID,
				Raw:       raw,
			})
		// Skip "thinking", "signature", and other block types.
		}
	}
	return events
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

// buildCommonArgs constructs the shared CLI arguments (model, turns, budget, etc.)
// that are common across all invocation modes.
func (e *Executor) buildCommonArgs(cfg RunConfig) []string {
	var args []string
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

// buildStreamingArgs constructs the CLI arguments for a streaming invocation.
func (e *Executor) buildStreamingArgs(cfg RunConfig) []string {
	args := []string{
		"-p", cfg.Prompt,
		"--output-format", "stream-json",
		"--verbose",
	}
	args = append(args, e.buildCommonArgs(cfg)...)
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

		sentInit := false

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			events := parseStreamLine(line)

			for _, evt := range events {
				// Emit a synthetic init event the first time we see a session ID.
				if !sentInit && evt.SessionID != "" {
					sentInit = true
					select {
					case ch <- StreamEvent{
						Type:      EventInit,
						SessionID: evt.SessionID,
					}:
					case <-ctx.Done():
						return
					}
				}

				select {
				case ch <- evt:
				case <-ctx.Done():
					return
				}
			}
		}

		// Wait for process to exit and report non-zero exit as an error event.
		if waitErr := cmd.Wait(); waitErr != nil {
			select {
			case ch <- StreamEvent{
				Type:    EventError,
				Message: fmt.Sprintf("agent process exited with error: %v", waitErr),
			}:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}

// RunInteractive runs the Claude CLI with stdin/stdout/stderr connected to the
// user's terminal. This mode does NOT use the -p flag — Claude runs in its
// interactive mode where it can converse with the user. If cfg.Prompt is set it
// is passed via -p so the session starts with an initial message.
func (e *Executor) RunInteractive(ctx context.Context, cfg RunConfig) error {
	bin := e.ClaudePath
	if bin == "" {
		bin = "claude"
	}

	var args []string
	if cfg.Prompt != "" {
		args = append(args, "-p", cfg.Prompt)
	}
	args = append(args, e.buildCommonArgs(cfg)...)

	cmd := exec.CommandContext(ctx, bin, args...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude: interactive: %w", err)
	}
	return nil
}

// PromptResult holds the output from a single-turn prompt invocation,
// including the session ID needed to resume the conversation.
type PromptResult struct {
	SessionID string
	Text      string
}

// RunPromptCapture runs the Claude CLI with -p and --output-format json,
// returning the response text and session ID. This is useful for sending an
// initial prompt and then resuming the session interactively via
// RunInteractive with ResumeSessionID.
func (e *Executor) RunPromptCapture(ctx context.Context, cfg RunConfig) (*PromptResult, error) {
	bin := e.ClaudePath
	if bin == "" {
		bin = "claude"
	}

	args := []string{"-p", cfg.Prompt, "--output-format", "json"}
	args = append(args, e.buildCommonArgs(cfg)...)

	cmd := exec.CommandContext(ctx, bin, args...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude: prompt capture: %w", err)
	}

	output := stdout.Bytes()
	if len(output) == 0 {
		return nil, fmt.Errorf("claude: prompt capture: empty output")
	}

	var envelope struct {
		SessionID string          `json:"session_id"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		return nil, fmt.Errorf("claude: prompt capture: parsing output: %w", err)
	}

	var text string
	if err := json.Unmarshal(envelope.Result, &text); err != nil {
		text = string(envelope.Result)
	}

	return &PromptResult{
		SessionID: envelope.SessionID,
		Text:      text,
	}, nil
}

// jsonOutput is the envelope returned by claude --output-format json.
type jsonOutput struct {
	Result json.RawMessage `json:"result"`
}

// RunJSON runs the Claude CLI with -p, --output-format json, and --json-schema.
// It returns the parsed result field from the JSON output. An error is returned
// if Claude exits non-zero or the output is not valid JSON.
func (e *Executor) RunJSON(ctx context.Context, cfg RunConfig, jsonSchema string) (json.RawMessage, error) {
	bin := e.ClaudePath
	if bin == "" {
		bin = "claude"
	}

	args := []string{
		"-p", cfg.Prompt,
		"--output-format", "json",
	}
	if jsonSchema != "" {
		args = append(args, "--json-schema", jsonSchema)
	}
	args = append(args, e.buildCommonArgs(cfg)...)

	cmd := exec.CommandContext(ctx, bin, args...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude: json mode: %w (stderr: %s)", err, stderr.String())
	}

	output := stdout.Bytes()
	if len(output) == 0 {
		return nil, fmt.Errorf("claude: json mode: empty output")
	}

	// The claude CLI with --output-format json returns a JSON object with a
	// "result" field containing the actual response.
	var envelope jsonOutput
	if err := json.Unmarshal(output, &envelope); err != nil {
		// If the output doesn't match the envelope format, try returning it
		// directly as raw JSON.
		if !json.Valid(output) {
			return nil, fmt.Errorf("claude: json mode: output is not valid JSON: %s", string(output))
		}
		return json.RawMessage(output), nil
	}

	if envelope.Result == nil {
		// No "result" field — return the entire output as raw JSON.
		return json.RawMessage(output), nil
	}

	return envelope.Result, nil
}
