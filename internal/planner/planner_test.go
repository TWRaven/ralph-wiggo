package planner

import (
	"testing"

	"github.com/radvoogh/ralph-wiggo/internal/prd"
)

func testPRD() *prd.PRD {
	return &prd.PRD{
		Project:     "test-project",
		BranchName:  "ralph/test",
		Description: "Test PRD",
		UserStories: []prd.UserStory{
			{ID: "US-001", Title: "First", Priority: 1, Passes: true},
			{ID: "US-002", Title: "Second", Priority: 2, Passes: false},
			{ID: "US-003", Title: "Third", Priority: 3, Passes: false},
			{ID: "US-004", Title: "Fourth", Priority: 4, Passes: false},
			{ID: "US-005", Title: "Fifth", Priority: 5, Passes: false},
		},
	}
}

func TestNextStories_Sequential(t *testing.T) {
	p := testPRD()
	stories, err := NextStories(p, "sequential")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 1 {
		t.Fatalf("expected 1 story, got %d", len(stories))
	}
	if stories[0].ID != "US-002" {
		t.Errorf("expected US-002, got %s", stories[0].ID)
	}
}

func TestNextStories_SequentialSkipsPassed(t *testing.T) {
	p := testPRD()
	// Mark US-002 as passed too.
	p.UserStories[1].Passes = true

	stories, err := NextStories(p, "sequential")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 1 {
		t.Fatalf("expected 1 story, got %d", len(stories))
	}
	if stories[0].ID != "US-003" {
		t.Errorf("expected US-003, got %s", stories[0].ID)
	}
}

func TestNextStories_AllPassed(t *testing.T) {
	p := testPRD()
	for i := range p.UserStories {
		p.UserStories[i].Passes = true
	}

	stories, err := NextStories(p, "sequential")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 0 {
		t.Errorf("expected empty slice, got %d stories", len(stories))
	}
}

func TestNextStories_ParallelN(t *testing.T) {
	p := testPRD()
	stories, err := NextStories(p, "parallel-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 3 {
		t.Fatalf("expected 3 stories, got %d", len(stories))
	}
	// Should be the 3 highest-priority incomplete stories.
	expectedIDs := []string{"US-002", "US-003", "US-004"}
	for i, id := range expectedIDs {
		if stories[i].ID != id {
			t.Errorf("story[%d]: expected %s, got %s", i, id, stories[i].ID)
		}
	}
}

func TestNextStories_ParallelExceedingIncomplete(t *testing.T) {
	p := testPRD()
	// Only 4 incomplete stories exist.
	stories, err := NextStories(p, "parallel-10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 4 {
		t.Fatalf("expected 4 stories (all incomplete), got %d", len(stories))
	}
}

func TestNextStories_Parallel1(t *testing.T) {
	p := testPRD()
	stories, err := NextStories(p, "parallel-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 1 {
		t.Fatalf("expected 1 story, got %d", len(stories))
	}
	if stories[0].ID != "US-002" {
		t.Errorf("expected US-002, got %s", stories[0].ID)
	}
}

func TestNextStories_ParallelAllPassed(t *testing.T) {
	p := testPRD()
	for i := range p.UserStories {
		p.UserStories[i].Passes = true
	}

	stories, err := NextStories(p, "parallel-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 0 {
		t.Errorf("expected empty slice, got %d stories", len(stories))
	}
}

func TestNextStories_InvalidMode(t *testing.T) {
	p := testPRD()
	_, err := NextStories(p, "unknown")
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestNextStories_InvalidParallelFormat(t *testing.T) {
	p := testPRD()
	_, err := NextStories(p, "parallel-abc")
	if err == nil {
		t.Fatal("expected error for invalid parallel format")
	}
}

func TestNextStories_ParallelZero(t *testing.T) {
	p := testPRD()
	_, err := NextStories(p, "parallel-0")
	if err == nil {
		t.Fatal("expected error for parallel-0")
	}
}

func TestNextStories_UnsortedPriorities(t *testing.T) {
	// Stories listed out of priority order.
	p := &prd.PRD{
		Project: "test",
		UserStories: []prd.UserStory{
			{ID: "US-003", Title: "Third", Priority: 3, Passes: false},
			{ID: "US-001", Title: "First", Priority: 1, Passes: false},
			{ID: "US-002", Title: "Second", Priority: 2, Passes: false},
		},
	}

	stories, err := NextStories(p, "sequential")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stories[0].ID != "US-001" {
		t.Errorf("expected US-001 (lowest priority number), got %s", stories[0].ID)
	}

	stories, err = NextStories(p, "parallel-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stories[0].ID != "US-001" || stories[1].ID != "US-002" {
		t.Errorf("expected [US-001, US-002], got [%s, %s]", stories[0].ID, stories[1].ID)
	}
}
