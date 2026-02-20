package planner

import (
	"strings"
	"testing"

	"github.com/joshharrison/beadloom/internal/bd"
	"github.com/joshharrison/beadloom/internal/cpm"
	"github.com/joshharrison/beadloom/internal/graph"
)

func TestGenerate_BasicPlan(t *testing.T) {
	// A -> B -> C
	raw := []bd.RawTask{
		{ID: "a", Title: "Task A", Status: "open", Blocks: []string{"b"}, Description: "Do A"},
		{ID: "b", Title: "Task B", Status: "open", Blocks: []string{"c"}, BlockedBy: []string{"a"}, Description: "Do B"},
		{ID: "c", Title: "Task C", Status: "open", BlockedBy: []string{"b"}, Description: "Do C"},
	}

	g, err := graph.BuildFromRaw(raw)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	result, err := cpm.Analyze(g)
	if err != nil {
		t.Fatalf("cpm analyze: %v", err)
	}

	config := PlanConfig{
		MaxParallel:    2,
		WorktreeDir:    ".worktrees",
		TimeoutPerTask: "30m",
	}

	plan, err := Generate(g, result, config)
	if err != nil {
		t.Fatalf("generate plan: %v", err)
	}

	if plan.TotalTasks != 3 {
		t.Errorf("expected 3 total tasks, got %d", plan.TotalTasks)
	}
	if plan.TotalWaves != 3 {
		t.Errorf("expected 3 waves, got %d", plan.TotalWaves)
	}
	if len(plan.CriticalPath) != 3 {
		t.Errorf("expected 3 tasks on critical path, got %d", len(plan.CriticalPath))
	}

	// Check wave 0 has task A
	if len(plan.Waves[0].Tasks) != 1 || plan.Waves[0].Tasks[0].TaskID != "a" {
		t.Errorf("expected wave 0 to have task a, got %v", plan.Waves[0].Tasks)
	}

	// Check wave dependencies
	if len(plan.Waves[0].DependsOn) != 0 {
		t.Errorf("wave 0 should have no dependencies")
	}
	if len(plan.Waves[1].DependsOn) != 1 || plan.Waves[1].DependsOn[0] != 0 {
		t.Errorf("wave 1 should depend on wave 0")
	}

	// Check flat task lookup
	if len(plan.Tasks) != 3 {
		t.Errorf("expected 3 tasks in map, got %d", len(plan.Tasks))
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, ok := plan.Tasks[id]; !ok {
			t.Errorf("expected task %s in Tasks map", id)
		}
	}

	// Check dependency graph
	if len(plan.Deps.Predecessors["a"]) != 0 {
		t.Errorf("expected a to have no predecessors, got %v", plan.Deps.Predecessors["a"])
	}
	if len(plan.Deps.Predecessors["b"]) != 1 || plan.Deps.Predecessors["b"][0] != "a" {
		t.Errorf("expected b predecessors=[a], got %v", plan.Deps.Predecessors["b"])
	}
	if len(plan.Deps.Successors["a"]) != 1 || plan.Deps.Successors["a"][0] != "b" {
		t.Errorf("expected a successors=[b], got %v", plan.Deps.Successors["a"])
	}
	if len(plan.Deps.Successors["c"]) != 0 {
		t.Errorf("expected c to have no successors, got %v", plan.Deps.Successors["c"])
	}
}

