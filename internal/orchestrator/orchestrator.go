package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joshharrison/beadloom/internal/planner"
	"github.com/joshharrison/beadloom/internal/state"
	"github.com/joshharrison/beadloom/internal/ui"
	"github.com/joshharrison/beadloom/internal/worktree"
)

// Orchestrator manages the execution of an ExecutionPlan.
type Orchestrator struct {
	Plan       *planner.ExecutionPlan
	Worktrees  *worktree.Manager
	Config     Config
	State      *state.RunState
	sessions   map[string]*AgentSession
	closed     map[string]bool // tasks already closed in beads (skip on restart)
	mu         sync.Mutex
	ctx        context.Context
	cancelFunc context.CancelFunc
}

// New creates a new Orchestrator.
func New(plan *planner.ExecutionPlan, wm *worktree.Manager, cfg Config) *Orchestrator {
	if cfg.ClaudeBin == "" {
		cfg.ClaudeBin = "claude"
	}
	if cfg.MaxParallel == 0 {
		cfg.MaxParallel = 4
	}
	if cfg.TimeoutPerTask == 0 {
		cfg.TimeoutPerTask = 30 * time.Minute
	}

	return &Orchestrator{
		Plan:      plan,
		Worktrees: wm,
		Config:    cfg,
		sessions:  make(map[string]*AgentSession),
	}
}

// Run executes the plan using wave-barrier scheduling. All tasks in wave N
// complete before wave N+1 starts. At each wave boundary, automerge mode
// merges automatically; interactive mode calls the OnWaveComplete callback.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.ctx, o.cancelFunc = context.WithCancel(ctx)
	defer o.cancelFunc()

	// Detect tasks already closed in beads so we skip them on restart
	o.closed = make(map[string]bool)
	for id := range o.Plan.Tasks {
		task, err := o.Worktrees.Client.Show(id)
		if err == nil && task.Status == "closed" {
			o.closed[id] = true
		}
	}
	if len(o.closed) > 0 {
		fmt.Fprintf(os.Stderr, "  %s %d tasks already closed, will be skipped\n", ui.Dim("↩"), len(o.closed))
	}

	// Check previous state file for tasks completed in a prior run
	if prev := state.LoadCompletedTaskIDs(); len(prev) > 0 {
		added := 0
		for id := range prev {
			if _, inPlan := o.Plan.Tasks[id]; inPlan && !o.closed[id] {
				o.closed[id] = true
				added++
			}
		}
		if added > 0 {
			fmt.Fprintf(os.Stderr, "  %s %d tasks completed in previous run, will be skipped\n", ui.Dim("↩"), added)
		}
	}

	// Check git branch state for tasks completed but not recorded
	for id, task := range o.Plan.Tasks {
		if o.closed[id] {
			continue
		}
		// Check if the branch exists
		if _, err := runGit(o.Config.GitTrace, ".", "rev-parse", "--verify", task.BranchName); err != nil {
			continue // branch doesn't exist
		}
		// Check if the branch is an ancestor of HEAD (already merged)
		if err := exec.Command("git", "merge-base", "--is-ancestor", task.BranchName, "HEAD").Run(); err == nil {
			// Already merged — clean up worktree and branch
			fmt.Fprintf(os.Stderr, "  %s %s already merged, cleaning up\n", ui.Dim("↩"), task.BranchName)
			wtPath := o.Worktrees.Path(task.WorktreeName)
			runGit(o.Config.GitTrace, ".", "worktree", "remove", "--force", wtPath)
			runGit(o.Config.GitTrace, ".", "branch", "-D", task.BranchName)
			o.closed[id] = true
		} else {
			// Branch exists with commits but not merged — task work is done,
			// skip execution but mark for merge at wave boundary
			o.closed[id] = true
			fmt.Fprintf(os.Stderr, "  %s %s has unmerged work from previous run\n", ui.Dim("↩"), task.BranchName)
		}
	}
	runGit(o.Config.GitTrace, ".", "worktree", "prune")

	// Initialize state
	st, err := state.New(o.Plan.ID, o.Plan.TotalWaves, o.Plan.TotalTasks)
	if err != nil {
		return fmt.Errorf("init state: %w", err)
	}
	o.State = st

	// Persist the plan so `bdl status` can load it without rebuilding from beads
	if err := state.SavePlan(o.Plan); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	// Archive state+plan to history when the run ends (success, failure, or cancel)
	defer func() {
		if archiveErr := state.Archive(); archiveErr != nil {
			fmt.Fprintf(os.Stderr, "  %s archive run state: %v\n", ui.Yellow("⚠️  Warning:"), archiveErr)
		}
	}()

	return o.runWaveMode()
}

