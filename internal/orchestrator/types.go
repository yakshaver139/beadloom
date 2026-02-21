package orchestrator

import (
	"time"
)

// Config holds orchestrator configuration.
type Config struct {
	MaxParallel    int
	Safe           bool
	Quiet          bool
	Automerge      bool
	TimeoutPerTask time.Duration
	WorktreeDir    string
	DbPath         string
	ClaudeBin      string // path to claude binary (default: "claude")
	GitTrace       bool   // log every git command and its output to stderr
}

// SessionStatus represents the status of an agent session.
type SessionStatus string

const (
	StatusPending   SessionStatus = "pending"
	StatusRunning   SessionStatus = "running"
	StatusCompleted SessionStatus = "completed"
	StatusFailed    SessionStatus = "failed"
	StatusCancelled SessionStatus = "cancelled"
	StatusSkipped   SessionStatus = "skipped"
)

// taskResult communicates task completion from worker goroutines to the main event loop.
type taskResult struct {
	TaskID   string
	Err      error
	Critical bool
}

// AgentSession tracks a running Claude agent.
type AgentSession struct {
	TaskID       string
	WorktreePath string
	Status       SessionStatus
	StartedAt    time.Time
	FinishedAt   time.Time
	ExitCode     int
	LogFile      string
	PID          int
}
