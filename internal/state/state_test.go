package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshharrison/beadloom/internal/planner"
)

func TestNewAndLoad(t *testing.T) {
	// Clean up
	defer os.RemoveAll(".beadloom")

	s, err := New("test-plan-001", 3, 5)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if s.PlanID != "test-plan-001" {
		t.Errorf("expected plan ID test-plan-001, got %s", s.PlanID)
	}
	if s.Status != "running" {
		t.Errorf("expected status running, got %s", s.Status)
	}
	if s.TotalWaves != 3 {
		t.Errorf("expected 3 total waves, got %d", s.TotalWaves)
	}

	// Load from disk
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.PlanID != "test-plan-001" {
		t.Errorf("loaded plan ID mismatch: %s", loaded.PlanID)
	}
	if loaded.TotalWaves != 3 {
		t.Errorf("loaded total waves mismatch: %d", loaded.TotalWaves)
	}
}

func TestUpdateSession(t *testing.T) {
	defer os.RemoveAll(".beadloom")

	s, err := New("test-plan-002", 1, 3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ss := &SessionState{
		Status:   StatusRunning,
		Worktree: ".worktrees/bd-123",
		Branch:   "beadloom/bd-123",
		PID:      12345,
		LogFile:  ".worktrees/bd-123/beadloom.log",
	}

	if err := s.UpdateSession("bd-123", ss); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}

	got := s.GetSession("bd-123")
	if got == nil {
		t.Fatal("expected session, got nil")
	}
	if got.Status != StatusRunning {
		t.Errorf("expected running, got %s", got.Status)
	}
	if got.PID != 12345 {
		t.Errorf("expected PID 12345, got %d", got.PID)
	}
}

func TestActivePIDs(t *testing.T) {
	defer os.RemoveAll(".beadloom")

	s, err := New("test-plan-003", 1, 3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	s.UpdateSession("a", &SessionState{Status: StatusRunning, PID: 100})
	s.UpdateSession("b", &SessionState{Status: StatusCompleted, PID: 200})
	s.UpdateSession("c", &SessionState{Status: StatusRunning, PID: 300})

	pids := s.ActivePIDs()
	if len(pids) != 2 {
		t.Errorf("expected 2 active PIDs, got %d: %v", len(pids), pids)
	}
}

func TestExists(t *testing.T) {
	defer os.RemoveAll(".beadloom")

	if Exists() {
		t.Error("expected Exists()=false before creation")
	}

	New("test", 1, 1)

	if !Exists() {
		t.Error("expected Exists()=true after creation")
	}

	Clean()

	if Exists() {
		t.Error("expected Exists()=false after Clean()")
	}
}

func TestSetWaveAndStatus(t *testing.T) {
	defer os.RemoveAll(".beadloom")

	s, err := New("test-plan-004", 5, 10)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	s.SetWave(2)
	s.SetStatus("completed")

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.CurrentWave != 2 {
		t.Errorf("expected wave 2, got %d", loaded.CurrentWave)
	}
	if loaded.Status != "completed" {
		t.Errorf("expected completed, got %s", loaded.Status)
	}
}

func testPlan(id string) *planner.ExecutionPlan {
	return &planner.ExecutionPlan{
		ID:         id,
		CreatedAt:  time.Now(),
		TotalTasks: 2,
		TotalWaves: 1,
		Waves: []planner.ExecutionWave{
			{Index: 0, Tasks: []planner.PlannedTask{
				{TaskID: "t1", Title: "Task 1"},
				{TaskID: "t2", Title: "Task 2"},
			}},
		},
		Tasks: map[string]*planner.PlannedTask{
			"t1": {TaskID: "t1", Title: "Task 1"},
			"t2": {TaskID: "t2", Title: "Task 2"},
		},
	}
}

func TestSavePlanAndLoadPlan(t *testing.T) {
	defer os.RemoveAll(".beadloom")

	os.MkdirAll(stateDir, 0755)

	plan := testPlan("loom-20260220-100000-abc")
	if err := SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}

	if !PlanExists() {
		t.Fatal("expected PlanExists()=true after SavePlan")
	}

	loaded, err := LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}

	if loaded.ID != plan.ID {
		t.Errorf("expected plan ID %s, got %s", plan.ID, loaded.ID)
	}
	if loaded.TotalTasks != 2 {
		t.Errorf("expected 2 total tasks, got %d", loaded.TotalTasks)
	}
}