// runWaveMode executes waves sequentially with automerge at wave boundaries.
// All tasks in wave N run in parallel, then their branches are squash-merged
// into the current branch before wave N+1 starts.
func (o *Orchestrator) runWaveMode() error {
	totalTasks := len(o.Plan.Tasks)
	done := make(chan taskResult, totalTasks)
	sem := make(chan struct{}, o.Config.MaxParallel)
	var wtMu sync.Mutex

	mergeMode := "interactive merge"
	if o.Config.Automerge {
		mergeMode = "automerge on"
	}
	fmt.Fprintf(os.Stderr, "\n🚀 %s (%d tasks, %d waves, max %d parallel, %s)\n",
		ui.BoldCyan("Wave-barrier scheduler started"), totalTasks, len(o.Plan.Waves), o.Config.MaxParallel, mergeMode)

	finished := make(map[string]bool, totalTasks)
	pending := make(map[string]int, totalTasks)
	for id := range o.Plan.Tasks {
		pending[id] = len(o.Plan.Deps.Predecessors[id])
	}

	for _, wave := range o.Plan.Waves {
		if err := o.ctx.Err(); err != nil {
			o.State.SetStatus("cancelled")
			return fmt.Errorf("cancelled: %w", err)
		}

		// Mark already-closed tasks as finished so they're skipped
		for _, task := range wave.Tasks {
			if o.closed[task.TaskID] {
				finished[task.TaskID] = true
			}
		}

		// Count actionable tasks in this wave
		actionable := 0
		for _, task := range wave.Tasks {
			if !finished[task.TaskID] {
				actionable++
			}
		}
		if actionable == 0 {
			fmt.Fprintf(os.Stderr, "\n🌊 %s %d — %s\n", ui.BoldWhite("Wave"), wave.Index+1, ui.Dim("all tasks already completed, skipping"))
			continue
		}

		fmt.Fprintf(os.Stderr, "\n🌊 %s %d (%d tasks)\n", ui.BoldWhite("Wave"), wave.Index+1, actionable)

		// Dispatch all tasks in this wave
		inflight := 0
		for _, task := range wave.Tasks {
			if finished[task.TaskID] {
				continue
			}
			fmt.Fprintf(os.Stderr, "  ▶ %s %s\n", ui.TaskPrefix(task.TaskID), task.Title)
			o.dispatch(task, sem, &wtMu, done)
			inflight++
		}

		// Wait for all tasks in this wave to complete
		completedIDs := make(map[string]bool)
		for inflight > 0 {
			result := <-done
			inflight--
			finished[result.TaskID] = true

			if result.Err != nil {
				if result.Critical {
					fmt.Fprintf(os.Stderr, "  💀 %s critical task failed, cancelling run\n", ui.TaskPrefix(result.TaskID))
					o.cancelFunc()
					for inflight > 0 {
						<-done
						inflight--
					}
					o.State.SetStatus("failed")
					return fmt.Errorf("critical task %s failed: %w", result.TaskID, result.Err)
				}
				fmt.Fprintf(os.Stderr, "  %s non-critical task %s failed: %v\n", ui.Yellow("⚠️  Warning:"), result.TaskID, result.Err)
				skipped := o.cascadeSkip(result.TaskID, finished, pending)
				_ = skipped
			} else {
				completedIDs[result.TaskID] = true
			}
		}

		o.updateCurrentWave()

		// Include tasks from previous runs that have unmerged branches
		for _, task := range wave.Tasks {
			if !completedIDs[task.TaskID] && o.closed[task.TaskID] {
				// Check if branch still exists (needs merging)
				if err := exec.Command("git", "rev-parse", "--verify", task.BranchName).Run(); err == nil {
					completedIDs[task.TaskID] = true
				}
			}
		}

		// Merge completed branches from this wave
		if len(completedIDs) > 0 {
			var branches []string
			for _, task := range wave.Tasks {
				if completedIDs[task.TaskID] {
					branches = append(branches, task.BranchName)
				}
			}

			shouldMerge := o.Config.Automerge
			if !shouldMerge && o.Config.OnWaveComplete != nil {
				shouldMerge = o.Config.OnWaveComplete(wave.Index, branches)
			}

			if shouldMerge {
				fmt.Fprintf(os.Stderr, "\n🧵 Merging wave %d branches...\n", wave.Index+1)
				merged, err := o.mergeWaveBranches(o.ctx, wave, completedIDs)
				if err != nil {
					o.State.SetStatus("failed")
					return fmt.Errorf("merge wave %d: %w", wave.Index+1, err)
				}
				fmt.Fprintf(os.Stderr, "  🏁 Merged %d/%d branches from wave %d\n", merged, len(completedIDs), wave.Index+1)
			}
		}
	}

	o.updateCurrentWave()
	o.State.SetStatus("completed")

	// If a sync branch is configured, merge beads metadata into main
	if o.Config.SyncBranch != "" {
		fmt.Fprintf(os.Stderr, "\n🔄 Syncing beads metadata branch (%s)...\n", o.Config.SyncBranch)
		if err := o.Worktrees.Client.SyncMerge(); err != nil {
			fmt.Fprintf(os.Stderr, "  %s bd sync --merge: %v\n", ui.Yellow("⚠"), err)
		} else {
			fmt.Fprintf(os.Stderr, "  %s Beads metadata synced\n", ui.Green("✓"))
		}
	}

	return nil
}

