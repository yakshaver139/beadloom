package cpm

import (
	"testing"

	"github.com/joshharrison/beadloom/internal/bd"
	"github.com/joshharrison/beadloom/internal/graph"
)

func buildTestGraph(t *testing.T, raw []bd.RawTask) *graph.TaskGraph {
	t.Helper()
	g, err := graph.BuildFromRaw(raw)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	return g
}

func TestAnalyze_LinearChain(t *testing.T) {
	// A -> B -> C (each duration 1)
	raw := []bd.RawTask{
		{ID: "a", Title: "A", Status: "open", Blocks: []string{"b"}},
		{ID: "b", Title: "B", Status: "open", Blocks: []string{"c"}, BlockedBy: []string{"a"}},
		{ID: "c", Title: "C", Status: "open", BlockedBy: []string{"b"}},
	}
	g := buildTestGraph(t, raw)

	result, err := Analyze(g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Total duration should be 3 (3 tasks, each duration 1)
	if result.TotalDuration != 3 {
		t.Errorf("expected total duration 3, got %d", result.TotalDuration)
	}

	// All tasks should be on critical path
	if len(result.CriticalPath) != 3 {
		t.Errorf("expected 3 tasks on critical path, got %d: %v", len(result.CriticalPath), result.CriticalPath)
	}

	// Should be 3 waves (no parallelism in a chain)
	if len(result.Waves) != 3 {
		t.Errorf("expected 3 waves, got %d", len(result.Waves))
	}

	// Check ES/EF for each task
	assertSchedule(t, result.Tasks["a"], 0, 1, 0, 1, 0, true)
	assertSchedule(t, result.Tasks["b"], 1, 2, 1, 2, 0, true)
	assertSchedule(t, result.Tasks["c"], 2, 3, 2, 3, 0, true)
}

func TestAnalyze_DiamondDAG(t *testing.T) {
	// A -> B -> D
	// A -> C -> D
	raw := []bd.RawTask{
		{ID: "a", Title: "A", Status: "open", Blocks: []string{"b", "c"}},
		{ID: "b", Title: "B", Status: "open", Blocks: []string{"d"}, BlockedBy: []string{"a"}},
		{ID: "c", Title: "C", Status: "open", Blocks: []string{"d"}, BlockedBy: []string{"a"}},
		{ID: "d", Title: "D", Status: "open", BlockedBy: []string{"b", "c"}},
	}
	g := buildTestGraph(t, raw)

	result, err := Analyze(g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Total duration: A(1) + max(B(1), C(1)) + D(1) = 3
	if result.TotalDuration != 3 {
		t.Errorf("expected total duration 3, got %d", result.TotalDuration)
	}

	// 3 waves: [A], [B,C], [D]
	if len(result.Waves) != 3 {
		t.Errorf("expected 3 waves, got %d", len(result.Waves))
	}

	// Wave 1 should have B and C
	if len(result.Waves) >= 2 {
		wave1 := result.Waves[1]
		if len(wave1.TaskIDs) != 2 {
			t.Errorf("expected 2 tasks in wave 1, got %d: %v", len(wave1.TaskIDs), wave1.TaskIDs)
		}
	}

	// A and D should both be critical; B and C should both be critical (same duration path)
	if !result.Tasks["a"].IsCritical {
		t.Error("expected task A to be critical")
	}
	if !result.Tasks["d"].IsCritical {
		t.Error("expected task D to be critical")
	}
}

func TestAnalyze_WithEstimates(t *testing.T) {
	// A(5) -> B(1) -> D(1)
	// A(5) -> C(10) -> D(1)
	// Critical path should be A -> C -> D (total 16)
	raw := []bd.RawTask{
		{ID: "a", Title: "A", Status: "open", Blocks: []string{"b", "c"}},
		{ID: "b", Title: "B", Status: "open", Blocks: []string{"d"}, BlockedBy: []string{"a"}},
		{ID: "c", Title: "C", Status: "open", Blocks: []string{"d"}, BlockedBy: []string{"a"}},
		{ID: "d", Title: "D", Status: "open", BlockedBy: []string{"b", "c"}},
	}
	g := buildTestGraph(t, raw)

	// Set estimates
	g.Tasks["a"].EstimateMins = 5
	g.Tasks["b"].EstimateMins = 1
	g.Tasks["c"].EstimateMins = 10
	g.Tasks["d"].EstimateMins = 1

	result, err := Analyze(g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Total duration: 5 + 10 + 1 = 16
	if result.TotalDuration != 16 {
		t.Errorf("expected total duration 16, got %d", result.TotalDuration)
	}

	// B should have slack (not critical)
	if result.Tasks["b"].IsCritical {
		t.Error("expected task B to NOT be critical")
	}
	if result.Tasks["b"].Slack != 9 {
		t.Errorf("expected B slack=9, got %d", result.Tasks["b"].Slack)
	}

	// A, C, D should be critical
	if !result.Tasks["a"].IsCritical {
		t.Error("expected task A to be critical")
	}
	if !result.Tasks["c"].IsCritical {
		t.Error("expected task C to be critical")
	}
	if !result.Tasks["d"].IsCritical {
		t.Error("expected task D to be critical")
	}
}

func TestAnalyze_ParallelIndependent(t *testing.T) {
	// Three independent tasks
	raw := []bd.RawTask{
		{ID: "a", Title: "A", Status: "open"},
		{ID: "b", Title: "B", Status: "open"},
		{ID: "c", Title: "C", Status: "open"},
	}
	g := buildTestGraph(t, raw)

	result, err := Analyze(g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All should be in wave 0
	if len(result.Waves) != 1 {
		t.Errorf("expected 1 wave, got %d", len(result.Waves))
	}
	if len(result.Waves[0].TaskIDs) != 3 {
		t.Errorf("expected 3 tasks in wave 0, got %d", len(result.Waves[0].TaskIDs))
	}

	// Total duration = 1 (all parallel)
	if result.TotalDuration != 1 {
		t.Errorf("expected total duration 1, got %d", result.TotalDuration)
	}
}

func TestAnalyze_SingleTask(t *testing.T) {
	raw := []bd.RawTask{
		{ID: "solo", Title: "Solo", Status: "open"},
	}
	g := buildTestGraph(t, raw)

	result, err := Analyze(g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TotalDuration != 1 {
		t.Errorf("expected total duration 1, got %d", result.TotalDuration)
	}
	if len(result.CriticalPath) != 1 || result.CriticalPath[0] != "solo" {
		t.Errorf("expected critical path [solo], got %v", result.CriticalPath)
	}
}

func TestAnalyze_WideDAG(t *testing.T) {
	//     A
	//   / | \
	//  B  C  D
	//   \ | /
	//     E
	raw := []bd.RawTask{
		{ID: "a", Title: "A", Status: "open", Blocks: []string{"b", "c", "d"}},
		{ID: "b", Title: "B", Status: "open", Blocks: []string{"e"}, BlockedBy: []string{"a"}},
		{ID: "c", Title: "C", Status: "open", Blocks: []string{"e"}, BlockedBy: []string{"a"}},
		{ID: "d", Title: "D", Status: "open", Blocks: []string{"e"}, BlockedBy: []string{"a"}},
		{ID: "e", Title: "E", Status: "open", BlockedBy: []string{"b", "c", "d"}},
	}
	g := buildTestGraph(t, raw)

	result, err := Analyze(g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 3 waves: [A], [B,C,D], [E]
	if len(result.Waves) != 3 {
		t.Errorf("expected 3 waves, got %d", len(result.Waves))
	}

	// Middle wave should have 3 tasks
	if len(result.Waves) >= 2 && len(result.Waves[1].TaskIDs) != 3 {
		t.Errorf("expected 3 tasks in wave 1, got %d", len(result.Waves[1].TaskIDs))
	}
}

func assertSchedule(t *testing.T, ts *TaskSchedule, es, ef, ls, lf, slack int, critical bool) {
	t.Helper()
	if ts.ES != es {
		t.Errorf("task %s: expected ES=%d, got %d", ts.TaskID, es, ts.ES)
	}
	if ts.EF != ef {
		t.Errorf("task %s: expected EF=%d, got %d", ts.TaskID, ef, ts.EF)
	}
	if ts.LS != ls {
		t.Errorf("task %s: expected LS=%d, got %d", ts.TaskID, ls, ts.LS)
	}
	if ts.LF != lf {
		t.Errorf("task %s: expected LF=%d, got %d", ts.TaskID, lf, ts.LF)
	}
	if ts.Slack != slack {
		t.Errorf("task %s: expected slack=%d, got %d", ts.TaskID, slack, ts.Slack)
	}
	if ts.IsCritical != critical {
		t.Errorf("task %s: expected critical=%v, got %v", ts.TaskID, critical, ts.IsCritical)
	}
}
