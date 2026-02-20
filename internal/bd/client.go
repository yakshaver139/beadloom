package bd

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Client wraps the bd CLI binary for task and worktree operations.
type Client struct {
	BdBin  string // path to bd binary (default: "bd")
	DbPath string // --db flag value (optional)
}

// NewClient creates a Client using the given bd binary path and database path.
func NewClient(bdBin, dbPath string) *Client {
	if bdBin == "" {
		bdBin = "bd"
	}
	return &Client{BdBin: bdBin, DbPath: dbPath}
}

func (c *Client) baseArgs() []string {
	if c.DbPath != "" {
		return []string{"--db", c.DbPath}
	}
	return nil
}

func (c *Client) run(args ...string) ([]byte, error) {
	all := append(c.baseArgs(), args...)
	cmd := exec.Command(c.BdBin, all...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("bd %s: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return out, nil
}

// RawTask is the JSON structure returned by bd list/show.
type RawTask struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Priority    int      `json:"priority"`
	Type        string   `json:"issue_type"`
	Labels      []string `json:"labels,omitempty"`
	Description string   `json:"description"`
	Acceptance  string   `json:"acceptance_criteria"`
	Notes       string   `json:"notes"`
	Design      string   `json:"design,omitempty"`
	Estimate    int      `json:"estimate,omitempty"` // minutes

	// Dependencies are NOT in bd JSON output — populated separately via DepList.
	BlockedBy []string `json:"-"`
	Blocks    []string `json:"-"`
}

// ListOpen returns all open tasks as JSON.
func (c *Client) ListOpen() ([]RawTask, error) {
	out, err := c.run("list", "--json", "--status", "open", "--limit", "0")
	if err != nil {
		return nil, err
	}
	var tasks []RawTask
	if err := json.Unmarshal(out, &tasks); err != nil {
		return nil, fmt.Errorf("parse bd list output: %w", err)
	}
	return tasks, nil
}

// ListReady returns tasks that are ready to work on (open, no blockers).
func (c *Client) ListReady() ([]RawTask, error) {
	out, err := c.run("ready", "--json", "--limit", "0")
	if err != nil {
		return nil, err
	}
	var tasks []RawTask
	if err := json.Unmarshal(out, &tasks); err != nil {
		return nil, fmt.Errorf("parse bd ready output: %w", err)
	}
	return tasks, nil
}

// Show returns full details for a single task.
func (c *Client) Show(id string) (*RawTask, error) {
	out, err := c.run("show", id, "--json")
	if err != nil {
		return nil, err
	}
	var task RawTask
	if err := json.Unmarshal(out, &task); err != nil {
		return nil, fmt.Errorf("parse bd show output: %w", err)
	}
	return &task, nil
}

// DepListItem is an issue returned by bd dep list --json.
type DepListItem struct {
	ID string `json:"id"`
}

// Deps returns the dependency edges for a task.
// blockedBy = what this task depends on (bd dep list <id> --direction=down)
// blocks = what depends on this task (bd dep list <id> --direction=up)
func (c *Client) Deps(id string) (blocks, blockedBy []string, err error) {
	// Get what blocks this task (its dependencies)
	downOut, err := c.run("dep", "list", id, "--direction=down", "--json")
	if err != nil {
		// dep list may fail if no deps exist; treat as empty
		downOut = []byte("[]")
	}
	var downDeps []DepListItem
	if err := json.Unmarshal(downOut, &downDeps); err != nil {
		return nil, nil, fmt.Errorf("parse bd dep list (down): %w", err)
	}
	for _, d := range downDeps {
		blockedBy = append(blockedBy, d.ID)
	}

	// Get what this task blocks (its dependents)
	upOut, err2 := c.run("dep", "list", id, "--direction=up", "--json")
	if err2 != nil {
		upOut = []byte("[]")
	}
	var upDeps []DepListItem
	if err := json.Unmarshal(upOut, &upDeps); err != nil {
		return nil, nil, fmt.Errorf("parse bd dep list (up): %w", err)
	}
	for _, d := range upDeps {
		blocks = append(blocks, d.ID)
	}

	return blocks, blockedBy, nil
}

// Update changes a task's status and/or adds notes.
// For closing, use bd close; for other status changes, use bd update --status.
func (c *Client) Update(id string, status string, notes string) error {
	if status == "closed" {
		args := []string{"close", id}
		if notes != "" {
			args = append(args, "--reason", notes)
		}
		_, err := c.run(args...)
		return err
	}

	args := []string{"update", id}
	if status != "" {
		args = append(args, "--status", status)
	}
	if notes != "" {
		args = append(args, "--append-notes", notes)
	}
	_, err := c.run(args...)
	return err
}

// Close closes a task with an optional reason.
func (c *Client) Close(id string, reason string) error {
	args := []string{"close", id}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	_, err := c.run(args...)
	return err
}

// AddDep adds a dependency edge: blockedID is blocked by blockerID.
func (c *Client) AddDep(blockedID, blockerID string) error {
	_, err := c.run("dep", "add", blockedID, blockerID)
	return err
}

// WorktreeCreate creates a git worktree via bd.
// name is the worktree directory name (created at ./<name>).
// branch is the git branch name for the worktree.
func (c *Client) WorktreeCreate(name, branch string) error {
	args := []string{"worktree", "create", name}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	_, err := c.run(args...)
	return err
}

// WorktreeRemove removes a git worktree via bd.
func (c *Client) WorktreeRemove(name string) error {
	_, err := c.run("worktree", "remove", name)
	return err
}

// WorktreeInfo holds information about a worktree.
type WorktreeInfo struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

// ShowHuman returns the human-readable bd show output for use in commit messages.
func (c *Client) ShowHuman(id string) (string, error) {
	out, err := c.run("show", id)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Sync runs bd sync to clean up worktree state.
func (c *Client) Sync() error {
	_, err := c.run("sync")
	return err
}

// WorktreeList lists all worktrees.
func (c *Client) WorktreeList() ([]WorktreeInfo, error) {
	out, err := c.run("worktree", "list", "--json")
	if err != nil {
		return nil, err
	}
	var wts []WorktreeInfo
	if err := json.Unmarshal(out, &wts); err != nil {
		return nil, fmt.Errorf("parse bd worktree list: %w", err)
	}
	return wts, nil
}
