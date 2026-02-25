package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshharrison/beadloom/internal/planner"
	"github.com/joshharrison/beadloom/internal/state"
	"github.com/joshharrison/beadloom/internal/worktree"
)

func TestNew_Defaults(t *testing.T) {
	o := New(nil, nil, Config{})
	if o.Config.ClaudeBin != "claude" {
		t.Errorf("expected default claude binary, got %q", o.Config.ClaudeBin)
	}
	if o.Config.MaxParallel != 4 {
		t.Errorf("expected default max parallel 4, got %d", o.Config.MaxParallel)
	}
	if o.Config.TimeoutPerTask != 30*time.Minute {
		t.Errorf("expected default timeout 30m, got %v", o.Config.TimeoutPerTask)
	}
}

func TestNew_Custom(t *testing.T) {
	o := New(nil, nil, Config{
		ClaudeBin:      "/usr/bin/claude",
		MaxParallel:    8,
		TimeoutPerTask: 1 * time.Hour,
		Safe:           true,
	})
	if o.Config.ClaudeBin != "/usr/bin/claude" {
		t.Errorf("expected custom claude binary, got %q", o.Config.ClaudeBin)
	}
	if o.Config.MaxParallel != 8 {
		t.Errorf("expected 8, got %d", o.Config.MaxParallel)
	}
	if !o.Config.Safe {
		t.Error("expected safe=true")
	}
}

func TestGetSessions_ReturnsCopy(t *testing.T) {
	o := &Orchestrator{
		sessions: map[string]*AgentSession{
			"a": {TaskID: "a", Status: StatusRunning},
		},
	}

	sessions := o.GetSessions()
	sessions["a"].Status = StatusFailed

	// Original should be unchanged
	if o.sessions["a"].Status != StatusRunning {
		t.Error("GetSessions should return a copy, not a reference")
	}
}

// makePlanWithDeps creates an Orchestrator with a plan containing the given
// dependency graph. tasks maps taskID -> isCritical.
// preds maps taskID -> list of predecessor task IDs.
func makePlanWithDeps(tasks map[string]bool, preds map[string][]string) *Orchestrator {
	plan := &planner.ExecutionPlan{
		Tasks: make(map[string]*planner.PlannedTask, len(tasks)),
		Deps: planner.TaskDeps{
			Predecessors: make(map[string][]string, len(tasks)),
			Successors:   make(map[string][]string, len(tasks)),
		},
	}

	for id, critical := range tasks {
		plan.Tasks[id] = &planner.PlannedTask{TaskID: id, IsCritical: critical}
		plan.Deps.Predecessors[id] = nil
		plan.Deps.Successors[id] = nil
	}

	for id, predList := range preds {
		plan.Deps.Predecessors[id] = predList
		for _, pred := range predList {
			plan.Deps.Successors[pred] = append(plan.Deps.Successors[pred], id)
		}
	}

	st := &state.RunState{
		Sessions: make(map[string]*state.SessionState),
	}

	return &Orchestrator{
		Plan:     plan,
		State:    st,
		sessions: make(map[string]*AgentSession),
	}
}

// TestCascadeSkip_Linear: A -> B -> C, fail A, verify B and C are skipped.
func TestCascadeSkip_Linear(t *testing.T) {
	o := makePlanWithDeps(
		map[string]bool{"a": false, "b": false, "c": false},
		map[string][]string{"b": {"a"}, "c": {"b"}},
	)

	finished := map[string]bool{"a": true}
	pending := map[string]int{"a": 0, "b": 1, "c": 1}

	skipped := o.cascadeSkip("a", finished, pending)

	if skipped != 2 {
		t.Errorf("expected 2 skipped, got %d", skipped)
	}
	if !finished["b"] {
		t.Error("expected b to be finished (skipped)")
	}
	if !finished["c"] {
		t.Error("expected c to be finished (skipped)")
	}

	// Verify session states
	if o.sessions["b"].Status != StatusSkipped {
		t.Errorf("expected b status skipped, got %s", o.sessions["b"].Status)
	}
	if o.sessions["c"].Status != StatusSkipped {
		t.Errorf("expected c status skipped, got %s", o.sessions["c"].Status)
	}
}

