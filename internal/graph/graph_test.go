package graph

import (
	"testing"

	"github.com/joshharrison/beadloom/internal/bd"
)

func TestBuildFromRaw_SimpleDAG(t *testing.T) {
	// A -> B -> D
	// A -> C -> D
	raw := []bd.RawTask{
		{ID: "a", Title: "Task A", Status: "open", Blocks: []string{"b", "c"}},
		{ID: "b", Title: "Task B", Status: "open", Blocks: []string{"d"}, BlockedBy: []string{"a"}},
		{ID: "c", Title: "Task C", Status: "open", Blocks: []string{"d"}, BlockedBy: []string{"a"}},
		{ID: "d", Title: "Task D", Status: "open", BlockedBy: []string{"b", "c"}},
	}

	g, err := BuildFromRaw(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if g.TaskCount() != 4 {
		t.Errorf("expected 4 tasks, got %d", g.TaskCount())
	}

	// Check roots
	if len(g.Roots) != 1 || g.Roots[0] != "a" {
		t.Errorf("expected roots=[a], got %v", g.Roots)
	}

	// Check leaves
	if len(g.Leaves) != 1 || g.Leaves[0] != "d" {
		t.Errorf("expected leaves=[d], got %v", g.Leaves)
	}

	// Check adjacency
	if adj := g.Adj["a"]; len(adj) != 2 {
		t.Errorf("expected a to block 2 tasks, got %v", adj)
	}

	// Check reverse adjacency
	if rev := g.RevAdj["d"]; len(rev) != 2 {
		t.Errorf("expected d to be blocked by 2 tasks, got %v", rev)
	}
}

func TestBuildFromRaw_SingleTask(t *testing.T) {
	raw := []bd.RawTask{
		{ID: "x", Title: "Solo task", Status: "open"},
	}

	g, err := BuildFromRaw(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if g.TaskCount() != 1 {
		t.Errorf("expected 1 task, got %d", g.TaskCount())
	}
	if len(g.Roots) != 1 || g.Roots[0] != "x" {
		t.Errorf("expected roots=[x], got %v", g.Roots)
	}
	if len(g.Leaves) != 1 || g.Leaves[0] != "x" {
		t.Errorf("expected leaves=[x], got %v", g.Leaves)
	}
}

func TestBuildFromRaw_CycleDetection(t *testing.T) {
	// A -> B -> C -> A (cycle)
	raw := []bd.RawTask{
		{ID: "a", Title: "Task A", Status: "open", Blocks: []string{"b"}},
		{ID: "b", Title: "Task B", Status: "open", Blocks: []string{"c"}},
		{ID: "c", Title: "Task C", Status: "open", Blocks: []string{"a"}},
	}

	_, err := BuildFromRaw(raw)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	t.Logf("cycle error (expected): %v", err)
}

func TestBuildFromRaw_ExternalDepsIgnored(t *testing.T) {
	// Task blocks a non-existent task — should be ignored
	raw := []bd.RawTask{
		{ID: "a", Title: "Task A", Status: "open", Blocks: []string{"z"}},
		{ID: "b", Title: "Task B", Status: "open"},
	}

	g, err := BuildFromRaw(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "z" doesn't exist in graph, so "a" should have no adjacency
	if len(g.Adj["a"]) != 0 {
		t.Errorf("expected no adj for a (z not in graph), got %v", g.Adj["a"])
	}
}

func TestDetectCycle_NoCycle(t *testing.T) {
	g := &TaskGraph{
		Tasks: map[string]*Task{
			"a": {ID: "a"},
			"b": {ID: "b"},
		},
		Adj: map[string][]string{
			"a": {"b"},
		},
		RevAdj: map[string][]string{
			"b": {"a"},
		},
	}

	cycle := g.DetectCycle()
	if cycle != nil {
		t.Errorf("expected no cycle, got %v", cycle)
	}
}

func TestDetectCycle_WithCycle(t *testing.T) {
	g := &TaskGraph{
		Tasks: map[string]*Task{
			"a": {ID: "a"},
			"b": {ID: "b"},
			"c": {ID: "c"},
		},
		Adj: map[string][]string{
			"a": {"b"},
			"b": {"c"},
			"c": {"a"},
		},
		RevAdj: map[string][]string{
			"a": {"c"},
			"b": {"a"},
			"c": {"b"},
		},
	}

	cycle := g.DetectCycle()
	if cycle == nil {
		t.Fatal("expected cycle, got nil")
	}
	if len(cycle) < 3 {
		t.Errorf("expected cycle of length >= 3, got %v", cycle)
	}
	t.Logf("detected cycle: %v", cycle)
}

func TestFilter(t *testing.T) {
	raw := []bd.RawTask{
		{ID: "a", Title: "Task A", Priority: 0, Status: "open"},
		{ID: "b", Title: "Task B", Priority: 1, Status: "open"},
		{ID: "c", Title: "Task C", Priority: 2, Status: "open"},
	}

	g, err := BuildFromRaw(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	filtered, err := g.Filter(func(t *Task) bool {
		return t.Priority <= 1
	})
	if err != nil {
		t.Fatalf("unexpected filter error: %v", err)
	}

	if filtered.TaskCount() != 2 {
		t.Errorf("expected 2 tasks after filter, got %d", filtered.TaskCount())
	}
	if _, ok := filtered.Tasks["c"]; ok {
		t.Error("task c (priority 2) should have been filtered out")
	}
}

func TestBuildFromRaw_Empty(t *testing.T) {
	g, err := BuildFromRaw(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.TaskCount() != 0 {
		t.Errorf("expected 0 tasks, got %d", g.TaskCount())
	}
}

func TestBuildFromRaw_LinearChain(t *testing.T) {
	// A -> B -> C -> D -> E
	raw := []bd.RawTask{
		{ID: "a", Title: "A", Status: "open", Blocks: []string{"b"}},
		{ID: "b", Title: "B", Status: "open", Blocks: []string{"c"}, BlockedBy: []string{"a"}},
		{ID: "c", Title: "C", Status: "open", Blocks: []string{"d"}, BlockedBy: []string{"b"}},
		{ID: "d", Title: "D", Status: "open", Blocks: []string{"e"}, BlockedBy: []string{"c"}},
		{ID: "e", Title: "E", Status: "open", BlockedBy: []string{"d"}},
	}

	g, err := BuildFromRaw(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(g.Roots) != 1 || g.Roots[0] != "a" {
		t.Errorf("expected roots=[a], got %v", g.Roots)
	}
	if len(g.Leaves) != 1 || g.Leaves[0] != "e" {
		t.Errorf("expected leaves=[e], got %v", g.Leaves)
	}
}
