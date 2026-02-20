package prompts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/radvoogh/ralph-wiggo/internal/prompts"
)

func TestGetEmbeddedFiles(t *testing.T) {
	tests := []struct {
		name     string
		contains string
	}{
		{"prompt.md", "Ralph Agent Instructions"},
		{"prd-skill.md", "PRD Generator"},
		{"ralph-skill.md", "Ralph PRD Converter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := prompts.Get(tt.name)
			if err != nil {
				t.Fatalf("Get(%q) returned error: %v", tt.name, err)
			}
			if !strings.Contains(content, tt.contains) {
				t.Errorf("Get(%q) missing expected content %q", tt.name, tt.contains)
			}
		})
	}
}

func TestGetNonExistent(t *testing.T) {
	_, err := prompts.Get("nonexistent.md")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestOverride(t *testing.T) {
	dir := t.TempDir()
	overridePath := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(overridePath, []byte("custom content"), 0644); err != nil {
		t.Fatal(err)
	}

	prompts.SetOverride("prompt.md", overridePath)
	t.Cleanup(func() {
		// Reset by overriding with a non-existent path won't work, but
		// for test isolation this is sufficient since tests run in a single process.
		prompts.SetOverride("prompt.md", "")
	})

	content, err := prompts.Get("prompt.md")
	if err != nil {
		t.Fatalf("Get with override returned error: %v", err)
	}
	if content != "custom content" {
		t.Errorf("expected override content, got %q", content)
	}
}
