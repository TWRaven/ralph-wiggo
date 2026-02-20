package planner

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/radvoogh/ralph-wiggo/internal/prd"
)

// NextStories returns the next stories to work on based on the parallelism mode.
//
// Supported modes:
//   - "sequential": returns the single highest-priority incomplete story
//   - "parallel-N" (e.g. "parallel-3"): returns up to N highest-priority incomplete stories
//
// Stories with Passes == true are never returned.
// Returns an empty slice when all stories pass.
func NextStories(p *prd.PRD, mode string) ([]*prd.UserStory, error) {
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

	default:
		return nil, fmt.Errorf("unknown planner mode: %q", mode)
	}
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
