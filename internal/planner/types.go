package planner

import "time"

// TaskDeps holds per-task predecessor and successor lists for dependency tracking.
type TaskDeps struct {
	Predecessors map[string][]string `json:"predecessors"`
	Successors   map[string][]string `json:"successors"`
}

// ExecutionPlan is the complete plan for executing tasks.
type ExecutionPlan struct {
	ID           string                 `json:"id"`
	CreatedAt    time.Time              `json:"created_at"`
	TotalTasks   int                    `json:"total_tasks"`
	TotalWaves   int                    `json:"total_waves"`
	CriticalPath []string               `json:"critical_path"`
	Waves        []ExecutionWave        `json:"waves"`
	Tasks        map[string]*PlannedTask `json:"tasks"`
	Deps         TaskDeps               `json:"deps"`
	Config       PlanConfig             `json:"config"`
}

// ExecutionWave is a group of tasks that execute in parallel.
type ExecutionWave struct {
	Index     int           `json:"index"`
	Tasks     []PlannedTask `json:"tasks"`
	DependsOn []int         `json:"depends_on"`
}

// PlannedTask is a single task ready for execution.
type PlannedTask struct {
	TaskID       string `json:"task_id"`
	Title        string `json:"title"`
	IsCritical   bool   `json:"is_critical"`
	WorktreeName string `json:"worktree_name"`
	BranchName   string `json:"branch_name"`
	Prompt       string `json:"prompt"`
	WorktreePath string `json:"worktree_path"`
	WaveIndex    int    `json:"wave_index"`
}

// PlanConfig holds configuration for plan execution.
type PlanConfig struct {
	MaxParallel        int    `json:"max_parallel"`
	Safe               bool   `json:"safe"`
	TimeoutPerTask     string `json:"timeout_per_task"`
	WorktreeDir        string `json:"worktree_dir"`
	PromptTemplatePath string `json:"prompt_template_path"`
	DbPath             string `json:"db_path"`
}
