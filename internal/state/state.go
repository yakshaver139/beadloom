package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const stateDir = ".beadloom"
const stateFile = "state.json"

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

// RunState is the persistent state of a beadloom execution.
type RunState struct {
	PlanID      string                   `json:"plan_id"`
	StartedAt   time.Time                `json:"started_at"`
	Status      string                   `json:"status"` // "running", "completed", "failed", "cancelled"
	CurrentWave int                      `json:"current_wave"`
	TotalWaves  int                      `json:"total_waves"`
	TotalTasks  int                      `json:"total_tasks"`
	Sessions    map[string]*SessionState `json:"sessions"`

	mu   sync.Mutex `json:"-"`
	path string     `json:"-"`
}

// SessionState is the persistent state of a single agent session.
type SessionState struct {
	Status     SessionStatus `json:"status"`
	Worktree   string        `json:"worktree"`
	Branch     string        `json:"branch"`
	StartedAt  *time.Time    `json:"started_at,omitempty"`
	FinishedAt *time.Time    `json:"finished_at,omitempty"`
	ExitCode   int           `json:"exit_code,omitempty"`
	PID        int           `json:"pid,omitempty"`
	LogFile    string        `json:"log_file"`
}

// New creates a new RunState and persists it.
func New(planID string, totalWaves int, totalTasks int) (*RunState, error) {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	s := &RunState{
		PlanID:     planID,
		StartedAt:  time.Now(),
		Status:     "running",
		TotalWaves: totalWaves,
		TotalTasks: totalTasks,
		Sessions:   make(map[string]*SessionState),
		path:       filepath.Join(stateDir, stateFile),
	}

	if err := s.Save(); err != nil {
		return nil, err
	}
	return s, nil
}

// Load reads existing state from disk.
func Load() (*RunState, error) {
	path := filepath.Join(stateDir, stateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}

	var s RunState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	s.path = path
	return &s, nil
}

// Exists checks if a state file exists.
func Exists() bool {
	_, err := os.Stat(filepath.Join(stateDir, stateFile))
	return err == nil
}

// Save persists the current state to disk.
func (s *RunState) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return os.WriteFile(s.path, data, 0644)
}

// SetWave updates the current wave index and saves.
func (s *RunState) SetWave(wave int) error {
	s.CurrentWave = wave
	return s.Save()
}

// SetStatus updates the overall run status and saves.
func (s *RunState) SetStatus(status string) error {
	s.Status = status
	return s.Save()
}

// UpdateSession updates a session's state and saves.
func (s *RunState) UpdateSession(taskID string, ss *SessionState) error {
	s.mu.Lock()
	s.Sessions[taskID] = ss
	s.mu.Unlock()
	return s.Save()
}

// GetSession returns the session state for a task.
func (s *RunState) GetSession(taskID string) *SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Sessions[taskID]
}

// ActivePIDs returns PIDs of all running sessions.
func (s *RunState) ActivePIDs() []int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var pids []int
	for _, ss := range s.Sessions {
		if ss.Status == StatusRunning && ss.PID > 0 {
			pids = append(pids, ss.PID)
		}
	}
	return pids
}

// Clean removes the state directory.
func Clean() error {
	return os.RemoveAll(stateDir)
}
