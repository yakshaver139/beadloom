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

// Run executes the full plan wave by wave.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.ctx, o.cancelFunc = context.WithCancel(ctx)
	defer o.cancelFunc()

	// Initialize state
	st, err := state.New(o.Plan.ID, o.Plan.TotalWaves)
	if err != nil {
		return fmt.Errorf("init state: %w", err)
	}
	o.State = st

	for i, wave := range o.Plan.Waves {
		if err := o.ctx.Err(); err != nil {
			return fmt.Errorf("cancelled before wave %d: %w", i, err)
		}

		fmt.Fprintf(os.Stderr, "\n🌊 %s %d/%d (%d tasks)\n", ui.BoldCyan("Wave"), i+1, o.Plan.TotalWaves, len(wave.Tasks))
		if err := o.State.SetWave(i); err != nil {
			return fmt.Errorf("update wave state: %w", err)
		}

		if err := o.executeWave(wave); err != nil {
			if o.hasCriticalFailure(wave) {
				o.State.SetStatus("failed")
				return fmt.Errorf("critical path task failed in wave %d: %w", i, err)
			}
			fmt.Fprintf(os.Stderr, "  %s non-critical task(s) failed in wave %d: %v\n", ui.Yellow("⚠️  Warning:"), i, err)
		}
	}

	o.State.SetStatus("completed")
	return nil
}

// executeWave runs all tasks in a wave concurrently (up to maxParallel).
// Worktrees are created and cleaned up sequentially to avoid git lock contention,
// while agents run in parallel.
func (o *Orchestrator) executeWave(wave planner.ExecutionWave) error {
	// Phase 1: create all worktrees sequentially (git locks prevent parallel creation)
	worktrees := make(map[string]string, len(wave.Tasks))
	for _, task := range wave.Tasks {
		fmt.Fprintf(os.Stderr, "  ▶ %s %s\n", ui.TaskPrefix(task.TaskID), task.Title)
		wtPath, err := o.Worktrees.Create(task.WorktreeName, task.BranchName)
		if err != nil {
			o.updateSession(task.TaskID, StatusFailed, 0, "")
			fmt.Fprintf(os.Stderr, "  %s %v\n", ui.Red("❌ Task error:"), fmt.Errorf("task %s: create worktree: %w", task.TaskID, err))
			continue
		}
		worktrees[task.TaskID] = wtPath
	}

	// Phase 2: spawn agents in parallel
	sem := make(chan struct{}, o.Config.MaxParallel)
	var wg sync.WaitGroup
	errs := make(chan error, len(wave.Tasks))

	for _, task := range wave.Tasks {
		wtPath, ok := worktrees[task.TaskID]
		if !ok {
			errs <- fmt.Errorf("task %s: worktree creation failed", task.TaskID)
			continue
		}
		task := task // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			if err := o.executeTask(task, wtPath); err != nil {
				errs <- fmt.Errorf("task %s: %w", task.TaskID, err)
			}
		}()
	}

	wg.Wait()
	close(errs)

	var firstErr error
	for err := range errs {
		if firstErr == nil {
			firstErr = err
		}
		fmt.Fprintf(os.Stderr, "  %s %v\n", ui.Red("❌ Task error:"), err)
	}

	// Phase 3: cleanup completed worktrees sequentially (git locks prevent parallel removal)
	for _, task := range wave.Tasks {
		o.mu.Lock()
		s := o.sessions[task.TaskID]
		completed := s != nil && s.Status == StatusCompleted
		o.mu.Unlock()

		if completed {
			if rmErr := o.Worktrees.Remove(task.WorktreeName); rmErr != nil {
				fmt.Fprintf(os.Stderr, "  %s failed to remove worktree %s: %v\n", ui.Yellow("⚠️  Warning:"), task.WorktreeName, rmErr)
			}
		}
	}

	return firstErr
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

	// Worktree cleanup is handled sequentially in executeWave Phase 3
	// to avoid git lock contention from concurrent bd worktree remove calls.

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

func (o *Orchestrator) hasCriticalFailure(wave planner.ExecutionWave) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	for _, task := range wave.Tasks {
		if task.IsCritical {
			if s, ok := o.sessions[task.TaskID]; ok && s.Status == StatusFailed {
				return true
			}
		}
	}
	return false
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
