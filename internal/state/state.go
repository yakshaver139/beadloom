package state

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joshharrison/beadloom/internal/planner"
)

const stateDir = ".beadloom"
const stateFile = "state.json"
const planFile = "plan.json"
const historyDir = "history"

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

// ensureGitignore adds .beadloom to .gitignore if not already present.
func ensureGitignore() {
	const entry = ".beadloom"
	const gitignore = ".gitignore"

	// Check if already present
	if f, err := os.Open(gitignore); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == entry {
				f.Close()
				return
			}
		}
		f.Close()
	}

	// Append entry
	f, err := os.OpenFile(gitignore, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	// Check if file needs a leading newline
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		buf := make([]byte, 1)
		if rf, err := os.Open(gitignore); err == nil {
			rf.Seek(info.Size()-1, 0)
			rf.Read(buf)
			rf.Close()
			if buf[0] != '\n' {
				f.WriteString("\n")
			}
		}
	}
	f.WriteString(entry + "\n")
}

// New creates a new RunState and persists it.
func New(planID string, totalWaves int, totalTasks int) (*RunState, error) {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	ensureGitignore()

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

// SavePlan writes an execution plan to .beadloom/plan.json.
func SavePlan(plan *planner.ExecutionPlan) error {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}
	return os.WriteFile(filepath.Join(stateDir, planFile), data, 0644)
}

// LoadPlan reads the current execution plan from .beadloom/plan.json.
func LoadPlan() (*planner.ExecutionPlan, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, planFile))
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	var plan planner.ExecutionPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("parse plan: %w", err)
	}
	return &plan, nil
}

// Archive copies state.json and plan.json into .beadloom/history/<plan-id>/.
func Archive() error {
	st, err := Load()
	if err != nil {
		return fmt.Errorf("load state for archive: %w", err)
	}

	dir := filepath.Join(stateDir, historyDir, st.PlanID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}

	// Copy state.json
	stateData, err := os.ReadFile(filepath.Join(stateDir, stateFile))
	if err != nil {
		return fmt.Errorf("read state for archive: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFile), stateData, 0644); err != nil {
		return fmt.Errorf("write archived state: %w", err)
	}

	// Copy plan.json (if it exists)
	planData, err := os.ReadFile(filepath.Join(stateDir, planFile))
	if err == nil {
		if err := os.WriteFile(filepath.Join(dir, planFile), planData, 0644); err != nil {
			return fmt.Errorf("write archived plan: %w", err)
		}
	}

	return nil
}

// LoadArchived loads a RunState from .beadloom/history/<planID>/state.json.
func LoadArchived(planID string) (*RunState, error) {
	path := filepath.Join(stateDir, historyDir, planID, stateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read archived state: %w", err)
	}
	var s RunState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse archived state: %w", err)
	}
	s.path = path
	return &s, nil
}

// LoadArchivedPlan loads an ExecutionPlan from .beadloom/history/<planID>/plan.json.
func LoadArchivedPlan(planID string) (*planner.ExecutionPlan, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, historyDir, planID, planFile))
	if err != nil {
		return nil, fmt.Errorf("read archived plan: %w", err)
	}
	var plan planner.ExecutionPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("parse archived plan: %w", err)
	}
	return &plan, nil
}

// ListHistory returns archived plan IDs sorted newest-first.
// Plan IDs contain a timestamp prefix (e.g. "20060102-150405-xxxxx") so
// reverse-lexicographic order gives newest first.
func ListHistory() ([]string, error) {
	dir := filepath.Join(stateDir, historyDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read history dir: %w", err)
	}

	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}

	// Sort newest-first: plan IDs start with a timestamp, so reverse-lexicographic works.
	// For IDs without timestamps, fall back to the directory mod time.
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] > ids[j]
	})

	return ids, nil
}

// LoadPrevious loads the most recent archived run's state and plan.
func LoadPrevious() (*RunState, *planner.ExecutionPlan, error) {
	ids, err := ListHistory()
	if err != nil {
		return nil, nil, err
	}
	if len(ids) == 0 {
		return nil, nil, fmt.Errorf("no previous runs found")
	}

	st, err := LoadArchived(ids[0])
	if err != nil {
		return nil, nil, err
	}
	plan, err := LoadArchivedPlan(ids[0])
	if err != nil {
		return nil, nil, err
	}
	return st, plan, nil
}

// CleanCurrent removes only the current run files (state.json, plan.json),
// preserving the history/ directory.
func CleanCurrent() error {
	os.Remove(filepath.Join(stateDir, stateFile))
	os.Remove(filepath.Join(stateDir, planFile))

	// If the directory is now empty (no history), remove it entirely
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil // directory already gone
	}
	if len(entries) == 0 {
		return os.Remove(stateDir)
	}
	return nil
}

// HistoryExists checks if there are any archived runs.
func HistoryExists() bool {
	ids, err := ListHistory()
	return err == nil && len(ids) > 0
}

// PlanExists checks if a plan.json file exists.
func PlanExists() bool {
	_, err := os.Stat(filepath.Join(stateDir, planFile))
	return err == nil
}
