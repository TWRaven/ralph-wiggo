package prd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func testPRD() *PRD {
	return &PRD{
		Project:     "test-project",
		BranchName:  "ralph/test-project",
		Description: "A test project",
		UserStories: []UserStory{
			{
				ID:                 "US-001",
				Title:              "First story",
				Description:        "As a dev, I want to test loading.",
				AcceptanceCriteria: []string{"Loads correctly", "Parses JSON"},
				Priority:           1,
				Passes:             false,
				Notes:              "some notes",
			},
			{
				ID:                 "US-002",
				Title:              "Second story",
				Description:        "As a dev, I want to test saving.",
				AcceptanceCriteria: []string{"Saves correctly"},
				Priority:           2,
				Passes:             true,
				Notes:              "",
			},
		},
	}
}

func TestLoadPRD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prd.json")

	// Write a valid PRD file.
	p := testPRD()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Load it back.
	loaded, err := LoadPRD(path)
	if err != nil {
		t.Fatalf("LoadPRD: %v", err)
	}

	if loaded.Project != "test-project" {
		t.Errorf("project = %q, want %q", loaded.Project, "test-project")
	}
	if loaded.BranchName != "ralph/test-project" {
		t.Errorf("branchName = %q, want %q", loaded.BranchName, "ralph/test-project")
	}
	if len(loaded.UserStories) != 2 {
		t.Fatalf("stories = %d, want 2", len(loaded.UserStories))
	}
	if loaded.UserStories[0].ID != "US-001" {
		t.Errorf("story[0].ID = %q, want %q", loaded.UserStories[0].ID, "US-001")
	}
	if loaded.UserStories[1].Passes != true {
		t.Errorf("story[1].Passes = %v, want true", loaded.UserStories[1].Passes)
	}
}

func TestLoadPRD_NotFound(t *testing.T) {
	_, err := LoadPRD("/nonexistent/prd.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadPRD_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prd.json")
	if err := os.WriteFile(path, []byte("{invalid}"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadPRD(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSavePRD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prd.json")

	p := testPRD()
	if err := SavePRD(path, p); err != nil {
		t.Fatalf("SavePRD: %v", err)
	}

	// Read back and verify it's valid JSON with indentation.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Should be indented (contains newlines and spaces).
	if len(data) < 10 {
		t.Fatal("saved file is too small")
	}

	// Should parse back correctly.
	var loaded PRD
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if loaded.Project != "test-project" {
		t.Errorf("project = %q, want %q", loaded.Project, "test-project")
	}
	if len(loaded.UserStories) != 2 {
		t.Errorf("stories = %d, want 2", len(loaded.UserStories))
	}
}

func TestSavePRD_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prd.json")

	original := testPRD()
	if err := SavePRD(path, original); err != nil {
		t.Fatalf("SavePRD: %v", err)
	}
	loaded, err := LoadPRD(path)
	if err != nil {
		t.Fatalf("LoadPRD: %v", err)
	}

	// Verify round-trip preserves data.
	if loaded.Project != original.Project {
		t.Errorf("project mismatch: %q vs %q", loaded.Project, original.Project)
	}
	if loaded.Description != original.Description {
		t.Errorf("description mismatch")
	}
	if len(loaded.UserStories) != len(original.UserStories) {
		t.Fatalf("story count: %d vs %d", len(loaded.UserStories), len(original.UserStories))
	}
	for i := range original.UserStories {
		if loaded.UserStories[i].ID != original.UserStories[i].ID {
			t.Errorf("story[%d].ID: %q vs %q", i, loaded.UserStories[i].ID, original.UserStories[i].ID)
		}
		if loaded.UserStories[i].Priority != original.UserStories[i].Priority {
			t.Errorf("story[%d].Priority: %d vs %d", i, loaded.UserStories[i].Priority, original.UserStories[i].Priority)
		}
		if loaded.UserStories[i].Passes != original.UserStories[i].Passes {
			t.Errorf("story[%d].Passes: %v vs %v", i, loaded.UserStories[i].Passes, original.UserStories[i].Passes)
		}
		if len(loaded.UserStories[i].AcceptanceCriteria) != len(original.UserStories[i].AcceptanceCriteria) {
			t.Errorf("story[%d].AcceptanceCriteria length: %d vs %d", i, len(loaded.UserStories[i].AcceptanceCriteria), len(original.UserStories[i].AcceptanceCriteria))
		}
	}
}

func TestValidate_Valid(t *testing.T) {
	p := testPRD()
	if err := Validate(p); err != nil {
		t.Errorf("Validate returned error for valid PRD: %v", err)
	}
}

func TestValidate_Empty(t *testing.T) {
	p := &PRD{Project: "empty"}
	if err := Validate(p); err != nil {
		t.Errorf("Validate returned error for empty stories: %v", err)
	}
}

func TestValidate_DuplicateIDs(t *testing.T) {
	p := &PRD{
		Project: "dup",
		UserStories: []UserStory{
			{ID: "US-001", Priority: 1},
			{ID: "US-001", Priority: 2},
		},
	}
	err := Validate(p)
	if err == nil {
		t.Fatal("expected error for duplicate IDs")
	}
}

func TestValidate_EmptyID(t *testing.T) {
	p := &PRD{
		Project: "empty-id",
		UserStories: []UserStory{
			{ID: "", Priority: 1},
		},
	}
	err := Validate(p)
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
}

func TestValidate_NonSequentialPriority(t *testing.T) {
	p := &PRD{
		Project: "gap",
		UserStories: []UserStory{
			{ID: "US-001", Priority: 1},
			{ID: "US-002", Priority: 3},
		},
	}
	err := Validate(p)
	if err == nil {
		t.Fatal("expected error for non-sequential priorities")
	}
}

func TestValidate_PriorityStartsAtTwo(t *testing.T) {
	p := &PRD{
		Project: "no-one",
		UserStories: []UserStory{
			{ID: "US-001", Priority: 2},
		},
	}
	err := Validate(p)
	if err == nil {
		t.Fatal("expected error when priority doesn't start at 1")
	}
}

func TestValidate_UnorderedButSequential(t *testing.T) {
	// Stories listed out of order but priorities are sequential 1,2,3.
	p := &PRD{
		Project: "unordered",
		UserStories: []UserStory{
			{ID: "US-003", Priority: 3},
			{ID: "US-001", Priority: 1},
			{ID: "US-002", Priority: 2},
		},
	}
	if err := Validate(p); err != nil {
		t.Errorf("Validate should accept unordered but sequential priorities: %v", err)
	}
}

func TestJSONSchema_ValidJSON(t *testing.T) {
	var schema map[string]interface{}
	if err := json.Unmarshal([]byte(JSONSchema), &schema); err != nil {
		t.Fatalf("JSONSchema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}
