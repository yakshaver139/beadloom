package planner

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/joshharrison/beadloom/internal/cpm"
	"github.com/joshharrison/beadloom/internal/graph"
)

// Generate creates an ExecutionPlan from CPM analysis results.
func Generate(g *graph.TaskGraph, cpmResult *cpm.CPMResult, config PlanConfig) (*ExecutionPlan, error) {
	if config.WorktreeDir == "" {
		config.WorktreeDir = ".worktrees"
	}
	if config.MaxParallel == 0 {
		config.MaxParallel = 4
	}
	if config.TimeoutPerTask == "" {
		config.TimeoutPerTask = "30m"
	}

	plan := &ExecutionPlan{
		ID:           fmt.Sprintf("loom-%s", time.Now().Format("2006-01-02-150405")),
		CreatedAt:    time.Now(),
		TotalTasks:   g.TaskCount(),
		TotalWaves:   len(cpmResult.Waves),
		CriticalPath: cpmResult.CriticalPath,
		Config:       config,
	}

	for _, wave := range cpmResult.Waves {
		ew := ExecutionWave{
			Index: wave.Index,
		}

		// Each wave depends on all previous waves
		if wave.Index > 0 {
			ew.DependsOn = []int{wave.Index - 1}
		}

		for _, taskID := range wave.TaskIDs {
			task := g.Tasks[taskID]
			schedule := cpmResult.Tasks[taskID]

			worktreeName := taskID
			branchName := fmt.Sprintf("beadloom/%s", taskID)
			worktreePath := filepath.Join(config.WorktreeDir, worktreeName)

			promptData := PromptData{
				TaskID:       taskID,
				Title:        task.Title,
				Description:  task.Description,
				Acceptance:   task.Acceptance,
				Design:       task.Design,
				Notes:        task.Notes,
				WorktreePath: worktreePath,
				BranchName:   branchName,
				WaveIndex:    wave.Index,
				WaveSize:     len(wave.TaskIDs),
				IsCritical:   schedule.IsCritical,
			}

			prompt, err := RenderPrompt(promptData, config.PromptTemplatePath)
			if err != nil {
				return nil, fmt.Errorf("render prompt for task %s: %w", taskID, err)
			}

			pt := PlannedTask{
				TaskID:       taskID,
				Title:        task.Title,
				IsCritical:   schedule.IsCritical,
				WorktreeName: worktreeName,
				BranchName:   branchName,
				Prompt:       prompt,
				WorktreePath: worktreePath,
				WaveIndex:    wave.Index,
			}
			ew.Tasks = append(ew.Tasks, pt)
		}

		plan.Waves = append(plan.Waves, ew)
	}

	return plan, nil
}
