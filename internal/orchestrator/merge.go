package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/joshharrison/beadloom/internal/planner"
	"github.com/joshharrison/beadloom/internal/ui"
)

// mergeWaveBranches squash-merges branches from completed tasks in the given wave
// into the current branch. Returns the count of merged branches.
// On merge conflict it aborts the merge and returns an error.
func (o *Orchestrator) mergeWaveBranches(ctx context.Context, wave planner.ExecutionWave, completedIDs map[string]bool) (int, error) {
	_, mergeCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer mergeCancel()

	trace := o.Config.GitTrace
	merged := 0
	var mergedBranches []string

	// Commit the dolt working set inside .beads/ so dolt's git merge hooks
	// don't warn about "local changes would be stomped". The bd CLI (close,
	// update) modifies the dolt working set but doesn't always commit it;
	// if the working set is dirty when git merge triggers dolt's hooks, dolt
	// refuses to merge its internal import branch.
	doltCommit := exec.Command("dolt", "add", ".")
	doltCommit.Dir = ".beads"
	if out, err := doltCommit.CombinedOutput(); err != nil && trace {
		fmt.Fprintf(os.Stderr, "  %s dolt add: %s\n", ui.Dim("GIT>"), strings.TrimSpace(string(out)))
	}
	doltCommit = exec.Command("dolt", "commit", "--allow-empty", "-m", "beadloom: sync before merge")
	doltCommit.Dir = ".beads"
	if out, err := doltCommit.CombinedOutput(); err != nil && trace {
		fmt.Fprintf(os.Stderr, "  %s dolt commit: %s\n", ui.Dim("GIT>"), strings.TrimSpace(string(out)))
	}

	for _, task := range wave.Tasks {
		if !completedIDs[task.TaskID] {
			continue
		}

		branch := task.BranchName
		commitMsg := fmt.Sprintf("beadloom: %s — %s", task.TaskID, task.Title)

		// Squash merge
		out, err := runGit(trace, ".", "merge", "--squash", branch)
		if err != nil {
			runGit(trace, ".", "merge", "--abort")
			return merged, fmt.Errorf("merge conflict on %s: %s", branch, strings.TrimSpace(string(out)))
		}

		// Stage beads state (but not the redirect file)
		runGit(trace, ".", "add", ".beads/")
		runGit(trace, ".", "reset", "HEAD", "--", ".beads/redirect")

		// Unstage beadloom.log if it was brought in by the squash
		runGit(trace, ".", "reset", "HEAD", "--", "beadloom.log")

		// Check if the squash merge staged anything
		if _, err := runGit(trace, ".", "diff", "--cached", "--quiet"); err == nil {
			fmt.Fprintf(os.Stderr, "  %s %s — already up to date\n", ui.Dim("‣"), ui.BoldMagenta(branch))
			continue
		}

		// Show what will be committed
		runGit(true, ".", "status", "--short")

		// Commit
		out, err = runGit(trace, ".", "commit", "-m", commitMsg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s %s — commit failed: %s\n", ui.Yellow("⚠"), ui.BoldMagenta(branch), strings.TrimSpace(string(out)))
			continue
		}

		fmt.Fprintf(os.Stderr, "  %s %s\n", ui.Green("✓"), ui.BoldMagenta(branch))
		merged++
		mergedBranches = append(mergedBranches, branch)
	}

	// Cleanup: force-remove worktrees first so branches aren't "checked out",
	// then prune stale worktree metadata, then delete the branches.
	for _, branch := range mergedBranches {
		taskID := strings.TrimPrefix(branch, "beadloom/")
		wtPath := o.Worktrees.Path(taskID)
		runGit(trace, ".", "worktree", "remove", "--force", wtPath)
	}
	runGit(trace, ".", "worktree", "prune")
	for _, branch := range mergedBranches {
		if _, err := runGit(trace, ".", "branch", "-D", branch); err != nil {
			fmt.Fprintf(os.Stderr, "  %s delete branch %s: %v\n", ui.Yellow("⚠"), branch, err)
		}
	}

	return merged, nil
}
