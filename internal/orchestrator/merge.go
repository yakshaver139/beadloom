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
	mergeCtx, mergeCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer mergeCancel()

	merged := 0
	var mergedBranches []string

	for _, task := range wave.Tasks {
		if !completedIDs[task.TaskID] {
			continue
		}

		branch := task.BranchName
		commitMsg := fmt.Sprintf("beadloom: %s — %s", task.TaskID, task.Title)

		// Squash merge
		out, err := exec.CommandContext(mergeCtx, "git", "merge", "--squash", branch).CombinedOutput()
		if err != nil {
			exec.CommandContext(mergeCtx, "git", "merge", "--abort").Run()
			return merged, fmt.Errorf("merge conflict on %s: %s", branch, strings.TrimSpace(string(out)))
		}

		// Stage beads state
		exec.CommandContext(mergeCtx, "git", "add", ".beads/").Run()

		// Check if the squash merge staged anything
		if err := exec.CommandContext(mergeCtx, "git", "diff", "--cached", "--quiet").Run(); err == nil {
			fmt.Fprintf(os.Stderr, "  %s %s — already up to date\n", ui.Dim("‣"), ui.BoldMagenta(branch))
			continue
		}

		// Commit
		out, err = exec.CommandContext(mergeCtx, "git", "commit", "-m", commitMsg).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s %s — commit failed: %s\n", ui.Yellow("⚠"), ui.BoldMagenta(branch), strings.TrimSpace(string(out)))
			continue
		}

		fmt.Fprintf(os.Stderr, "  %s %s\n", ui.Green("✓"), ui.BoldMagenta(branch))
		merged++
		mergedBranches = append(mergedBranches, branch)
	}

	// Cleanup: delete merged branches and prune worktrees
	for _, branch := range mergedBranches {
		if err := exec.CommandContext(mergeCtx, "git", "branch", "-D", branch).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  %s delete branch %s: %v\n", ui.Yellow("⚠"), branch, err)
		}
	}
	exec.CommandContext(mergeCtx, "git", "worktree", "prune").Run()

	return merged, nil
}
