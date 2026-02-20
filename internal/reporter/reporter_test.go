package reporter

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/joshharrison/beadloom/internal/planner"
	"github.com/joshharrison/beadloom/internal/state"
)

func makePlan() *planner.ExecutionPlan {
	return &planner.ExecutionPlan{
		ID:         "test-plan",
		TotalTasks: 3,
		TotalWaves: 2,
		CriticalPath: []string{"a", "c"},
		Waves: []planner.ExecutionWave{
			{
				Index: 0,
				Tasks: []planner.PlannedTask{
					{TaskID: "a", Title: "Task A", IsCritical: true},
					{TaskID: "b", Title: "Task B", IsCritical: false},
				},
			},
			{
				Index:     1,
				Tasks:     []planner.PlannedTask{{TaskID: "c", Title: "Task C", IsCritical: true}},
				DependsOn: []int{0},
			},
		},
	}
}

func makeState() *state.RunState {
	now := time.Now()
	finished := now.Add(2 * time.Minute)

	return &state.RunState{
		PlanID:      "test-plan",
		StartedAt:   now,
		Status:      "running",
		CurrentWave: 1,
		TotalWaves:  2,
		Sessions: map[string]*state.SessionState{
			"a": {Status: state.StatusCompleted, StartedAt: &now, FinishedAt: &finished},
			"b": {Status: state.StatusCompleted, StartedAt: &now, FinishedAt: &finished},
			"c": {Status: state.StatusRunning, StartedAt: &now},
		},
	}
}

func TestPrintStatus(t *testing.T) {
	plan := makePlan()
	st := makeState()
	rpt := New(plan, st)

	var buf bytes.Buffer
	rpt.PrintStatus(&buf)

	output := buf.String()

	if !strings.Contains(output, "Beadloom") {
		t.Error("expected output to contain 'Beadloom'")
	}
	if !strings.Contains(output, "WAVE 1") {
		t.Error("expected output to contain 'WAVE 1'")
	}
	if !strings.Contains(output, "WAVE 2") {
		t.Error("expected output to contain 'WAVE 2'")
	}
	if !strings.Contains(output, "Task A") {
		t.Error("expected output to contain 'Task A'")
	}
	if !strings.Contains(output, "⚡") {
		t.Error("expected output to contain critical path marker")
	}
}

func TestJSON(t *testing.T) {
	plan := makePlan()
	st := makeState()
	rpt := New(plan, st)

	data, err := rpt.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	output := string(data)
	if !strings.Contains(output, "test-plan") {
		t.Error("JSON should contain plan ID")
	}
	if !strings.Contains(output, "running") {
		t.Error("JSON should contain status")
	}
}

func TestSummary(t *testing.T) {
	plan := makePlan()
	st := makeState()
	st.Status = "completed"
	rpt := New(plan, st)

	summary := rpt.Summary()
	if !strings.Contains(summary, "Beadloom Run Complete") {
		t.Error("summary should contain header")
	}
	if !strings.Contains(summary, "test-plan") {
		t.Error("summary should contain plan ID")
	}
}

func TestSummary_WithFailures(t *testing.T) {
	plan := makePlan()
	st := makeState()
	st.Status = "failed"
	st.Sessions["c"] = &state.SessionState{
		Status:  state.StatusFailed,
		LogFile: ".worktrees/c/beadloom.log",
	}
	rpt := New(plan, st)

	summary := rpt.Summary()
	if !strings.Contains(summary, "Failed tasks") {
		t.Error("summary should list failed tasks")
	}
	if !strings.Contains(summary, "beadloom.log") {
		t.Error("summary should show log file path")
	}
}