func TestArchiveAndLoadArchived(t *testing.T) {
	defer os.RemoveAll(".beadloom")

	planID := "loom-20260220-100000-abc"

	// Create state and plan
	_, err := New(planID, 1, 2)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	plan := testPlan(planID)
	if err := SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}

	// Archive
	if err := Archive(); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	// Verify archive directory exists
	archiveDir := filepath.Join(stateDir, historyDir, planID)
	if _, err := os.Stat(archiveDir); os.IsNotExist(err) {
		t.Fatal("expected archive directory to exist")
	}

	// Load archived state
	st, err := LoadArchived(planID)
	if err != nil {
		t.Fatalf("LoadArchived: %v", err)
	}
	if st.PlanID != planID {
		t.Errorf("expected plan ID %s, got %s", planID, st.PlanID)
	}

	// Load archived plan
	loadedPlan, err := LoadArchivedPlan(planID)
	if err != nil {
		t.Fatalf("LoadArchivedPlan: %v", err)
	}
	if loadedPlan.ID != planID {
		t.Errorf("expected plan ID %s, got %s", planID, loadedPlan.ID)
	}
}

func TestListHistory(t *testing.T) {
	defer os.RemoveAll(".beadloom")

	// Empty history
	ids, err := ListHistory()
	if err != nil {
		t.Fatalf("ListHistory (empty): %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 history entries, got %d", len(ids))
	}

	// Create two archived runs with different timestamps
	for _, id := range []string{"loom-20260220-090000-aaa", "loom-20260220-110000-bbb"} {
		if _, err := New(id, 1, 1); err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := SavePlan(testPlan(id)); err != nil {
			t.Fatalf("SavePlan: %v", err)
		}
		if err := Archive(); err != nil {
			t.Fatalf("Archive: %v", err)
		}
	}

	ids, err = ListHistory()
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(ids))
	}
	// Newest first
	if ids[0] != "loom-20260220-110000-bbb" {
		t.Errorf("expected newest first, got %s", ids[0])
	}
	if ids[1] != "loom-20260220-090000-aaa" {
		t.Errorf("expected oldest second, got %s", ids[1])
	}
}

func TestLoadPrevious(t *testing.T) {
	defer os.RemoveAll(".beadloom")

	// No history — should error
	_, _, err := LoadPrevious()
	if err == nil {
		t.Fatal("expected error from LoadPrevious with no history")
	}

	// Create two archived runs
	for _, id := range []string{"loom-20260220-090000-old", "loom-20260220-110000-new"} {
		if _, err := New(id, 1, 1); err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := SavePlan(testPlan(id)); err != nil {
			t.Fatalf("SavePlan: %v", err)
		}
		if err := Archive(); err != nil {
			t.Fatalf("Archive: %v", err)
		}
	}

	st, plan, err := LoadPrevious()
	if err != nil {
		t.Fatalf("LoadPrevious: %v", err)
	}
	if st.PlanID != "loom-20260220-110000-new" {
		t.Errorf("expected newest plan ID, got %s", st.PlanID)
	}
	if plan.ID != "loom-20260220-110000-new" {
		t.Errorf("expected newest plan, got %s", plan.ID)
	}
}

func TestCleanCurrent(t *testing.T) {
	defer os.RemoveAll(".beadloom")

	// Create state, plan, and archive
	planID := "loom-20260220-100000-test"
	if _, err := New(planID, 1, 1); err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := SavePlan(testPlan(planID)); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := Archive(); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	// CleanCurrent should remove state.json and plan.json but keep history
	if err := CleanCurrent(); err != nil {
		t.Fatalf("CleanCurrent: %v", err)
	}

	if Exists() {
		t.Error("expected state.json to be removed")
	}
	if PlanExists() {
		t.Error("expected plan.json to be removed")
	}

	// History should still be there
	if !HistoryExists() {
		t.Error("expected history to be preserved")
	}

	ids, _ := ListHistory()
	if len(ids) != 1 || ids[0] != planID {
		t.Errorf("expected history entry %s, got %v", planID, ids)
	}
}

func TestHistoryExists(t *testing.T) {
	defer os.RemoveAll(".beadloom")

	if HistoryExists() {
		t.Error("expected HistoryExists()=false with no history")
	}

	planID := "loom-20260220-100000-test"
	New(planID, 1, 1)
	SavePlan(testPlan(planID))
	Archive()

	if !HistoryExists() {
		t.Error("expected HistoryExists()=true after archive")
	}
}
