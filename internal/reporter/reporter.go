package reporter

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/joshharrison/beadloom/internal/planner"
	"github.com/joshharrison/beadloom/internal/state"
	"github.com/joshharrison/beadloom/internal/ui"
)

// Reporter provides status display for beadloom execution.
type Reporter struct {
	Plan      *planner.ExecutionPlan
	State     *state.RunState
	StartTime time.Time
}

// New creates a new Reporter.
func New(plan *planner.ExecutionPlan, st *state.RunState) *Reporter {
	return &Reporter{
		Plan:      plan,
		State:     st,
		StartTime: st.StartedAt,
	}
}

// PrintStatus writes a terminal-friendly status table.
func (r *Reporter) PrintStatus(w io.Writer) {
	elapsed := time.Since(r.StartTime).Truncate(time.Second)

	completed := 0
	running := 0
	failed := 0
	for _, ss := range r.State.Sessions {
		switch ss.Status {
		case state.StatusCompleted:
			completed++
		case state.StatusRunning:
			running++
		case state.StatusFailed:
			failed++
		}
	}

	fmt.Fprintf(w, "%s %s — %s %d/%d — %d of %d tasks complete",
		ui.BoldCyan("🧵 Beadloom"),
		ui.Dim(""),
		ui.Bold("Wave"),
		r.State.CurrentWave+1, r.State.TotalWaves, completed, r.Plan.TotalTasks)
	if failed > 0 {
		fmt.Fprintf(w, " %s", ui.Red(fmt.Sprintf("(%d failed)", failed)))
	}
	fmt.Fprintf(w, " %s\n\n", ui.Dim(fmt.Sprintf("[%s elapsed]", elapsed)))

	for _, wave := range r.Plan.Waves {
		waveStatus := r.waveStatus(wave.Index)
		fmt.Fprintf(w, "  🌊 %s %d (%s)\n", ui.BoldWhite("WAVE"), wave.Index+1, ui.WaveStatus(waveStatus))

		for _, task := range wave.Tasks {
			r.printTask(w, task)
		}
		fmt.Fprintln(w)
	}
}

func (r *Reporter) waveStatus(waveIndex int) string {
	if waveIndex < r.State.CurrentWave {
		return "done"
	}
	if waveIndex == r.State.CurrentWave {
		return "running"
	}
	return "blocked"
}

func (r *Reporter) printTask(w io.Writer, task planner.PlannedTask) {
	ss := r.State.GetSession(task.TaskID)

	status := "pending"
	dur := ""
	if ss != nil {
		switch ss.Status {
		case state.StatusCompleted:
			status = "completed"
			if ss.StartedAt != nil && ss.FinishedAt != nil {
				dur = ui.Dim(fmt.Sprintf("[%s]", ss.FinishedAt.Sub(*ss.StartedAt).Truncate(time.Second)))
			}
		case state.StatusRunning:
			status = "running"
			if ss.StartedAt != nil {
				dur = ui.Cyan(fmt.Sprintf("[running %s]", time.Since(*ss.StartedAt).Truncate(time.Second)))
			}
		case state.StatusFailed:
			status = "failed"
			if ss.StartedAt != nil && ss.FinishedAt != nil {
				dur = ui.Red(fmt.Sprintf("[failed after %s]", ss.FinishedAt.Sub(*ss.StartedAt).Truncate(time.Second)))
			}
		case state.StatusCancelled:
			status = "cancelled"
			dur = ui.Dim("[cancelled]")
		}
	}

	icon := ui.StatusIcon(status)

	critical := " "
	if task.IsCritical {
		critical = ui.BoldYellow("⚡")
	}

	title := task.Title
	if len(title) > 40 {
		title = title[:37] + "..."
	}

	taskID := ui.BoldMagenta(task.TaskID)

	fmt.Fprintf(w, "    %s %-8s %-40s %s  %s\n", icon, taskID, title, critical, dur)
}

// JSON returns machine-readable status.
func (r *Reporter) JSON() ([]byte, error) {
	type taskStatus struct {
		TaskID     string `json:"task_id"`
		Title      string `json:"title"`
		Status     string `json:"status"`
		IsCritical bool   `json:"is_critical"`
		Wave       int    `json:"wave"`
	}

	type output struct {
		PlanID      string       `json:"plan_id"`
		Status      string       `json:"status"`
		CurrentWave int          `json:"current_wave"`
		TotalWaves  int          `json:"total_waves"`
		TotalTasks  int          `json:"total_tasks"`
		Elapsed     string       `json:"elapsed"`
		Tasks       []taskStatus `json:"tasks"`
	}

	o := output{
		PlanID:      r.Plan.ID,
		Status:      r.State.Status,
		CurrentWave: r.State.CurrentWave,
		TotalWaves:  r.State.TotalWaves,
		TotalTasks:  r.Plan.TotalTasks,
		Elapsed:     time.Since(r.StartTime).Truncate(time.Second).String(),
	}

	for _, wave := range r.Plan.Waves {
		for _, task := range wave.Tasks {
			ts := taskStatus{
				TaskID:     task.TaskID,
				Title:      task.Title,
				IsCritical: task.IsCritical,
				Wave:       wave.Index,
				Status:     "pending",
			}
			if ss := r.State.GetSession(task.TaskID); ss != nil {
				ts.Status = string(ss.Status)
			}
			o.Tasks = append(o.Tasks, ts)
		}
	}

	return json.MarshalIndent(o, "", "  ")
}

// Summary returns a final summary string.
func (r *Reporter) Summary() string {
	var b strings.Builder
	elapsed := time.Since(r.StartTime).Truncate(time.Second)

	completed := 0
	failed := 0
	for _, ss := range r.State.Sessions {
		switch ss.Status {
		case state.StatusCompleted:
			completed++
		case state.StatusFailed:
			failed++
		}
	}

	statusText := ui.BoldGreen("completed")
	statusEmoji := "✅"
	if r.State.Status == "failed" {
		statusText = ui.BoldRed("failed")
		statusEmoji = "❌"
	} else if r.State.Status == "cancelled" {
		statusText = ui.Yellow("cancelled")
		statusEmoji = "🚫"
	}

	fmt.Fprintf(&b, "\n%s %s\n", statusEmoji, ui.BoldCyan("Beadloom Run Complete"))
	fmt.Fprintf(&b, "%s\n", ui.Cyan("═════════════════════════"))
	fmt.Fprintf(&b, "Plan:      %s\n", ui.Dim(r.Plan.ID))
	fmt.Fprintf(&b, "Duration:  %s\n", ui.Bold(elapsed))
	fmt.Fprintf(&b, "Tasks:     %s, %s, %d total\n",
		ui.Green(fmt.Sprintf("%d completed", completed)),
		ui.Red(fmt.Sprintf("%d failed", failed)),
		r.Plan.TotalTasks)
	fmt.Fprintf(&b, "Status:    %s\n", statusText)

	if failed > 0 {
		fmt.Fprintf(&b, "\n%s\n", ui.BoldRed("Failed tasks:"))
		for taskID, ss := range r.State.Sessions {
			if ss.Status == state.StatusFailed {
				fmt.Fprintf(&b, "  %s %s  %s\n", ui.Red("✗"), ui.BoldMagenta(taskID), ui.Dim("(log: "+ss.LogFile+")"))
			}
		}
	}

	return b.String()
}
