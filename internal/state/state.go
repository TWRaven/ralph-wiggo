// Package state provides structured state management for agent runs and
// iterations. It supports an in-memory store backed by JSON files on disk
// so that the web UI and CLI can report on run progress and history.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/radvoogh/ralph-wiggo/internal/claude"
)

// Status represents the state of a story or run.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
)

// Run represents a single execution of the agent loop against a PRD.
type Run struct {
	ID         string          `json:"id"`
	PRDPath    string          `json:"prdPath"`
	BranchName string          `json:"branchName"`
	StartTime  time.Time       `json:"startTime"`
	Status     Status          `json:"status"`
	Stories    []*AgentSession `json:"stories"`
}

// Iteration represents a single attempt to implement a story within a run.
type Iteration struct {
	RunID     string              `json:"runID"`
	StoryID   string              `json:"storyID"`
	Number    int                 `json:"number"`
	StartTime time.Time           `json:"startTime"`
	EndTime   time.Time           `json:"endTime"`
	Status    Status              `json:"status"`
	Events    []claude.StreamEvent `json:"events"`
}

// AgentSession tracks the state of an agent working on a single story.
type AgentSession struct {
	StoryID    string      `json:"storyID"`
	Status     Status      `json:"status"`
	Iterations []Iteration `json:"iterations"`
}

// RunStore defines the interface for persisting and querying run state.
type RunStore interface {
	SaveRun(run *Run) error
	GetRun(id string) (*Run, error)
	ListRuns() ([]*Run, error)
	AddIteration(runID string, iter Iteration) error
	GetIterationsForStory(runID, storyID string) ([]Iteration, error)
}

// MemoryStore is an in-memory RunStore backed by JSON files on disk.
type MemoryStore struct {
	mu      sync.RWMutex
	runs    map[string]*Run
	baseDir string // directory for JSON persistence (e.g. .ralph-wiggo/runs/)
}

// NewMemoryStore creates a new MemoryStore that persists state to the given
// base directory. It loads any existing run history from disk.
func NewMemoryStore(baseDir string) (*MemoryStore, error) {
	s := &MemoryStore{
		runs:    make(map[string]*Run),
		baseDir: baseDir,
	}
	if err := s.loadFromDisk(); err != nil {
		return nil, fmt.Errorf("state: loading run history: %w", err)
	}
	return s, nil
}

// SaveRun persists a run to the in-memory store and writes it to disk.
func (s *MemoryStore) SaveRun(run *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.runs[run.ID] = run
	return s.persistRun(run)
}

// GetRun returns a run by its ID.
func (s *MemoryStore) GetRun(id string) (*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	run, ok := s.runs[id]
	if !ok {
		return nil, fmt.Errorf("state: run %q not found", id)
	}
	return run, nil
}

// ListRuns returns all runs ordered by start time (most recent first).
func (s *MemoryStore) ListRuns() ([]*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	runs := make([]*Run, 0, len(s.runs))
	for _, r := range s.runs {
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartTime.After(runs[j].StartTime)
	})
	return runs, nil
}

// AddIteration adds an iteration to the given run and story session, then
// persists the updated run to disk.
func (s *MemoryStore) AddIteration(runID string, iter Iteration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	run, ok := s.runs[runID]
	if !ok {
		return fmt.Errorf("state: run %q not found", runID)
	}

	// Find or create the agent session for this story.
	var session *AgentSession
	for _, sess := range run.Stories {
		if sess.StoryID == iter.StoryID {
			session = sess
			break
		}
	}
	if session == nil {
		session = &AgentSession{
			StoryID: iter.StoryID,
			Status:  StatusPending,
		}
		run.Stories = append(run.Stories, session)
	}

	session.Iterations = append(session.Iterations, iter)

	// Update session status based on iteration outcome.
	switch iter.Status {
	case StatusPassed:
		session.Status = StatusPassed
	case StatusFailed:
		session.Status = StatusFailed
	case StatusRunning:
		session.Status = StatusRunning
	}

	return s.persistRun(run)
}

// GetIterationsForStory returns all iterations for a story within a run.
func (s *MemoryStore) GetIterationsForStory(runID, storyID string) ([]Iteration, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	run, ok := s.runs[runID]
	if !ok {
		return nil, fmt.Errorf("state: run %q not found", runID)
	}

	for _, sess := range run.Stories {
		if sess.StoryID == storyID {
			return sess.Iterations, nil
		}
	}
	return nil, nil
}

// persistRun writes a single run to disk as a JSON file. Caller must hold s.mu.
func (s *MemoryStore) persistRun(run *Run) error {
	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		return fmt.Errorf("state: creating runs dir: %w", err)
	}

	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshaling run %s: %w", run.ID, err)
	}

	path := filepath.Join(s.baseDir, run.ID+".json")
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("state: writing run %s: %w", run.ID, err)
	}
	return nil
}

// loadFromDisk reads all JSON files from the base directory and populates the
// in-memory store.
func (s *MemoryStore) loadFromDisk() error {
	entries, err := os.ReadDir(s.baseDir)
	if os.IsNotExist(err) {
		return nil // no history yet
	}
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.baseDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("reading %s: %w", entry.Name(), err)
		}

		var run Run
		if err := json.Unmarshal(data, &run); err != nil {
			return fmt.Errorf("parsing %s: %w", entry.Name(), err)
		}
		s.runs[run.ID] = &run
	}
	return nil
}
