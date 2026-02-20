package orchestrator

import (
	"testing"
	"time"

	"github.com/joshharrison/beadloom/internal/planner"
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

func TestHasCriticalFailure_NoFailure(t *testing.T) {
	o := &Orchestrator{
		sessions: map[string]*AgentSession{
			"a": {TaskID: "a", Status: StatusCompleted},
		},
	}

	wave := planner.ExecutionWave{
		Index: 0,
		Tasks: []planner.PlannedTask{
			{TaskID: "a", IsCritical: true},
		},
	}
	if o.hasCriticalFailure(wave) {
		t.Error("expected no critical failure")
	}
}

func TestHasCriticalFailure_CriticalFailed(t *testing.T) {
	o := &Orchestrator{
		sessions: map[string]*AgentSession{
			"a": {TaskID: "a", Status: StatusFailed},
		},
	}

	wave := planner.ExecutionWave{
		Index: 0,
		Tasks: []planner.PlannedTask{
			{TaskID: "a", IsCritical: true},
		},
	}
	if !o.hasCriticalFailure(wave) {
		t.Error("expected critical failure")
	}
}

func TestHasCriticalFailure_NonCriticalFailed(t *testing.T) {
	o := &Orchestrator{
		sessions: map[string]*AgentSession{
			"a": {TaskID: "a", Status: StatusFailed},
		},
	}

	wave := planner.ExecutionWave{
		Index: 0,
		Tasks: []planner.PlannedTask{
			{TaskID: "a", IsCritical: false},
		},
	}
	if o.hasCriticalFailure(wave) {
		t.Error("expected no critical failure for non-critical task")
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
