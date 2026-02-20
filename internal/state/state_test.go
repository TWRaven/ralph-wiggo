package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/radvoogh/ralph-wiggo/internal/claude"
)

func testRun() *Run {
	return &Run{
		ID:         "run-001",
		PRDPath:    "prd.json",
		BranchName: "ralph/test-feature",
		StartTime:  time.Date(2026, 2, 20, 12, 0, 0, 0, time.UTC),
		Status:     StatusRunning,
		Stories:    []*AgentSession{},
	}
}

func TestSaveAndGetRun(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	run := testRun()
	if err := store.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	got, err := store.GetRun("run-001")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ID != "run-001" {
		t.Errorf("ID = %q, want %q", got.ID, "run-001")
	}
	if got.BranchName != "ralph/test-feature" {
		t.Errorf("BranchName = %q, want %q", got.BranchName, "ralph/test-feature")
	}
	if got.Status != StatusRunning {
		t.Errorf("Status = %q, want %q", got.Status, StatusRunning)
	}
}

func TestGetRunNotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	_, err = store.GetRun("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent run")
	}
}

func TestListRuns(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	// Empty store returns empty list.
	runs, err := store.ListRuns()
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}

	// Add two runs.
	run1 := testRun()
	run2 := &Run{
		ID:         "run-002",
		PRDPath:    "prd.json",
		BranchName: "ralph/other",
		StartTime:  time.Date(2026, 2, 21, 12, 0, 0, 0, time.UTC),
		Status:     StatusPassed,
	}
	if err := store.SaveRun(run1); err != nil {
		t.Fatalf("SaveRun run1: %v", err)
	}
	if err := store.SaveRun(run2); err != nil {
		t.Fatalf("SaveRun run2: %v", err)
	}

	runs, err = store.ListRuns()
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	// Most recent first.
	if runs[0].ID != "run-002" {
		t.Errorf("first run ID = %q, want %q (most recent)", runs[0].ID, "run-002")
	}
	if runs[1].ID != "run-001" {
		t.Errorf("second run ID = %q, want %q", runs[1].ID, "run-001")
	}
}

func TestAddIteration(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	run := testRun()
	if err := store.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	iter := Iteration{
		RunID:     "run-001",
		StoryID:   "US-001",
		Number:    1,
		StartTime: time.Date(2026, 2, 20, 12, 1, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 2, 20, 12, 5, 0, 0, time.UTC),
		Status:    StatusPassed,
		Events: []claude.StreamEvent{
			{Type: claude.EventAssistant, Message: "Implementing US-001..."},
		},
	}
	if err := store.AddIteration("run-001", iter); err != nil {
		t.Fatalf("AddIteration: %v", err)
	}

	// Verify the session was created and has the iteration.
	got, err := store.GetRun("run-001")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(got.Stories) != 1 {
		t.Fatalf("expected 1 story session, got %d", len(got.Stories))
	}
	sess := got.Stories[0]
	if sess.StoryID != "US-001" {
		t.Errorf("session StoryID = %q, want %q", sess.StoryID, "US-001")
	}
	if sess.Status != StatusPassed {
		t.Errorf("session Status = %q, want %q", sess.Status, StatusPassed)
	}
	if len(sess.Iterations) != 1 {
		t.Fatalf("expected 1 iteration, got %d", len(sess.Iterations))
	}
	if sess.Iterations[0].Number != 1 {
		t.Errorf("iteration Number = %d, want 1", sess.Iterations[0].Number)
	}
}

func TestAddIterationCreatesSession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	run := testRun()
	if err := store.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	// First iteration: session should be created automatically.
	iter1 := Iteration{RunID: "run-001", StoryID: "US-002", Number: 1, Status: StatusFailed}
	if err := store.AddIteration("run-001", iter1); err != nil {
		t.Fatalf("AddIteration iter1: %v", err)
	}

	// Second iteration: session should be reused.
	iter2 := Iteration{RunID: "run-001", StoryID: "US-002", Number: 2, Status: StatusPassed}
	if err := store.AddIteration("run-001", iter2); err != nil {
		t.Fatalf("AddIteration iter2: %v", err)
	}

	got, err := store.GetRun("run-001")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(got.Stories) != 1 {
		t.Fatalf("expected 1 story session, got %d", len(got.Stories))
	}
	if len(got.Stories[0].Iterations) != 2 {
		t.Fatalf("expected 2 iterations, got %d", len(got.Stories[0].Iterations))
	}
	// Session status should reflect the latest iteration.
	if got.Stories[0].Status != StatusPassed {
		t.Errorf("session Status = %q, want %q", got.Stories[0].Status, StatusPassed)
	}
}

func TestAddIterationRunNotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	iter := Iteration{RunID: "nonexistent", StoryID: "US-001", Number: 1}
	if err := store.AddIteration("nonexistent", iter); err == nil {
		t.Fatal("expected error for nonexistent run")
	}
}

func TestGetIterationsForStory(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	run := testRun()
	if err := store.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	// Add iterations for two different stories.
	iter1 := Iteration{RunID: "run-001", StoryID: "US-001", Number: 1, Status: StatusPassed}
	iter2 := Iteration{RunID: "run-001", StoryID: "US-002", Number: 1, Status: StatusFailed}
	iter3 := Iteration{RunID: "run-001", StoryID: "US-002", Number: 2, Status: StatusPassed}
	for _, iter := range []Iteration{iter1, iter2, iter3} {
		if err := store.AddIteration("run-001", iter); err != nil {
			t.Fatalf("AddIteration: %v", err)
		}
	}

	// Query iterations for US-002.
	iters, err := store.GetIterationsForStory("run-001", "US-002")
	if err != nil {
		t.Fatalf("GetIterationsForStory: %v", err)
	}
	if len(iters) != 2 {
		t.Fatalf("expected 2 iterations for US-002, got %d", len(iters))
	}
	if iters[0].Number != 1 || iters[1].Number != 2 {
		t.Errorf("iteration numbers = [%d, %d], want [1, 2]", iters[0].Number, iters[1].Number)
	}

	// Query a story with no iterations.
	iters, err = store.GetIterationsForStory("run-001", "US-999")
	if err != nil {
		t.Fatalf("GetIterationsForStory: %v", err)
	}
	if len(iters) != 0 {
		t.Errorf("expected 0 iterations for US-999, got %d", len(iters))
	}
}

func TestGetIterationsForStoryRunNotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	_, err = store.GetIterationsForStory("nonexistent", "US-001")
	if err == nil {
		t.Fatal("expected error for nonexistent run")
	}
}

func TestDiskPersistence(t *testing.T) {
	dir := t.TempDir()

	// Create a store, save some data.
	store1, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore 1: %v", err)
	}

	run := testRun()
	if err := store1.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	iter := Iteration{RunID: "run-001", StoryID: "US-001", Number: 1, Status: StatusPassed}
	if err := store1.AddIteration("run-001", iter); err != nil {
		t.Fatalf("AddIteration: %v", err)
	}

	// Verify the JSON file exists on disk.
	jsonPath := filepath.Join(dir, "run-001.json")
	if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
		t.Fatal("expected run-001.json to exist on disk")
	}

	// Create a new store from the same directory — it should load history.
	store2, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore 2: %v", err)
	}

	got, err := store2.GetRun("run-001")
	if err != nil {
		t.Fatalf("GetRun from reloaded store: %v", err)
	}
	if got.BranchName != "ralph/test-feature" {
		t.Errorf("BranchName = %q, want %q", got.BranchName, "ralph/test-feature")
	}
	if len(got.Stories) != 1 {
		t.Fatalf("expected 1 story session, got %d", len(got.Stories))
	}
	if len(got.Stories[0].Iterations) != 1 {
		t.Fatalf("expected 1 iteration, got %d", len(got.Stories[0].Iterations))
	}
}

func TestNewMemoryStoreEmptyDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent-subdir")
	// The directory doesn't exist yet — NewMemoryStore should handle it.
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore with nonexistent dir: %v", err)
	}

	runs, err := store.ListRuns()
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}
}

func TestSaveRunOverwrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	run := testRun()
	if err := store.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	// Update the run status and save again.
	run.Status = StatusPassed
	if err := store.SaveRun(run); err != nil {
		t.Fatalf("SaveRun overwrite: %v", err)
	}

	got, err := store.GetRun("run-001")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != StatusPassed {
		t.Errorf("Status = %q, want %q after overwrite", got.Status, StatusPassed)
	}
}
