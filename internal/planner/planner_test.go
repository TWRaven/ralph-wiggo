package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/radvoogh/ralph-wiggo/internal/claude"
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

// mockJSONRunner implements JSONRunner for testing auto mode.
type mockJSONRunner struct {
	response json.RawMessage
	err      error
}

func (m *mockJSONRunner) RunJSON(_ context.Context, _ claude.RunConfig, _ string) (json.RawMessage, error) {
	return m.response, m.err
}

func TestNextStories_Sequential(t *testing.T) {
	p := testPRD()
	stories, err := NextStories(context.Background(), p, "sequential", nil)
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

	stories, err := NextStories(context.Background(), p, "sequential", nil)
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

	stories, err := NextStories(context.Background(), p, "sequential", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 0 {
		t.Errorf("expected empty slice, got %d stories", len(stories))
	}
}

func TestNextStories_ParallelN(t *testing.T) {
	p := testPRD()
	stories, err := NextStories(context.Background(), p, "parallel-3", nil)
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
	stories, err := NextStories(context.Background(), p, "parallel-10", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 4 {
		t.Fatalf("expected 4 stories (all incomplete), got %d", len(stories))
	}
}

func TestNextStories_Parallel1(t *testing.T) {
	p := testPRD()
	stories, err := NextStories(context.Background(), p, "parallel-1", nil)
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

	stories, err := NextStories(context.Background(), p, "parallel-3", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 0 {
		t.Errorf("expected empty slice, got %d stories", len(stories))
	}
}

func TestNextStories_InvalidMode(t *testing.T) {
	p := testPRD()
	_, err := NextStories(context.Background(), p, "unknown", nil)
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestNextStories_InvalidParallelFormat(t *testing.T) {
	p := testPRD()
	_, err := NextStories(context.Background(), p, "parallel-abc", nil)
	if err == nil {
		t.Fatal("expected error for invalid parallel format")
	}
}

func TestNextStories_ParallelZero(t *testing.T) {
	p := testPRD()
	_, err := NextStories(context.Background(), p, "parallel-0", nil)
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

	stories, err := NextStories(context.Background(), p, "sequential", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stories[0].ID != "US-001" {
		t.Errorf("expected US-001 (lowest priority number), got %s", stories[0].ID)
	}

	stories, err = NextStories(context.Background(), p, "parallel-2", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stories[0].ID != "US-001" || stories[1].ID != "US-002" {
		t.Errorf("expected [US-001, US-002], got [%s, %s]", stories[0].ID, stories[1].ID)
	}
}

// --- Auto mode tests ---

func TestNextStories_AutoMode(t *testing.T) {
	p := testPRD()
	mock := &mockJSONRunner{
		response: json.RawMessage(`{"batches": [["US-002", "US-003"], ["US-004", "US-005"]]}`),
	}

	stories, err := NextStories(context.Background(), p, "auto", mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 2 {
		t.Fatalf("expected 2 stories in first batch, got %d", len(stories))
	}
	if stories[0].ID != "US-002" || stories[1].ID != "US-003" {
		t.Errorf("expected [US-002, US-003], got [%s, %s]", stories[0].ID, stories[1].ID)
	}
}

func TestNextStories_AutoModeSingleBatch(t *testing.T) {
	p := testPRD()
	mock := &mockJSONRunner{
		response: json.RawMessage(`{"batches": [["US-002", "US-003", "US-004", "US-005"]]}`),
	}

	stories, err := NextStories(context.Background(), p, "auto", mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 4 {
		t.Fatalf("expected 4 stories, got %d", len(stories))
	}
}

func TestNextStories_AutoModeSkipsCompletedBatch(t *testing.T) {
	p := testPRD()
	// Mark US-002 and US-003 as passed — first batch is done.
	p.UserStories[1].Passes = true
	p.UserStories[2].Passes = true

	mock := &mockJSONRunner{
		response: json.RawMessage(`{"batches": [["US-002", "US-003"], ["US-004", "US-005"]]}`),
	}

	stories, err := NextStories(context.Background(), p, "auto", mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 2 {
		t.Fatalf("expected 2 stories from second batch, got %d", len(stories))
	}
	if stories[0].ID != "US-004" || stories[1].ID != "US-005" {
		t.Errorf("expected [US-004, US-005], got [%s, %s]", stories[0].ID, stories[1].ID)
	}
}

func TestNextStories_AutoModeFallbackOnError(t *testing.T) {
	p := testPRD()
	mock := &mockJSONRunner{
		err: fmt.Errorf("Claude call failed"),
	}

	stories, err := NextStories(context.Background(), p, "auto", mock)
	if err != nil {
		t.Fatalf("unexpected error (should fall back, not error): %v", err)
	}
	// Falls back to sequential — returns single highest-priority incomplete story.
	if len(stories) != 1 {
		t.Fatalf("expected 1 story (sequential fallback), got %d", len(stories))
	}
	if stories[0].ID != "US-002" {
		t.Errorf("expected US-002 (sequential fallback), got %s", stories[0].ID)
	}
}

func TestNextStories_AutoModeFallbackOnInvalidJSON(t *testing.T) {
	p := testPRD()
	mock := &mockJSONRunner{
		response: json.RawMessage(`not valid json`),
	}

	stories, err := NextStories(context.Background(), p, "auto", mock)
	if err != nil {
		t.Fatalf("unexpected error (should fall back, not error): %v", err)
	}
	if len(stories) != 1 {
		t.Fatalf("expected 1 story (sequential fallback), got %d", len(stories))
	}
	if stories[0].ID != "US-002" {
		t.Errorf("expected US-002, got %s", stories[0].ID)
	}
}

func TestNextStories_AutoModeFallbackOnEmptyBatches(t *testing.T) {
	p := testPRD()
	mock := &mockJSONRunner{
		response: json.RawMessage(`{"batches": []}`),
	}

	stories, err := NextStories(context.Background(), p, "auto", mock)
	if err != nil {
		t.Fatalf("unexpected error (should fall back, not error): %v", err)
	}
	if len(stories) != 1 {
		t.Fatalf("expected 1 story (sequential fallback), got %d", len(stories))
	}
}

func TestNextStories_AutoModeNilExecutor(t *testing.T) {
	p := testPRD()

	stories, err := NextStories(context.Background(), p, "auto", nil)
	if err != nil {
		t.Fatalf("unexpected error (should fall back, not error): %v", err)
	}
	if len(stories) != 1 {
		t.Fatalf("expected 1 story (sequential fallback), got %d", len(stories))
	}
	if stories[0].ID != "US-002" {
		t.Errorf("expected US-002, got %s", stories[0].ID)
	}
}

func TestNextStories_AutoModeAllPassed(t *testing.T) {
	p := testPRD()
	for i := range p.UserStories {
		p.UserStories[i].Passes = true
	}

	mock := &mockJSONRunner{
		response: json.RawMessage(`{"batches": [["US-001"]]}`),
	}

	stories, err := NextStories(context.Background(), p, "auto", mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stories) != 0 {
		t.Errorf("expected empty slice when all stories pass, got %d", len(stories))
	}
}

func TestNextStories_AutoModeIgnoresUnknownIDs(t *testing.T) {
	p := testPRD()
	mock := &mockJSONRunner{
		response: json.RawMessage(`{"batches": [["US-UNKNOWN"], ["US-002", "US-003"]]}`),
	}

	stories, err := NextStories(context.Background(), p, "auto", mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// First batch has no valid IDs, so it's skipped; second batch is returned.
	if len(stories) != 2 {
		t.Fatalf("expected 2 stories from second batch, got %d", len(stories))
	}
	if stories[0].ID != "US-002" || stories[1].ID != "US-003" {
		t.Errorf("expected [US-002, US-003], got [%s, %s]", stories[0].ID, stories[1].ID)
	}
}

func TestBuildAutoPrompt(t *testing.T) {
	stories := []*prd.UserStory{
		{ID: "US-002", Title: "Second", Description: "Desc 2", Priority: 2},
		{ID: "US-003", Title: "Third", Description: "Desc 3", Priority: 3},
	}
	prompt := buildAutoPrompt(stories)
	if !contains(prompt, "US-002") || !contains(prompt, "US-003") {
		t.Errorf("prompt should mention story IDs, got: %s", prompt)
	}
	if !contains(prompt, "parallelizable batches") {
		t.Errorf("prompt should mention parallelizable batches, got: %s", prompt)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
