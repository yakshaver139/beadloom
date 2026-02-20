package state

import (
	"os"
	"testing"
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
