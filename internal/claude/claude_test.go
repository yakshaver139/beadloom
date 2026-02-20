package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripJSONFences_Clean(t *testing.T) {
	input := `{"edges": [], "summary": "no deps"}`
	got := stripJSONFences(input)
	if got != input {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestStripJSONFences_WithJSONTag(t *testing.T) {
	input := "```json\n{\"edges\": []}\n```"
	got := stripJSONFences(input)
	if got != `{"edges": []}` {
		t.Errorf("expected clean JSON, got %q", got)
	}
}

func TestStripJSONFences_WithPlainFence(t *testing.T) {
	input := "```\n{\"edges\": []}\n```"
	got := stripJSONFences(input)
	if got != `{"edges": []}` {
		t.Errorf("expected clean JSON, got %q", got)
	}
}

func TestStripJSONFences_WithWhitespace(t *testing.T) {
	input := "  \n```json\n{\"edges\": []}\n```\n  "
	got := stripJSONFences(input)
	if got != `{"edges": []}` {
		t.Errorf("expected clean JSON, got %q", got)
	}
}

func TestBuildPrompt_ContainsTaskData(t *testing.T) {
	tasks := []TaskSummary{
		{ID: "T1", Title: "Setup DB", Priority: 1, Type: "feature"},
		{ID: "T2", Title: "Add API", Priority: 2, Type: "feature"},
	}
	prompt, err := buildPrompt(tasks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "T1") || !strings.Contains(prompt, "Setup DB") {
		t.Error("prompt should contain task IDs and titles")
	}
	if !strings.Contains(prompt, "T2") || !strings.Contains(prompt, "Add API") {
		t.Error("prompt should contain all tasks")
	}
	if !strings.Contains(prompt, "strong causal reason") {
		t.Error("prompt should contain dependency rules")
	}
}

func TestInferDepsResult_Unmarshal(t *testing.T) {
	raw := `{
		"edges": [
			{"blocked_id": "T2", "blocker_id": "T1", "reason": "API needs DB"}
		],
		"summary": "T2 depends on T1"
	}`
	var result InferDepsResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(result.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(result.Edges))
	}
	if result.Edges[0].BlockedID != "T2" {
		t.Errorf("expected blocked_id=T2, got %s", result.Edges[0].BlockedID)
	}
	if result.Edges[0].BlockerID != "T1" {
		t.Errorf("expected blocker_id=T1, got %s", result.Edges[0].BlockerID)
	}
	if result.Summary != "T2 depends on T1" {
		t.Errorf("unexpected summary: %s", result.Summary)
	}
}