// runDynamic executes the plan using a dynamic dependency-tracking scheduler.
// Each task is dispatched the moment all its predecessors complete,
// eliminating wave-barrier delays.
func (o *Orchestrator) runDynamic() error {
	// Build pending count for each task (number of unfinished predecessors)
	pending := make(map[string]int, len(o.Plan.Tasks))
	for id := range o.Plan.Tasks {
		pending[id] = len(o.Plan.Deps.Predecessors[id])
	}

	done := make(chan taskResult, len(o.Plan.Tasks))
	sem := make(chan struct{}, o.Config.MaxParallel)
	var wtMu sync.Mutex // serializes worktree create/remove (git lock)

	totalTasks := len(o.Plan.Tasks)
	inflight := 0
	totalDone := 0 // completed + failed + skipped
	finished := make(map[string]bool, totalTasks)

	fmt.Fprintf(os.Stderr, "\n🚀 %s (%d tasks, max %d parallel)\n", ui.BoldCyan("Dynamic scheduler started"), totalTasks, o.Config.MaxParallel)

	// Mark already-closed tasks as finished and decrement successor pending counts
	for id := range o.closed {
		finished[id] = true
		totalDone++
		for _, succID := range o.Plan.Deps.Successors[id] {
			pending[succID]--
		}
	}

	// Dispatch all root tasks (pending == 0)
	for id, count := range pending {
		if count == 0 && !finished[id] {
			task := o.Plan.Tasks[id]
			fmt.Fprintf(os.Stderr, "  ▶ %s %s\n", ui.TaskPrefix(task.TaskID), task.Title)
			o.dispatch(*task, sem, &wtMu, done)
			inflight++
		}
	}

	// Main event loop
	for totalDone < totalTasks {
		if err := o.ctx.Err(); err != nil {
			o.State.SetStatus("cancelled")
			return fmt.Errorf("cancelled: %w", err)
		}

		// If nothing is in flight but tasks remain, they're unreachable
		if inflight == 0 {
			for id := range pending {
				if !finished[id] {
					o.markSkipped(id)
					finished[id] = true
					totalDone++
				}
			}
			break
		}

		result := <-done
		inflight--
		finished[result.TaskID] = true
		totalDone++

		if result.Err != nil {
			// Task failed
			if result.Critical {
				// Critical failure: cancel everything, drain inflight
				fmt.Fprintf(os.Stderr, "  💀 %s critical task failed, cancelling run\n", ui.TaskPrefix(result.TaskID))
				o.cancelFunc()
				for inflight > 0 {
					<-done
					inflight--
					totalDone++
				}
				o.State.SetStatus("failed")
				return fmt.Errorf("critical task %s failed: %w", result.TaskID, result.Err)
			}

			// Non-critical failure: cascade-skip successors
			fmt.Fprintf(os.Stderr, "  %s non-critical task %s failed: %v\n", ui.Yellow("⚠️  Warning:"), result.TaskID, result.Err)
			skipped := o.cascadeSkip(result.TaskID, finished, pending)
			totalDone += skipped
			o.updateCurrentWave()
		} else {
			// Task succeeded: update wave progress, then dispatch ready successors
			o.updateCurrentWave()
			for _, succID := range o.Plan.Deps.Successors[result.TaskID] {
				if finished[succID] {
					continue
				}
				pending[succID]--
				if pending[succID] == 0 {
					task := o.Plan.Tasks[succID]
					fmt.Fprintf(os.Stderr, "  ▶ %s %s\n", ui.TaskPrefix(task.TaskID), task.Title)
					o.dispatch(*task, sem, &wtMu, done)
					inflight++
				}
			}
		}
	}

	o.updateCurrentWave()
	o.State.SetStatus("completed")
	return nil
}

