package bd

import (
	"testing"
)

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient("", "")
	if c.BdBin != "bd" {
		t.Errorf("expected default bd binary 'bd', got %q", c.BdBin)
	}
	if c.DbPath != "" {
		t.Errorf("expected empty db path, got %q", c.DbPath)
	}
}

func TestNewClient_Custom(t *testing.T) {
	c := NewClient("/usr/local/bin/bd", "/path/to/db")
	if c.BdBin != "/usr/local/bin/bd" {
		t.Errorf("expected custom bd binary, got %q", c.BdBin)
	}
	if c.DbPath != "/path/to/db" {
		t.Errorf("expected custom db path, got %q", c.DbPath)
	}
}

func TestBaseArgs_WithDB(t *testing.T) {
	c := NewClient("bd", "/my/db")
	args := c.baseArgs()
	if len(args) != 2 || args[0] != "--db" || args[1] != "/my/db" {
		t.Errorf("expected [--db /my/db], got %v", args)
	}
}

func TestBaseArgs_WithoutDB(t *testing.T) {
	c := NewClient("bd", "")
	args := c.baseArgs()
	if len(args) != 0 {
		t.Errorf("expected empty args, got %v", args)
	}
}
