package ui

import (
	"fmt"
	"os"

	"github.com/fatih/color"
)

// Sprint color functions for building styled strings.
var (
	Bold        = color.New(color.Bold).SprintFunc()
	Dim         = color.New(color.Faint).SprintFunc()
	Cyan        = color.New(color.FgCyan).SprintFunc()
	Green       = color.New(color.FgGreen).SprintFunc()
	Red         = color.New(color.FgRed).SprintFunc()
	Yellow      = color.New(color.FgYellow).SprintFunc()
	Magenta     = color.New(color.FgMagenta).SprintFunc()
	BoldCyan    = color.New(color.Bold, color.FgCyan).SprintFunc()
	BoldGreen   = color.New(color.Bold, color.FgGreen).SprintFunc()
	BoldRed     = color.New(color.Bold, color.FgRed).SprintFunc()
	BoldYellow  = color.New(color.Bold, color.FgYellow).SprintFunc()
	BoldMagenta = color.New(color.Bold, color.FgMagenta).SprintFunc()
	BoldWhite   = color.New(color.Bold, color.FgWhite).SprintFunc()
)

// PrintLogo renders the colored beadloom logo to stderr.
func PrintLogo() {
	w := os.Stderr
	frame := color.New(color.FgCyan)
	beads := color.New(color.FgYellow)
	threads := color.New(color.FgCyan, color.Faint)
	sep := color.New(color.FgCyan)
	brand := color.New(color.Bold, color.FgMagenta)
	tag := color.New(color.Faint)

	fmt.Fprintln(w)
	frame.Fprintln(w, "   +--------------------------+")
	beads.Fprintln(w, "   |  o  o  o  o  o  o  o  o  |")
	threads.Fprintln(w, "   |  |  |  |  |  |  |  |  |  |")
	sep.Fprintln(w, "   |==========================|")
	brand.Fprintln(w, "   |  B  E  A  D  L  O  O  M  |")
	sep.Fprintln(w, "   |==========================|")
	threads.Fprintln(w, "   |  |  |  |  |  |  |  |  |  |")
	beads.Fprintln(w, "   |  o  o  o  o  o  o  o  o  |")
	frame.Fprintln(w, "   +--------------------------+")
	tag.Fprintf(w, "   %s Parallel task orchestration\n", Dim("🧵"))
	fmt.Fprintln(w)
}

// taskColors is a palette of distinct bold colors for differentiating tasks.
var taskColors = []func(a ...interface{}) string{
	BoldMagenta,
	BoldCyan,
	BoldYellow,
	BoldGreen,
	color.New(color.Bold, color.FgHiBlue).SprintFunc(),
	color.New(color.Bold, color.FgHiRed).SprintFunc(),
}

// taskColorIndex hashes a task ID to a palette index.
func taskColorIndex(taskID string) int {
	var h uint32
	for _, c := range taskID {
		h = h*31 + uint32(c)
	}
	return int(h % uint32(len(taskColors)))
}

// TaskPrefix returns a colored [task-id] prefix string.
// Each task ID gets a distinct color from the palette.
func TaskPrefix(taskID string) string {
	c := taskColors[taskColorIndex(taskID)]
	return Dim("[") + c(taskID) + Dim("]")
}

// StatusIcon returns a colored status icon for compact table display.
func StatusIcon(status string) string {
	switch status {
	case "completed":
		return Green("✓")
	case "running":
		return Cyan("●")
	case "failed":
		return Red("✗")
	case "skipped":
		return Yellow("⊘")
	case "cancelled":
		return Dim("⊘")
	default:
		return Dim("◌")
	}
}

// WaveStatus returns a colored wave status string.
func WaveStatus(status string) string {
	switch status {
	case "done":
		return Green("done")
	case "running":
		return BoldCyan("running")
	default:
		return Dim("blocked")
	}
}
