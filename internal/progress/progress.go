// Package progress manages progress.txt files for tracking agent loop
// iterations and provides archiving of previous runs when the branch changes.
package progress

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/radvoogh/ralph-wiggo/internal/claude"
)

// InitIfNeeded creates a progress.txt file with a header if it does not
// already exist. If it already exists, this is a no-op.
func InitIfNeeded(path, project, branch string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}

	header := fmt.Sprintf("# Ralph Progress Log\nProject: %s\nBranch: %s\nStarted: %s\n\n---\n",
		project, branch, time.Now().Format(time.RFC1123))

	return os.WriteFile(path, []byte(header), 0644)
}

// AppendEntry appends an iteration summary to the progress.txt file.
func AppendEntry(path, storyID string, passed bool, events []claude.StreamEvent) error {
	status := "FAIL"
	if passed {
		status = "PASS"
	}

	summary := summarizeEvents(events)

	entry := fmt.Sprintf("\n## %s - %s [%s]\n%s---\n",
		time.Now().Format("2006-01-02 15:04:05"), storyID, status, summary)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening progress file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("writing progress entry: %w", err)
	}
	return nil
}

// summarizeEvents produces a brief text summary from a slice of stream events.
func summarizeEvents(events []claude.StreamEvent) string {
	var sb strings.Builder
	var toolsUsed []string

	for _, evt := range events {
		switch evt.Type {
		case claude.EventToolUse:
			if evt.ToolName != "" {
				toolsUsed = append(toolsUsed, evt.ToolName)
			}
		case claude.EventError:
			sb.WriteString(fmt.Sprintf("- Error: %s\n", evt.Message))
		}
	}

	if len(toolsUsed) > 0 {
		// Deduplicate and count tool usage.
		counts := make(map[string]int)
		for _, t := range toolsUsed {
			counts[t]++
		}
		sb.WriteString("- Tools used: ")
		first := true
		for name, count := range counts {
			if !first {
				sb.WriteString(", ")
			}
			sb.WriteString(fmt.Sprintf("%s(%d)", name, count))
			first = false
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// readProgressBranch reads the "Branch:" line from a progress.txt file header.
func readProgressBranch(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Branch: ") {
			return strings.TrimPrefix(line, "Branch: "), nil
		}
		// Stop searching after a few header lines.
		if strings.HasPrefix(line, "---") {
			break
		}
	}
	return "", fmt.Errorf("no Branch header found in %s", path)
}

// ArchiveIfBranchChanged checks whether an existing progress.txt was created
// for a different branch than the current PRD. If so, it moves progress.txt
// (and prd.json if present) to an archive directory. Returns true if archiving
// was performed.
func ArchiveIfBranchChanged(workDir, prdPath, progressPath, currentBranch string) (bool, error) {
	// Check if progress.txt exists.
	if _, err := os.Stat(progressPath); os.IsNotExist(err) {
		return false, nil
	}

	// Read the branch from the progress.txt header.
	oldBranch, err := readProgressBranch(progressPath)
	if err != nil {
		return false, nil // can't determine old branch, skip archiving
	}

	// If branch names match, no archiving needed.
	if oldBranch == currentBranch {
		return false, nil
	}

	// Build the archive directory name: archive/YYYY-MM-DD-feature-name/
	featureName := strings.TrimPrefix(oldBranch, "ralph/")
	archiveDir := filepath.Join(workDir, "archive",
		time.Now().Format("2006-01-02")+"-"+featureName)

	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return false, fmt.Errorf("creating archive dir: %w", err)
	}

	// Move progress.txt to archive.
	if err := os.Rename(progressPath, filepath.Join(archiveDir, "progress.txt")); err != nil {
		return false, fmt.Errorf("archiving progress.txt: %w", err)
	}

	// Move prd.json to archive if it exists (it may have been overwritten already).
	// We copy instead of move since the current PRD needs to stay.
	if prdData, readErr := os.ReadFile(prdPath); readErr == nil {
		_ = os.WriteFile(filepath.Join(archiveDir, "prd.json"), prdData, 0644)
	}

	return true, nil
}