// dispatch launches a task in a goroutine: acquire semaphore, create worktree,
// execute, cleanup worktree on success, send result on done channel.
func (o *Orchestrator) dispatch(task planner.PlannedTask, sem chan struct{}, wtMu *sync.Mutex, done chan<- taskResult) {
	go func() {
		sem <- struct{}{}        // acquire semaphore
		defer func() { <-sem }() // release semaphore

		// Create worktree (serialized via wtMu)
		wtMu.Lock()
		wtPath, err := o.Worktrees.Create(task.WorktreeName, task.BranchName)
		wtMu.Unlock()
		if err != nil {
			o.updateSession(task.TaskID, StatusFailed, 0, "")
			fmt.Fprintf(os.Stderr, "  %s %v\n", ui.Red("❌ Task error:"), fmt.Errorf("task %s: create worktree: %w", task.TaskID, err))
			done <- taskResult{TaskID: task.TaskID, Err: err, Critical: o.isTaskCritical(task.TaskID)}
			return
		}

		// Execute the task
		err = o.executeTask(task, wtPath)

		// Cleanup worktree on success in automerge mode only.
		// In interactive mode, preserve worktrees until the user
		// confirms merge at the wave boundary.
		if err == nil && o.Config.Automerge {
			wtMu.Lock()
			if rmErr := o.Worktrees.Remove(task.WorktreeName); rmErr != nil {
				fmt.Fprintf(os.Stderr, "  %s failed to remove worktree %s: %v\n", ui.Yellow("⚠️  Warning:"), task.WorktreeName, rmErr)
			}
			wtMu.Unlock()
		}

		done <- taskResult{TaskID: task.TaskID, Err: err, Critical: o.isTaskCritical(task.TaskID)}
	}()
}

// cascadeSkip performs BFS through the successor graph from a failed task,
// marking all transitively dependent tasks as skipped. Returns count of skipped tasks.
func (o *Orchestrator) cascadeSkip(failedID string, finished map[string]bool, pending map[string]int) int {
	skipped := 0
	queue := []string{}

	// Seed the queue with direct successors of the failed task
	for _, succID := range o.Plan.Deps.Successors[failedID] {
		if !finished[succID] {
			queue = append(queue, succID)
		}
	}

	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]

		if finished[id] {
			continue
		}

		o.markSkipped(id)
		finished[id] = true
		skipped++

		// Add successors of the skipped task
		for _, succID := range o.Plan.Deps.Successors[id] {
			if !finished[succID] {
				queue = append(queue, succID)
			}
		}
	}

	return skipped
}

