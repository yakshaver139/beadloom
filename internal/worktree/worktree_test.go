package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joshharrison/beadloom/internal/bd"
)

func TestNewManager_Defaults(t *testing.T) {
	c := bd.NewClient("", "")
	m := NewManager("", c)
	if m.BaseDir != ".worktrees" {
		t.Errorf("expected default base dir '.worktrees', got %q", m.BaseDir)
	}
}

func TestNewManager_Custom(t *testing.T) {
	c := bd.NewClient("", "")
	m := NewManager("/tmp/wt", c)
	if m.BaseDir != "/tmp/wt" {
		t.Errorf("expected custom base dir, got %q", m.BaseDir)
	}
}

func TestPath(t *testing.T) {
	c := bd.NewClient("", "")
	m := NewManager(".worktrees", c)
	got := m.Path("bd-123")
	expected := filepath.Join(".worktrees", "bd-123")
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestCreate_ExistingDir(t *testing.T) {
	// Create a temp dir to simulate existing worktree
	tmpDir, err := os.MkdirTemp("", "beadloom-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	taskDir := filepath.Join(tmpDir, "bd-123")
	os.MkdirAll(taskDir, 0755)

	c := bd.NewClient("", "")
	m := NewManager(tmpDir, c)

	// Should return existing path without calling bd
	path, err := m.Create("bd-123", "beadloom/bd-123")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if path != taskDir {
		t.Errorf("expected %q, got %q", taskDir, path)
	}
}
