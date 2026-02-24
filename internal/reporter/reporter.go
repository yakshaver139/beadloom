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

	currentWave := r.computeCurrentWave()
	fmt.Fprintf(w, "%s %s — %s %d/%d — %d of %d tasks complete",
		ui.BoldCyan("🧵 Beadloom"),
		ui.Dim(""),
		ui.Bold("Wave"),
		currentWave+1, r.State.TotalWaves, completed, r.Plan.TotalTasks)
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

// computeCurrentWave derives the current wave index from session states.
// Returns the index of the first wave that has incomplete tasks, or the last
// wave index if all waves are done.
func (r *Reporter) computeCurrentWave() int {
	for _, wave := range r.Plan.Waves {
		for _, task := range wave.Tasks {
			ss := r.State.GetSession(task.TaskID)
			if ss == nil {
				return wave.Index
			}
			switch ss.Status {
			case state.StatusCompleted, state.StatusFailed, state.StatusSkipped:
				// terminal — keep checking
			default:
				return wave.Index
			}
		}
	}
	// All waves complete — return last wave index
	if len(r.Plan.Waves) > 0 {
		return r.Plan.Waves[len(r.Plan.Waves)-1].Index
	}
	return 0
}

func (r *Reporter) waveStatus(waveIndex int) string {
	// Derive status from session states instead of CurrentWave comparison
	wave := r.Plan.Waves[waveIndex]
	allDone := true
	anyRunning := false
	for _, task := range wave.Tasks {
		ss := r.State.GetSession(task.TaskID)
		if ss == nil {
			allDone = false
			continue
		}
		switch ss.Status {
		case state.StatusCompleted, state.StatusFailed, state.StatusSkipped:
			// terminal state
		case state.StatusRunning:
			anyRunning = true
			allDone = false
		default:
			allDone = false
		}
	}
	if allDone {
		return "done"
	}
	if anyRunning {
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
		case state.StatusSkipped:
			status = "skipped"
			dur = ui.Yellow("[skipped]")
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
		CurrentWave: r.computeCurrentWave(),
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

// PrintSummaryReport writes a detailed run summary to the given writer.
// It includes the plan header, per-wave breakdown with task outcomes and timing,
// and a footer with totals. The output is also returned as a string for reuse
// (e.g. as context for Claude narrative summaries).
func (r *Reporter) PrintSummaryReport(w io.Writer) string {
	var b strings.Builder
	mw := io.MultiWriter(w, &b) // write to both output and capture

	// --- Header ---
	elapsed := r.runDuration()

	statusText := ui.BoldGreen("completed")
	statusEmoji := "✅"
	if r.State.Status == "failed" {
		statusText = ui.BoldRed("failed")
		statusEmoji = "❌"
	} else if r.State.Status == "cancelled" {
		statusText = ui.Yellow("cancelled")
		statusEmoji = "🚫"
	}

	fmt.Fprintf(mw, "\n%s %s\n", statusEmoji, ui.BoldCyan("Beadloom Run Summary"))
	fmt.Fprintf(mw, "%s\n", ui.Cyan("══════════════════════════"))
	fmt.Fprintf(mw, "Plan:      %s\n", ui.Dim(r.Plan.ID))
	fmt.Fprintf(mw, "Status:    %s\n", statusText)
	fmt.Fprintf(mw, "Duration:  %s\n", ui.Bold(elapsed))
	fmt.Fprintf(mw, "Waves:     %d\n", r.Plan.TotalWaves)
	fmt.Fprintf(mw, "Tasks:     %d total\n\n", r.Plan.TotalTasks)

	// --- Per-wave breakdown ---
	for _, wave := range r.Plan.Waves {
		wStatus := r.waveStatus(wave.Index)
		fmt.Fprintf(mw, "  🌊 %s %d  %s  (%d tasks)\n",
			ui.BoldWhite("Wave"), wave.Index+1,
			ui.WaveStatus(wStatus), len(wave.Tasks))

		for _, task := range wave.Tasks {
			r.printSummaryTask(mw, task)
		}
		fmt.Fprintln(mw)
	}

	// --- Footer: totals ---
	completed := 0
	failed := 0
	skipped := 0
	cancelled := 0
	for _, ss := range r.State.Sessions {
		switch ss.Status {
		case state.StatusCompleted:
			completed++
		case state.StatusFailed:
			failed++
		case state.StatusSkipped:
			skipped++
		case state.StatusCancelled:
			cancelled++
		}
	}

	fmt.Fprintf(mw, "%s\n", ui.Cyan("──────────────────────────"))
	fmt.Fprintf(mw, "Totals:  %s  %s  %s",
		ui.Green(fmt.Sprintf("%d completed", completed)),
		ui.Red(fmt.Sprintf("%d failed", failed)),
		ui.Yellow(fmt.Sprintf("%d skipped", skipped)))
	if cancelled > 0 {
		fmt.Fprintf(mw, "  %s", ui.Dim(fmt.Sprintf("%d cancelled", cancelled)))
	}
	fmt.Fprintln(mw)

	// Critical path info
	if len(r.Plan.CriticalPath) > 0 {
		fmt.Fprintf(mw, "Critical:  %s\n",
			ui.BoldYellow("⚡ "+strings.Join(r.Plan.CriticalPath, " → ")))
	}

	// Failed task details
	if failed > 0 {
		fmt.Fprintf(mw, "\n%s\n", ui.BoldRed("Failed tasks:"))
		for taskID, ss := range r.State.Sessions {
			if ss.Status == state.StatusFailed {
				durStr := ""
				if ss.StartedAt != nil && ss.FinishedAt != nil {
					durStr = fmt.Sprintf(" after %s", ss.FinishedAt.Sub(*ss.StartedAt).Truncate(time.Second))
				}
				fmt.Fprintf(mw, "  %s %s%s  %s\n",
					ui.Red("✗"), ui.BoldMagenta(taskID),
					ui.Red(durStr),
					ui.Dim("(log: "+ss.LogFile+")"))
			}
		}
	}

	return b.String()
}

// printSummaryTask writes a single task line for the summary report.
func (r *Reporter) printSummaryTask(w io.Writer, task planner.PlannedTask) {
	ss := r.State.GetSession(task.TaskID)

	status := "pending"
	durStr := ""
	if ss != nil {
		status = string(ss.Status)
		if ss.StartedAt != nil && ss.FinishedAt != nil {
			d := ss.FinishedAt.Sub(*ss.StartedAt).Truncate(time.Second)
			durStr = fmt.Sprintf("%s", d)
		}
	}

	icon := ui.StatusIcon(status)
	critical := " "
	if task.IsCritical {
		critical = ui.BoldYellow("⚡")
	}

	title := task.Title
	if len(title) > 50 {
		title = title[:47] + "..."
	}

	timeCol := ""
	if durStr != "" {
		timeCol = ui.Dim(fmt.Sprintf("[%s]", durStr))
	}

	fmt.Fprintf(w, "    %s %s %-50s %s  %s\n", icon, ui.BoldMagenta(task.TaskID), title, critical, timeCol)
}

// runDuration returns the total run duration. For finished runs it uses the
// latest session finish time; for in-progress runs it uses time.Since.
func (r *Reporter) runDuration() time.Duration {
	// Try to find the latest finish time across all sessions
	var latest time.Time
	for _, ss := range r.State.Sessions {
		if ss.FinishedAt != nil && ss.FinishedAt.After(latest) {
			latest = *ss.FinishedAt
		}
	}
	if !latest.IsZero() {
		return latest.Sub(r.StartTime).Truncate(time.Second)
	}
	return time.Since(r.StartTime).Truncate(time.Second)
}

// Summary returns a final summary string.
func (r *Reporter) Summary() string {
	var b strings.Builder
	elapsed := time.Since(r.StartTime).Truncate(time.Second)

	completed := 0
	failed := 0
	skipped := 0
	for _, ss := range r.State.Sessions {
		switch ss.Status {
		case state.StatusCompleted:
			completed++
		case state.StatusFailed:
			failed++
		case state.StatusSkipped:
			skipped++
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
	fmt.Fprintf(&b, "Tasks:     %s, %s, %s, %d total\n",
		ui.Green(fmt.Sprintf("%d completed", completed)),
		ui.Red(fmt.Sprintf("%d failed", failed)),
		ui.Yellow(fmt.Sprintf("%d skipped", skipped)),
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
