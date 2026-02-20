// Package git provides helpers for git branch creation, worktree management,
// and commit operations used by the agent loop.
package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// run executes a git command and returns combined output. It returns an error
// if the command exits non-zero.
func run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CurrentBranch returns the name of the currently checked-out branch.
func CurrentBranch() (string, error) {
	return run("rev-parse", "--abbrev-ref", "HEAD")
}

// CreateOrCheckoutBranch checks out an existing branch or creates a new one
// from the current HEAD.
func CreateOrCheckoutBranch(name string) error {
	// Try checking out existing branch first.
	_, err := run("checkout", name)
	if err == nil {
		return nil
	}
	// Branch doesn't exist — create it from HEAD.
	_, err = run("checkout", "-b", name)
	if err != nil {
		return fmt.Errorf("create or checkout branch %q: %w", name, err)
	}
	return nil
}

// WorktreeAdd creates a new git worktree at the given path on a new branch
// derived from the current HEAD. The branch name is based on the provided
// branch parameter.
func WorktreeAdd(path string, branch string) error {
	_, err := run("worktree", "add", "-b", branch, path)
	if err != nil {
		return fmt.Errorf("worktree add %q: %w", path, err)
	}
	return nil
}

// WorktreeRemove removes a git worktree at the given path and prunes stale
// worktree entries.
func WorktreeRemove(path string) error {
	_, err := run("worktree", "remove", "--force", path)
	if err != nil {
		return fmt.Errorf("worktree remove %q: %w", path, err)
	}
	return nil
}

// CommitAll stages all changes (tracked and untracked) and commits with the
// given message.
func CommitAll(message string) error {
	if _, err := run("add", "-A"); err != nil {
		return fmt.Errorf("commit all (stage): %w", err)
	}
	if _, err := run("commit", "-m", message); err != nil {
		return fmt.Errorf("commit all (commit): %w", err)
	}
	return nil
}