// markSkipped records a task as skipped in both session state, persistent state, and beads.
func (o *Orchestrator) markSkipped(taskID string) {
	now := time.Now()
	o.mu.Lock()
	o.sessions[taskID] = &AgentSession{
		TaskID:     taskID,
		Status:     StatusSkipped,
		FinishedAt: now,
	}
	o.mu.Unlock()

	st := &state.SessionState{
		Status:     state.StatusSkipped,
		FinishedAt: &now,
	}
	o.State.UpdateSession(taskID, st)

	// Update beads — move back to open so it can be retried in a future run
	if o.Worktrees != nil {
		if err := o.Worktrees.Client.Update(taskID, "open", ""); err != nil {
			fmt.Fprintf(os.Stderr, "  %s bd update %s --status open: %v\n", ui.Yellow("⚠"), taskID, err)
		}
	}

	fmt.Fprintf(os.Stderr, "  ⊘ %s %s\n", ui.TaskPrefix(taskID), ui.Yellow("Skipped (predecessor failed)"))
}

// updateCurrentWave computes the current wave from task session states and
// persists it. The wave is the index of the first wave with incomplete tasks,
// or the last wave index when all are done.
func (o *Orchestrator) updateCurrentWave() {
	wave := 0
	for _, w := range o.Plan.Waves {
		allDone := true
		for _, task := range w.Tasks {
			ss := o.State.GetSession(task.TaskID)
			if ss == nil {
				allDone = false
				break
			}
			switch ss.Status {
			case state.StatusCompleted, state.StatusFailed, state.StatusSkipped:
				// terminal
			default:
				allDone = false
			}
			if !allDone {
				break
			}
		}
		if !allDone {
			wave = w.Index
			break
		}
		wave = w.Index
	}
	o.State.SetWave(wave)
}

// isTaskCritical checks if a task is on the critical path.
func (o *Orchestrator) isTaskCritical(taskID string) bool {
	if task, ok := o.Plan.Tasks[taskID]; ok {
		return task.IsCritical
	}
	return false
}

// executeTask runs a single task: spawn agent in the given worktree and wait.
func (o *Orchestrator) executeTask(task planner.PlannedTask, wtPath string) error {
	startTime := time.Now()

	// Mark task as in-progress in beads
	if err := o.Worktrees.Client.Update(task.TaskID, "in-progress", ""); err != nil {
		fmt.Fprintf(os.Stderr, "  %s bd update %s --status in-progress: %v\n", ui.Yellow("⚠️  Warning:"), task.TaskID, err)
	}

	// Spawn agent
	runner, err := o.spawnAgent(task, wtPath)
	if err != nil {
		o.updateSession(task.TaskID, StatusFailed, 0, wtPath)
		return fmt.Errorf("spawn agent: %w", err)
	}
	defer runner.Cleanup()

	// Wait for completion
	err = runner.Cmd.Wait()

	// If the agent exited cleanly but child processes kept pipes open,
	// WaitDelay closes them after 5s. This is not a task failure.
	if err != nil && errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}

	// If the agent emitted a "result" event but the process was killed by
	// our grace-period timer (e.g. CLI hung waiting for a background task
	// like a dev server), treat as success.
	if err != nil && runner.resultSeen.Load() {
		err = nil
	}

	finishedAt := time.Now()
	elapsed := finishedAt.Sub(startTime)
	exitCode := 0
	status := StatusCompleted

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		status = StatusFailed
	}

	if status == StatusCompleted {
		// Ensure the task is closed in beads — the agent should have run
		// bd close, but we do it here as a fallback in case it didn't.
		if closeErr := o.Worktrees.Client.Close(task.TaskID, "Completed by beadloom agent"); closeErr != nil {
			fmt.Fprintf(os.Stderr, "  %s bd close %s: %v\n", ui.Yellow("⚠️  Warning:"), task.TaskID, closeErr)
		}

		// Auto-commit all changes the agent made in the worktree so that
		// the branch has commits for the squash-merge step.
		if commitErr := autoCommit(wtPath, task.TaskID, task.Title, o.Config.GitTrace); commitErr != nil {
			fmt.Fprintf(os.Stderr, "  %s auto-commit %s: %v\n", ui.Yellow("⚠️  Warning:"), task.TaskID, commitErr)
		}

		fmt.Fprintf(os.Stderr, "  ✅ %s %s %s\n", ui.TaskPrefix(task.TaskID), ui.Green("Completed"), ui.Dim(fmt.Sprintf("(%.1fs)", elapsed.Seconds())))
	} else {
		fmt.Fprintf(os.Stderr, "  ❌ %s %s %s\n", ui.TaskPrefix(task.TaskID), ui.Red(fmt.Sprintf("Failed exit %d", exitCode)), ui.Dim(fmt.Sprintf("(%.1fs)", elapsed.Seconds())))
	}

	o.mu.Lock()
	s := o.sessions[task.TaskID]
	s.Status = status
	s.FinishedAt = finishedAt
	s.ExitCode = exitCode
	o.mu.Unlock()

	// Update persistent state
	st := &state.SessionState{
		Status:     state.SessionStatus(status),
		Worktree:   wtPath,
		Branch:     task.BranchName,
		StartedAt:  &s.StartedAt,
		FinishedAt: &finishedAt,
		ExitCode:   exitCode,
		LogFile:    s.LogFile,
	}
	o.State.UpdateSession(task.TaskID, st)

	if status == StatusFailed {
		return fmt.Errorf("agent exited with code %d", exitCode)
	}
	return nil
}