// TestCascadeSkip_Diamond: A -> B, A -> C, B -> D, C -> D.
// Fail B, verify D is skipped but C is not.
func TestCascadeSkip_Diamond(t *testing.T) {
	o := makePlanWithDeps(
		map[string]bool{"a": false, "b": false, "c": false, "d": false},
		map[string][]string{"b": {"a"}, "c": {"a"}, "d": {"b", "c"}},
	)

	// A completed, B failed
	finished := map[string]bool{"a": true, "b": true}
	pending := map[string]int{"a": 0, "b": 1, "c": 1, "d": 2}

	skipped := o.cascadeSkip("b", finished, pending)

	if skipped != 1 {
		t.Errorf("expected 1 skipped (d), got %d", skipped)
	}
	if finished["c"] {
		t.Error("c should not be finished/skipped (it doesn't depend on b)")
	}
	if !finished["d"] {
		t.Error("d should be finished (skipped)")
	}
	if o.sessions["d"].Status != StatusSkipped {
		t.Errorf("expected d status skipped, got %s", o.sessions["d"].Status)
	}
}

// TestCascadeSkip_NoSuccessors: fail a leaf task, verify 0 skipped.
func TestCascadeSkip_NoSuccessors(t *testing.T) {
	o := makePlanWithDeps(
		map[string]bool{"a": false, "b": false},
		map[string][]string{"b": {"a"}},
	)

	// Both finished, fail b (a leaf)
	finished := map[string]bool{"a": true, "b": true}
	pending := map[string]int{"a": 0, "b": 1}

	skipped := o.cascadeSkip("b", finished, pending)

	if skipped != 0 {
		t.Errorf("expected 0 skipped, got %d", skipped)
	}
}

func TestIsTaskCritical(t *testing.T) {
	o := makePlanWithDeps(
		map[string]bool{"a": true, "b": false},
		map[string][]string{},
	)

	if !o.isTaskCritical("a") {
		t.Error("expected a to be critical")
	}
	if o.isTaskCritical("b") {
		t.Error("expected b to not be critical")
	}
	if o.isTaskCritical("nonexistent") {
		t.Error("expected nonexistent to not be critical")
	}
}

