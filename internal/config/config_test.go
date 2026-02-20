package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_FileExists(t *testing.T) {
	dir := t.TempDir()
	content := `model: claude-opus-4-6
maxBudget: 5.0
maxTurns: 100
parallelism: parallel-3
allowedTools:
  - Bash
  - Read
  - Edit
port: 9090
`
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigFile), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-opus-4-6")
	}
	if cfg.MaxBudget != 5.0 {
		t.Errorf("MaxBudget = %v, want 5.0", cfg.MaxBudget)
	}
	if cfg.MaxTurns != 100 {
		t.Errorf("MaxTurns = %d, want 100", cfg.MaxTurns)
	}
	if cfg.Parallelism != "parallel-3" {
		t.Errorf("Parallelism = %q, want %q", cfg.Parallelism, "parallel-3")
	}
	if len(cfg.AllowedTools) != 3 || cfg.AllowedTools[0] != "Bash" {
		t.Errorf("AllowedTools = %v, want [Bash Read Edit]", cfg.AllowedTools)
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d, want 9090", cfg.Port)
	}
}

func TestLoad_FileNotExists(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Zero values expected.
	if cfg.Model != "" {
		t.Errorf("Model = %q, want empty", cfg.Model)
	}
	if cfg.MaxTurns != 0 {
		t.Errorf("MaxTurns = %d, want 0", cfg.MaxTurns)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigFile), []byte(":::invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoad_PartialConfig(t *testing.T) {
	dir := t.TempDir()
	content := `model: claude-haiku-4-5-20251001
`
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigFile), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-haiku-4-5-20251001")
	}
	// Unset fields should be zero values.
	if cfg.MaxTurns != 0 {
		t.Errorf("MaxTurns = %d, want 0", cfg.MaxTurns)
	}
	if cfg.Parallelism != "" {
		t.Errorf("Parallelism = %q, want empty", cfg.Parallelism)
	}
}