// runGit executes a git command in the given directory. When trace is true it
// logs the full command and output to stderr for debugging merge issues.
func runGit(trace bool, dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if trace {
		fmt.Fprintf(os.Stderr, "  %s [%s] git %s\n", ui.Dim("GIT>"), filepath.Base(dir), strings.Join(args, " "))
	}
	out, err := cmd.CombinedOutput()
	if trace && len(out) > 0 {
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			fmt.Fprintf(os.Stderr, "  %s %s\n", ui.Dim("GIT<"), line)
		}
	}
	if trace && err != nil {
		fmt.Fprintf(os.Stderr, "  %s exit: %v\n", ui.Dim("GIT!"), err)
	}
	return out, err
}

// autoCommit stages and commits all changes in the worktree so the branch
// has content for the squash-merge step. It's a no-op if there's nothing to commit.
func autoCommit(wtPath, taskID, title string, trace bool) error {
	// Stage everything (including new files)
	if out, err := runGit(trace, wtPath, "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w\n%s", err, out)
	}

	// Unstage files that must not be committed:
	// - beadloom.log: agent log file, local to each worktree
	// - .beads/redirect: worktree-local pointer to the main beads DB;
	//   committing it creates redirect chains in downstream worktrees
	runGit(trace, wtPath, "reset", "HEAD", "--", "beadloom.log", ".beads/redirect")

	// Check if there's anything staged
	if _, err := runGit(trace, wtPath, "diff", "--cached", "--quiet"); err == nil {
		return nil // nothing to commit
	}

	// Commit
	msg := fmt.Sprintf("beadloom: %s — %s", taskID, title)
	if out, err := runGit(trace, wtPath, "commit", "-m", msg); err != nil {
		return fmt.Errorf("git commit: %w\n%s", err, out)
	}
	return nil
}

type cmdRunner struct {
	Cmd        *exec.Cmd
	cancel     context.CancelFunc
	resultSeen atomic.Bool // set when Claude CLI emits "type":"result"
}

// Cleanup releases the task context and kills any lingering child processes.
// Agent subprocesses (e.g. dev servers started for verification) can hold
// stdout/stderr pipes open after the agent exits, which blocks cmd.Wait
// indefinitely without the WaitDelay set in spawnAgent. This kills the
// entire process group to ensure full cleanup.
func (r *cmdRunner) Cleanup() {
	if r.cancel != nil {
		r.cancel()
	}
	if r.Cmd.Process != nil {
		// pgid == pid because we set Setpgid: true in spawnAgent.
		_ = syscall.Kill(-r.Cmd.Process.Pid, syscall.SIGKILL)
	}
}

// resultWatcher monitors Claude CLI stream-json output for the "result" event.
// When the agent emits its result, the CLI *should* exit, but it may hang if
// it's waiting for background tasks (e.g. a dev server started with & that
// kill %1 failed to stop in a non-interactive shell). After a 10-second grace
// period, we kill the entire process group to unblock cmd.Wait().
type resultWatcher struct {
	io.Writer
	cmd      *exec.Cmd
	seen     *atomic.Bool
	killOnce sync.Once
}

