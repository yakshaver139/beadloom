package cpm

// CPMResult holds the complete critical path analysis.
type CPMResult struct {
	Tasks         map[string]*TaskSchedule
	CriticalPath  []string // ordered task IDs on critical path
	TotalDuration int
	Waves         []Wave // parallelizable groups
	TopoOrder     []string
}

// TaskSchedule holds the scheduling info for a single task.
type TaskSchedule struct {
	TaskID     string
	ES, EF     int // earliest start/finish
	LS, LF     int // latest start/finish
	Slack      int
	IsCritical bool
	Wave       int // which parallel wave this belongs to
}

// Wave represents a group of tasks that can execute in parallel.
type Wave struct {
	Index      int
	TaskIDs    []string
	IsCritical bool // true if wave contains critical path tasks
}