// initTestRepo creates a bare-minimum git repo in a temp directory with one
// initial commit. Returns the temp dir path. The caller should defer os.RemoveAll.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// gitInDir runs a git command in the given directory.
func gitInDir(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func TestRestartDetection_PreviousStateFile(t *testing.T) {
	// Save and restore working directory since state.Load uses relative paths
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	// Create a state file with mixed session statuses
	s, err := state.New("test-restart", 1, 3)
	if err != nil {
		t.Fatalf("state.New: %v", err)
	}
	s.UpdateSession("t1", &state.SessionState{Status: state.StatusCompleted})
	s.UpdateSession("t2", &state.SessionState{Status: state.StatusFailed})
	s.UpdateSession("t3", &state.SessionState{Status: state.StatusCompleted})

	// Build an orchestrator with a plan containing t1, t2, t3, and t4 (not in state)
	plan := &planner.ExecutionPlan{
		Tasks: map[string]*planner.PlannedTask{
			"t1": {TaskID: "t1", BranchName: "beadloom/t1"},
			"t2": {TaskID: "t2", BranchName: "beadloom/t2"},
			"t3": {TaskID: "t3", BranchName: "beadloom/t3"},
			"t4": {TaskID: "t4", BranchName: "beadloom/t4"},
		},
	}

	o := &Orchestrator{
		Plan:   plan,
		closed: make(map[string]bool),
	}

	// Simulate the previous-state check from Run()
	if prev := state.LoadCompletedTaskIDs(); len(prev) > 0 {
		for id := range prev {
			if _, inPlan := o.Plan.Tasks[id]; inPlan && !o.closed[id] {
				o.closed[id] = true
			}
		}
	}

	if !o.closed["t1"] {
		t.Error("t1 should be closed (completed in previous state)")
	}
	if o.closed["t2"] {
		t.Error("t2 should not be closed (failed in previous state)")
	}
	if !o.closed["t3"] {
		t.Error("t3 should be closed (completed in previous state)")
	}
	if o.closed["t4"] {
		t.Error("t4 should not be closed (not in previous state)")
	}
}

func TestRestartDetection_PreviousStateSkipsAlreadyClosed(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	s, err := state.New("test-skip-closed", 1, 1)
	if err != nil {
		t.Fatalf("state.New: %v", err)
	}
	s.UpdateSession("t1", &state.SessionState{Status: state.StatusCompleted})

	plan := &planner.ExecutionPlan{
		Tasks: map[string]*planner.PlannedTask{
			"t1": {TaskID: "t1", BranchName: "beadloom/t1"},
		},
	}

	o := &Orchestrator{
		Plan:   plan,
		closed: map[string]bool{"t1": true}, // already closed via beads
	}

	added := 0
	if prev := state.LoadCompletedTaskIDs(); len(prev) > 0 {
		for id := range prev {
			if _, inPlan := o.Plan.Tasks[id]; inPlan && !o.closed[id] {
				o.closed[id] = true
				added++
			}
		}
	}

	if added != 0 {
		t.Errorf("expected 0 newly added (already closed via beads), got %d", added)
	}
}

func TestRestartDetection_MergedBranchCleanup(t *testing.T) {
	repoDir := initTestRepo(t)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	// Create a branch with a commit and merge it into main
	gitInDir(t, repoDir, "checkout", "-b", "beadloom/t1")
	gitInDir(t, repoDir, "commit", "--allow-empty", "-m", "task t1 work")
	gitInDir(t, repoDir, "checkout", "main")
	gitInDir(t, repoDir, "merge", "beadloom/t1", "-m", "merge t1")

	// Create a worktree dir so cleanup has something to target
	wtBase := filepath.Join(repoDir, ".worktrees")
	os.MkdirAll(filepath.Join(wtBase, "t1"), 0755)

	wm := worktree.NewManager(wtBase, nil)

	plan := &planner.ExecutionPlan{
		Tasks: map[string]*planner.PlannedTask{
			"t1": {TaskID: "t1", BranchName: "beadloom/t1", WorktreeName: "t1"},
		},
	}

	o := &Orchestrator{
		Plan:      plan,
		Worktrees: wm,
		closed:    make(map[string]bool),
	}

	// Run the git branch detection logic from Run()
	for id, task := range o.Plan.Tasks {
		if o.closed[id] {
			continue
		}
		if _, err := runGit(false, ".", "rev-parse", "--verify", task.BranchName); err != nil {
			continue
		}
		if err := exec.Command("git", "merge-base", "--is-ancestor", task.BranchName, "HEAD").Run(); err == nil {
			wtPath := o.Worktrees.Path(task.WorktreeName)
			runGit(false, ".", "worktree", "remove", "--force", wtPath)
			runGit(false, ".", "branch", "-D", task.BranchName)
			o.closed[id] = true
		}
	}

	if !o.closed["t1"] {
		t.Error("t1 should be closed (branch was merged into HEAD)")
	}

	// Verify the branch was deleted
	cmd := exec.Command("git", "rev-parse", "--verify", "beadloom/t1")
	cmd.Dir = repoDir
	if err := cmd.Run(); err == nil {
		t.Error("expected beadloom/t1 branch to be deleted")
	}
}

func TestRestartDetection_UnmergedBranchMarkedForMerge(t *testing.T) {
	repoDir := initTestRepo(t)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	// Create a branch with a commit but do NOT merge it
	gitInDir(t, repoDir, "checkout", "-b", "beadloom/t1")
	gitInDir(t, repoDir, "commit", "--allow-empty", "-m", "task t1 work")
	gitInDir(t, repoDir, "checkout", "main")

	wm := worktree.NewManager(filepath.Join(repoDir, ".worktrees"), nil)

	plan := &planner.ExecutionPlan{
		Tasks: map[string]*planner.PlannedTask{
			"t1": {TaskID: "t1", BranchName: "beadloom/t1", WorktreeName: "t1"},
		},
	}

	o := &Orchestrator{
		Plan:      plan,
		Worktrees: wm,
		closed:    make(map[string]bool),
	}

	// Run the git branch detection logic from Run()
	for id, task := range o.Plan.Tasks {
		if o.closed[id] {
			continue
		}
		if _, err := runGit(false, ".", "rev-parse", "--verify", task.BranchName); err != nil {
			continue
		}
		if err := exec.Command("git", "merge-base", "--is-ancestor", task.BranchName, "HEAD").Run(); err == nil {
			o.closed[id] = true
		} else {
			// Branch exists but not merged — mark for merge
			o.closed[id] = true
		}
	}

	if !o.closed["t1"] {
		t.Error("t1 should be closed (unmerged branch detected)")
	}

	// Branch should still exist (not deleted — needs merging)
	cmd := exec.Command("git", "rev-parse", "--verify", "beadloom/t1")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Error("expected beadloom/t1 branch to still exist for merging")
	}
}