func (rw *resultWatcher) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"type":"result"`)) {
		rw.seen.Store(true)
		rw.killOnce.Do(func() {
			go func() {
				time.Sleep(10 * time.Second)
				_ = syscall.Kill(-rw.cmd.Process.Pid, syscall.SIGKILL)
			}()
		})
	}
	return rw.Writer.Write(p)
}

func (o *Orchestrator) spawnAgent(task planner.PlannedTask, wtPath string) (*cmdRunner, error) {
	args := []string{
		"-p", task.Prompt,
		"--output-format", "stream-json",
		"--verbose",
	}
	if !o.Config.Safe {
		args = append(args, "--dangerously-skip-permissions")
	}

	taskCtx, cancel := context.WithTimeout(o.ctx, o.Config.TimeoutPerTask)

	cmd := exec.CommandContext(taskCtx, o.Config.ClaudeBin, args...)
	cmd.Dir = wtPath
	cmd.Env = append(os.Environ(), "BD_DB="+o.Config.DbPath)

	// Run the agent in its own process group so we can kill child processes
	// (e.g. dev servers started for verification) that outlive the agent.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// If the agent exits but child processes hold stdout/stderr pipes open,
	// WaitDelay lets cmd.Wait() return instead of blocking indefinitely.
	cmd.WaitDelay = 5 * time.Second

	logPath := filepath.Join(wtPath, "beadloom.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create log file: %w", err)
	}

	runner := &cmdRunner{Cmd: cmd, cancel: cancel}

	if !o.Config.Quiet {
		sf := ui.NewStreamFormatter(task.TaskID, os.Stderr, &o.mu)
		mw := io.MultiWriter(logFile, sf)
		rw := &resultWatcher{Writer: mw, cmd: cmd, seen: &runner.resultSeen}
		cmd.Stdout = rw
		cmd.Stderr = rw
	} else {
		rw := &resultWatcher{Writer: logFile, cmd: cmd, seen: &runner.resultSeen}
		cmd.Stdout = rw
		cmd.Stderr = rw
	}

	now := time.Now()

	o.mu.Lock()
	o.sessions[task.TaskID] = &AgentSession{
		TaskID:       task.TaskID,
		WorktreePath: wtPath,
		Status:       StatusRunning,
		StartedAt:    now,
		LogFile:      logPath,
	}
	o.mu.Unlock()

	// Update persistent state
	st := &state.SessionState{
		Status:    state.StatusRunning,
		Worktree:  wtPath,
		Branch:    task.BranchName,
		StartedAt: &now,
		LogFile:   logPath,
	}

	if err := cmd.Start(); err != nil {
		cancel()
		logFile.Close()
		return nil, err
	}

	st.PID = cmd.Process.Pid
	o.State.UpdateSession(task.TaskID, st)

	o.mu.Lock()
	o.sessions[task.TaskID].PID = cmd.Process.Pid
	o.mu.Unlock()

	return runner, nil
}

func (o *Orchestrator) updateSession(taskID string, status SessionStatus, exitCode int, wtPath string) {
	now := time.Now()
	o.mu.Lock()
	o.sessions[taskID] = &AgentSession{
		TaskID:       taskID,
		WorktreePath: wtPath,
		Status:       status,
		FinishedAt:   now,
		ExitCode:     exitCode,
	}
	o.mu.Unlock()

	st := &state.SessionState{
		Status:     state.SessionStatus(status),
		Worktree:   wtPath,
		FinishedAt: &now,
		ExitCode:   exitCode,
	}
	o.State.UpdateSession(taskID, st)
}

// Cancel aborts all running sessions.
func (o *Orchestrator) Cancel() {
	if o.cancelFunc != nil {
		o.cancelFunc()
	}
	o.State.SetStatus("cancelled")
}

// GetSessions returns a copy of all sessions.
func (o *Orchestrator) GetSessions() map[string]*AgentSession {
	o.mu.Lock()
	defer o.mu.Unlock()

	result := make(map[string]*AgentSession, len(o.sessions))
	for k, v := range o.sessions {
		copy := *v
		result[k] = &copy
	}
	return result
}
