package graph

// Task represents a single task from the beads database.
type Task struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Priority     int      `json:"priority"`
	Type         string   `json:"issue_type"`
	Labels       []string `json:"labels,omitempty"`
	BlockedBy    []string // populated from bd dep list
	Blocks       []string // populated from bd dep list
	EstimateMins int      // from bd's "estimate" field (minutes)
	Description  string   `json:"description"`
	Acceptance   string   `json:"acceptance_criteria"`
	Notes        string   `json:"notes"`
	Design       string   `json:"design,omitempty"`
}

// TaskGraph is a directed acyclic graph of tasks.
type TaskGraph struct {
	Tasks  map[string]*Task
	Adj    map[string][]string // task -> tasks it blocks
	RevAdj map[string][]string // task -> tasks that block it
	Roots  []string            // tasks with no blockers
	Leaves []string            // tasks that block nothing
}
