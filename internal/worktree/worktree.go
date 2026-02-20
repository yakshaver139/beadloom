package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joshharrison/beadloom/internal/bd"
)

// Manager handles git worktree lifecycle via the bd CLI.
type Manager struct {
	BaseDir string // e.g., ".worktrees/"
	Client  *bd.Client
}

// NewManager creates a new worktree Manager.
func NewManager(baseDir string, client *bd.Client) *Manager {
	if baseDir == "" {
		baseDir = ".worktrees"
	}
	return &Manager{BaseDir: baseDir, Client: client}
}

// Create creates a new worktree for the given task.
// Returns the path to the worktree directory.
func (m *Manager) Create(name, branch string) (string, error) {
	wtPath := filepath.Join(m.BaseDir, name)

	// Check if worktree already exists
	if _, err := os.Stat(wtPath); err == nil {
		return wtPath, nil // reuse existing
	}

	// bd worktree create accepts a path (e.g., .worktrees/bd-123)
	// and creates the directory with a beads redirect automatically
	if err := m.Client.WorktreeCreate(wtPath, branch); err != nil {
		return "", fmt.Errorf("create worktree %s: %w", name, err)
	}

	// Verify the directory was actually created — bd may exit 0
	// without creating the directory if git state is inconsistent
	// (e.g., stale worktree entries from concurrent removal)
	if _, err := os.Stat(wtPath); err != nil {
		return "", fmt.Errorf("worktree %s: directory missing after creation", name)
	}

	return wtPath, nil
}

// Remove removes a worktree by name.
func (m *Manager) Remove(name string) error {
	wtPath := filepath.Join(m.BaseDir, name)
	if err := m.Client.WorktreeRemove(wtPath); err != nil {
		// Fall back to manual removal if bd worktree remove fails
		return os.RemoveAll(wtPath)
	}
	return nil
}

// List returns all worktrees managed by beadloom (those under BaseDir).
func (m *Manager) List() ([]bd.WorktreeInfo, error) {
	all, err := m.Client.WorktreeList()
	if err != nil {
		return nil, err
	}

	var managed []bd.WorktreeInfo
	for _, wt := range all {
		if strings.HasPrefix(wt.Path, m.BaseDir) || strings.Contains(wt.Branch, "beadloom/") {
			managed = append(managed, wt)
		}
	}
	return managed, nil
}

// Cleanup removes all beadloom worktrees.
func (m *Manager) Cleanup() error {
	managed, err := m.List()
	if err != nil {
		// If listing fails, try to just remove the directory
		return os.RemoveAll(m.BaseDir)
	}

	var errs []string
	for _, wt := range managed {
		if err := m.Client.WorktreeRemove(wt.Path); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", wt.Path, err))
		}
	}

	// Also remove the base directory if empty
	os.Remove(m.BaseDir)

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Path returns the worktree path for a given task name.
func (m *Manager) Path(name string) string {
	return filepath.Join(m.BaseDir, name)
}

// ListBranches returns all beadloom/* git branches.
func (m *Manager) ListBranches() ([]string, error) {
	cmd := exec.Command("git", "branch", "--list", "beadloom/*", "--format", "%(refname:short)")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list beadloom branches: %w", err)
	}

	var branches []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}

// DeleteBranch deletes a git branch.
func (m *Manager) DeleteBranch(branch string) error {
	cmd := exec.Command("git", "branch", "-D", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("delete branch %s: %w\n%s", branch, err, string(out))
	}
	return nil
}
