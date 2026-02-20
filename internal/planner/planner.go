package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/radvoogh/ralph-wiggo/internal/claude"
	"github.com/radvoogh/ralph-wiggo/internal/prd"
)

// JSONRunner is the interface required by auto mode to invoke Claude for
// dependency analysis. It is satisfied by *claude.Executor.
type JSONRunner interface {
	RunJSON(ctx context.Context, cfg claude.RunConfig, jsonSchema string) (json.RawMessage, error)
}

// NextStories returns the next stories to work on based on the parallelism mode.
//
// Supported modes:
//   - "sequential": returns the single highest-priority incomplete story
//   - "parallel-N" (e.g. "parallel-3"): returns up to N highest-priority incomplete stories
//   - "auto": uses Claude to analyze dependencies and group stories into parallelizable batches
//
// For "auto" mode, exec and ctx are required; if the Claude call fails, it falls
// back to sequential mode. For other modes, exec may be nil.
//
// Stories with Passes == true are never returned.
// Returns an empty slice when all stories pass.
func NextStories(ctx context.Context, p *prd.PRD, mode string, exec JSONRunner) ([]*prd.UserStory, error) {
	incomplete := incompleteByPriority(p)
	if len(incomplete) == 0 {
		return nil, nil
	}

	switch {
	case mode == "sequential":
		return incomplete[:1], nil

	case strings.HasPrefix(mode, "parallel-"):
		nStr := strings.TrimPrefix(mode, "parallel-")
		n, err := strconv.Atoi(nStr)
		if err != nil {
			return nil, fmt.Errorf("invalid parallel mode %q: %w", mode, err)
		}
		if n < 1 {
			return nil, fmt.Errorf("parallel count must be >= 1, got %d", n)
		}
		if n > len(incomplete) {
			n = len(incomplete)
		}
		return incomplete[:n], nil

	case mode == "auto":
		return autoMode(ctx, p, incomplete, exec)

	default:
		return nil, fmt.Errorf("unknown planner mode: %q", mode)
	}
}

// batchSchema is the JSON schema for the batch response from Claude.
const batchSchema = `{
  "type": "object",
  "required": ["batches"],
  "properties": {
    "batches": {
      "type": "array",
      "description": "Array of batches. Each batch is an array of story IDs that can run concurrently. Batches must be executed in order.",
      "items": {
        "type": "array",
        "items": { "type": "string" }
      }
    }
  }
}`

// batchResponse is the parsed response from Claude's dependency analysis.
type batchResponse struct {
	Batches [][]string `json:"batches"`
}

// autoMode sends incomplete stories to Claude for dependency analysis and
// returns the first incomplete batch. Falls back to sequential mode on error.
func autoMode(ctx context.Context, p *prd.PRD, incomplete []*prd.UserStory, exec JSONRunner) ([]*prd.UserStory, error) {
	if exec == nil {
		log.Println("planner: auto mode requires an executor, falling back to sequential")
		return incomplete[:1], nil
	}

	prompt := buildAutoPrompt(incomplete)
	cfg := claude.RunConfig{
		Prompt: prompt,
	}

	raw, err := exec.RunJSON(ctx, cfg, batchSchema)
	if err != nil {
		log.Printf("planner: auto mode Claude call failed, falling back to sequential: %v", err)
		return incomplete[:1], nil
	}

	var resp batchResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		log.Printf("planner: auto mode failed to parse batches, falling back to sequential: %v", err)
		return incomplete[:1], nil
	}

	if len(resp.Batches) == 0 {
		log.Println("planner: auto mode returned empty batches, falling back to sequential")
		return incomplete[:1], nil
	}

	// Build a lookup from story ID to pointer for incomplete stories.
	byID := make(map[string]*prd.UserStory, len(incomplete))
	for _, s := range incomplete {
		byID[s.ID] = s
	}

	// Find the first batch that contains at least one incomplete story.
	for _, batch := range resp.Batches {
		var stories []*prd.UserStory
		for _, id := range batch {
			if s, ok := byID[id]; ok {
				stories = append(stories, s)
			}
		}
		if len(stories) > 0 {
			return stories, nil
		}
	}

	// All batches resolved or empty — fall back to sequential.
	log.Println("planner: auto mode batches contain no incomplete stories, falling back to sequential")
	return incomplete[:1], nil
}

// buildAutoPrompt constructs the prompt sent to Claude for dependency analysis.
func buildAutoPrompt(stories []*prd.UserStory) string {
	var sb strings.Builder
	sb.WriteString("Analyze the following user stories and group them into parallelizable batches. ")
	sb.WriteString("Stories in the same batch can be worked on concurrently (they have no dependencies on each other). ")
	sb.WriteString("Batches must be executed in order — all stories in batch 1 must complete before batch 2 starts.\n\n")
	sb.WriteString("Return ONLY the batches as an array of arrays of story IDs.\n\n")
	sb.WriteString("Stories:\n")
	for _, s := range stories {
		sb.WriteString(fmt.Sprintf("- %s (priority %d): %s — %s\n", s.ID, s.Priority, s.Title, s.Description))
	}
	return sb.String()
}

// incompleteByPriority returns pointers to incomplete stories sorted by priority (ascending).
func incompleteByPriority(p *prd.PRD) []*prd.UserStory {
	var stories []*prd.UserStory
	for i := range p.UserStories {
		if !p.UserStories[i].Passes {
			stories = append(stories, &p.UserStories[i])
		}
	}
	sort.Slice(stories, func(i, j int) bool {
		return stories[i].Priority < stories[j].Priority
	})
	return stories
}
