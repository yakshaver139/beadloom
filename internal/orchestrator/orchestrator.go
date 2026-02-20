package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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

// Run executes the plan using a dynamic dependency-tracking scheduler.
// Each task is dispatched the moment all its predecessors complete,
// eliminating wave-barrier delays.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.ctx, o.cancelFunc = context.WithCancel(ctx)
	defer o.cancelFunc()

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

	// Dispatch all root tasks (pending == 0)
	for id, count := range pending {
		if count == 0 {
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

		// Cleanup worktree on success (serialized via wtMu)
		if err == nil {
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

// markSkipped records a task as skipped in both session state and persistent state.
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

	// Wait for completion
	err = runner.Cmd.Wait()

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

type cmdRunner struct {
	Cmd *exec.Cmd
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
	_ = cancel // context cancelled when parent cancels or timeout fires

	cmd := exec.CommandContext(taskCtx, o.Config.ClaudeBin, args...)
	cmd.Dir = wtPath
	cmd.Env = append(os.Environ(), "BD_DB="+o.Config.DbPath)

	logPath := filepath.Join(wtPath, "beadloom.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("create log file: %w", err)
	}

	if !o.Config.Quiet {
		sf := ui.NewStreamFormatter(task.TaskID, os.Stderr, &o.mu)
		mw := io.MultiWriter(logFile, sf)
		cmd.Stdout = mw
		cmd.Stderr = mw
	} else {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
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
		logFile.Close()
		return nil, err
	}

	st.PID = cmd.Process.Pid
	o.State.UpdateSession(task.TaskID, st)

	o.mu.Lock()
	o.sessions[task.TaskID].PID = cmd.Process.Pid
	o.mu.Unlock()

	return &cmdRunner{Cmd: cmd}, nil
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
