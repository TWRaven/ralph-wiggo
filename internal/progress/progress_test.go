package progress

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/radvoogh/ralph-wiggo/internal/claude"
)

func TestInitIfNeeded_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.txt")

	if err := InitIfNeeded(path, "test-project", "ralph/test-branch"); err != nil {
		t.Fatalf("InitIfNeeded: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading progress file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "# Ralph Progress Log") {
		t.Error("missing header")
	}
	if !strings.Contains(content, "Project: test-project") {
		t.Error("missing project name")
	}
	if !strings.Contains(content, "Branch: ralph/test-branch") {
		t.Error("missing branch name")
	}
}

func TestInitIfNeeded_NoopIfExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.txt")

	// Create an existing file.
	if err := os.WriteFile(path, []byte("existing content"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := InitIfNeeded(path, "test-project", "ralph/test-branch"); err != nil {
		t.Fatalf("InitIfNeeded: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing content" {
		t.Error("InitIfNeeded overwrote existing file")
	}
}

func TestAppendEntry_Pass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.txt")

	// Create initial file.
	if err := os.WriteFile(path, []byte("# header\n"), 0644); err != nil {
		t.Fatal(err)
	}

	events := []claude.StreamEvent{
		{Type: claude.EventToolUse, ToolName: "Edit"},
		{Type: claude.EventToolUse, ToolName: "Read"},
		{Type: claude.EventToolUse, ToolName: "Edit"},
	}

	if err := AppendEntry(path, "US-001", true, events); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if !strings.Contains(content, "US-001 [PASS]") {
		t.Error("missing PASS status")
	}
	if !strings.Contains(content, "Tools used:") {
		t.Error("missing tools summary")
	}
	if !strings.Contains(content, "Edit(2)") {
		t.Error("missing Edit tool count")
	}
}

func TestAppendEntry_Fail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.txt")

	if err := os.WriteFile(path, []byte("# header\n"), 0644); err != nil {
		t.Fatal(err)
	}

	events := []claude.StreamEvent{
		{Type: claude.EventError, Message: "something went wrong"},
	}

	if err := AppendEntry(path, "US-002", false, events); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if !strings.Contains(content, "US-002 [FAIL]") {
		t.Error("missing FAIL status")
	}
	if !strings.Contains(content, "Error: something went wrong") {
		t.Error("missing error message")
	}
}

func TestArchiveIfBranchChanged_NoProgressFile(t *testing.T) {
	dir := t.TempDir()

	archived, err := ArchiveIfBranchChanged(dir,
		filepath.Join(dir, "prd.json"),
		filepath.Join(dir, "progress.txt"),
		"ralph/new-branch")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if archived {
		t.Error("should not archive when progress.txt doesn't exist")
	}
}

func TestArchiveIfBranchChanged_SameBranch(t *testing.T) {
	dir := t.TempDir()
	progressPath := filepath.Join(dir, "progress.txt")

	header := "# Ralph Progress Log\nProject: test\nBranch: ralph/same-branch\nStarted: now\n\n---\n"
	if err := os.WriteFile(progressPath, []byte(header), 0644); err != nil {
		t.Fatal(err)
	}

	archived, err := ArchiveIfBranchChanged(dir,
		filepath.Join(dir, "prd.json"),
		progressPath,
		"ralph/same-branch")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if archived {
		t.Error("should not archive when branch is the same")
	}
}

func TestArchiveIfBranchChanged_DifferentBranch(t *testing.T) {
	dir := t.TempDir()
	progressPath := filepath.Join(dir, "progress.txt")
	prdPath := filepath.Join(dir, "prd.json")

	header := "# Ralph Progress Log\nProject: test\nBranch: ralph/old-branch\nStarted: now\n\n---\n"
	if err := os.WriteFile(progressPath, []byte(header), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prdPath, []byte(`{"project":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	archived, err := ArchiveIfBranchChanged(dir, prdPath, progressPath, "ralph/new-branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !archived {
		t.Fatal("expected archiving to occur")
	}

	// Verify progress.txt was moved.
	if _, err := os.Stat(progressPath); !os.IsNotExist(err) {
		t.Error("progress.txt should have been moved")
	}

	// Verify archive directory was created with the old branch name (ralph/ stripped).
	entries, err := os.ReadDir(filepath.Join(dir, "archive"))
	if err != nil {
		t.Fatalf("reading archive dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 archive entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Name(), "old-branch") {
		t.Errorf("archive dir name %q should contain 'old-branch'", entries[0].Name())
	}

	// Verify archived files exist.
	archiveDir := filepath.Join(dir, "archive", entries[0].Name())
	if _, err := os.Stat(filepath.Join(archiveDir, "progress.txt")); err != nil {
		t.Error("progress.txt not in archive")
	}
	if _, err := os.Stat(filepath.Join(archiveDir, "prd.json")); err != nil {
		t.Error("prd.json snapshot not in archive")
	}
}