func TestRestartDetection_NoBranch(t *testing.T) {
	repoDir := initTestRepo(t)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	wm := worktree.NewManager(filepath.Join(repoDir, ".worktrees"), nil)

	plan := &planner.ExecutionPlan{
		Tasks: map[string]*planner.PlannedTask{
			"t1": {TaskID: "t1", BranchName: "beadloom/t1", WorktreeName: "t1"},
		},
	}

	o := &Orchestrator{
		Plan:      plan,
		Worktrees: wm,
		closed:    make(map[string]bool),
	}

	// Run the git branch detection logic — branch doesn't exist
	for id, task := range o.Plan.Tasks {
		if o.closed[id] {
			continue
		}
		if _, err := runGit(false, ".", "rev-parse", "--verify", task.BranchName); err != nil {
			continue
		}
		if err := exec.Command("git", "merge-base", "--is-ancestor", task.BranchName, "HEAD").Run(); err == nil {
			o.closed[id] = true
		} else {
			o.closed[id] = true
		}
	}

	if o.closed["t1"] {
		t.Error("t1 should not be closed (branch doesn't exist)")
	}
}

func TestRestartDetection_WaveMergeIncludesPreviousUnmerged(t *testing.T) {
	repoDir := initTestRepo(t)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	// Create an unmerged branch
	gitInDir(t, repoDir, "checkout", "-b", "beadloom/t1")
	gitInDir(t, repoDir, "commit", "--allow-empty", "-m", "task t1 work")
	gitInDir(t, repoDir, "checkout", "main")

	wave := planner.ExecutionWave{
		Index: 0,
		Tasks: []planner.PlannedTask{
			{TaskID: "t1", BranchName: "beadloom/t1"},
			{TaskID: "t2", BranchName: "beadloom/t2"},
		},
	}

	o := &Orchestrator{
		closed: map[string]bool{"t1": true}, // marked closed from restart detection
	}

	// Simulate the wave merge inclusion logic from runWaveMode()
	completedIDs := map[string]bool{
		"t2": true, // t2 completed in this wave normally
	}

	for _, task := range wave.Tasks {
		if !completedIDs[task.TaskID] && o.closed[task.TaskID] {
			if err := exec.Command("git", "rev-parse", "--verify", task.BranchName).Run(); err == nil {
				completedIDs[task.TaskID] = true
			}
		}
	}

	if !completedIDs["t1"] {
		t.Error("t1 should be included in completedIDs (has unmerged branch from previous run)")
	}
	if !completedIDs["t2"] {
		t.Error("t2 should still be in completedIDs")
	}
}

func TestRestartDetection_WaveMergeSkipsMergedBranch(t *testing.T) {
	repoDir := initTestRepo(t)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	// t1's branch was already deleted during merged-branch cleanup in Run()
	// (no branch exists), so it should NOT be added to completedIDs

	wave := planner.ExecutionWave{
		Index: 0,
		Tasks: []planner.PlannedTask{
			{TaskID: "t1", BranchName: "beadloom/t1"},
		},
	}

	o := &Orchestrator{
		closed: map[string]bool{"t1": true}, // marked closed (branch was merged and deleted)
	}

	completedIDs := make(map[string]bool)

	for _, task := range wave.Tasks {
		if !completedIDs[task.TaskID] && o.closed[task.TaskID] {
			if err := exec.Command("git", "rev-parse", "--verify", task.BranchName).Run(); err == nil {
				completedIDs[task.TaskID] = true
			}
		}
	}

	if completedIDs["t1"] {
		t.Error("t1 should not be in completedIDs (branch was already deleted)")
	}
}