func TestGenerate_WorktreeNaming(t *testing.T) {
	raw := []bd.RawTask{
		{ID: "bd-abc123", Title: "My Task", Status: "open"},
	}

	g, err := graph.BuildFromRaw(raw)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	result, err := cpm.Analyze(g)
	if err != nil {
		t.Fatalf("cpm analyze: %v", err)
	}

	plan, err := Generate(g, result, PlanConfig{WorktreeDir: ".wt"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	task := plan.Waves[0].Tasks[0]
	if task.WorktreeName != "bd-abc123" {
		t.Errorf("expected worktree name bd-abc123, got %s", task.WorktreeName)
	}
	if task.BranchName != "beadloom/bd-abc123" {
		t.Errorf("expected branch beadloom/bd-abc123, got %s", task.BranchName)
	}
	if task.WorktreePath != ".wt/bd-abc123" {
		t.Errorf("expected worktree path .wt/bd-abc123, got %s", task.WorktreePath)
	}
}

func TestRenderPrompt_Default(t *testing.T) {
	data := PromptData{
		TaskID:       "bd-123",
		Title:        "Implement feature X",
		Description:  "Build the X feature",
		Acceptance:   "Tests pass",
		WorktreePath: ".worktrees/bd-123",
		BranchName:   "beadloom/bd-123",
		WaveIndex:    1,
		WaveSize:     3,
		IsCritical:   true,
	}

	prompt, err := RenderPrompt(data, "")
	if err != nil {
		t.Fatalf("render prompt: %v", err)
	}

	if !strings.Contains(prompt, "bd-123") {
		t.Error("prompt should contain task ID")
	}
	if !strings.Contains(prompt, "Implement feature X") {
		t.Error("prompt should contain title")
	}
	if !strings.Contains(prompt, "Build the X feature") {
		t.Error("prompt should contain description")
	}
	if !strings.Contains(prompt, "CRITICAL PATH") {
		t.Error("prompt should mention critical path for critical tasks")
	}
	if !strings.Contains(prompt, "bd close bd-123") {
		t.Error("prompt should contain bd close instruction")
	}
}

func TestRenderPrompt_NonCritical(t *testing.T) {
	data := PromptData{
		TaskID:     "bd-456",
		Title:      "Write docs",
		IsCritical: false,
	}

	prompt, err := RenderPrompt(data, "")
	if err != nil {
		t.Fatalf("render prompt: %v", err)
	}

	if strings.Contains(prompt, "CRITICAL PATH") {
		t.Error("non-critical task should not mention critical path")
	}
}

func TestGenerate_DepsMatchGraph(t *testing.T) {
	// Diamond: a -> b, a -> c, b -> d, c -> d
	raw := []bd.RawTask{
		{ID: "a", Title: "A", Status: "open", Blocks: []string{"b", "c"}},
		{ID: "b", Title: "B", Status: "open", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		{ID: "c", Title: "C", Status: "open", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		{ID: "d", Title: "D", Status: "open", BlockedBy: []string{"b", "c"}},
	}

	g, err := graph.BuildFromRaw(raw)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	result, err := cpm.Analyze(g)
	if err != nil {
		t.Fatalf("cpm analyze: %v", err)
	}

	plan, err := Generate(g, result, PlanConfig{WorktreeDir: ".wt"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Verify flat task lookup has all 4 tasks
	if len(plan.Tasks) != 4 {
		t.Errorf("expected 4 tasks in map, got %d", len(plan.Tasks))
	}

	// Verify predecessors
	if len(plan.Deps.Predecessors["a"]) != 0 {
		t.Errorf("a should have no predecessors")
	}
	if len(plan.Deps.Predecessors["d"]) != 2 {
		t.Errorf("d should have 2 predecessors, got %d", len(plan.Deps.Predecessors["d"]))
	}

	// Verify successors
	if len(plan.Deps.Successors["a"]) != 2 {
		t.Errorf("a should have 2 successors, got %d", len(plan.Deps.Successors["a"]))
	}
	if len(plan.Deps.Successors["d"]) != 0 {
		t.Errorf("d should have no successors")
	}
}

func TestGenerate_ConfigDefaults(t *testing.T) {
	raw := []bd.RawTask{
		{ID: "a", Title: "A", Status: "open"},
	}
	g, _ := graph.BuildFromRaw(raw)
	result, _ := cpm.Analyze(g)

	// Empty config — should get defaults
	plan, err := Generate(g, result, PlanConfig{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if plan.Config.MaxParallel != 4 {
		t.Errorf("expected default max parallel 4, got %d", plan.Config.MaxParallel)
	}
	if plan.Config.TimeoutPerTask != "30m" {
		t.Errorf("expected default timeout 30m, got %s", plan.Config.TimeoutPerTask)
	}
	if plan.Config.WorktreeDir != ".worktrees" {
		t.Errorf("expected default worktree dir .worktrees, got %s", plan.Config.WorktreeDir)
	}
}
