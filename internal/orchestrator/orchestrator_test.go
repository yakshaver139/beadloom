package orchestrator

import (
	"testing"
	"time"

	"github.com/joshharrison/beadloom/internal/planner"
	"github.com/joshharrison/beadloom/internal/state"
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
