package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// TaskSummary is the minimal task info sent to Claude for dependency inference.
type TaskSummary struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
	Type     string `json:"type"`
}

// DepEdge is a single inferred dependency.
type DepEdge struct {
	BlockedID string `json:"blocked_id"` // task that is blocked
	BlockerID string `json:"blocker_id"` // task that must finish first
	Reason    string `json:"reason"`
}

// InferDepsResult holds the full response from Claude.
type InferDepsResult struct {
	Edges   []DepEdge `json:"edges"`
	Summary string    `json:"summary"`
}

// Client wraps the Anthropic SDK for Claude API calls.
type Client struct {
	inner anthropic.Client
	model anthropic.Model
}

// NewClient creates a Claude client. apiKey defaults to ANTHROPIC_API_KEY env.
// model defaults to Claude Sonnet.
func NewClient(apiKey, model string) (*Client, error) {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	inner := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)

	m := anthropic.ModelClaudeSonnet4_6
	if model != "" {
		m = anthropic.Model(model)
	}

	return &Client{inner: inner, model: m}, nil
}

const inferDepsPrompt = `You are an expert software project manager. Given a list of tasks from a software project, infer dependency edges between them.

Rules:
- Only add a dependency when there is a strong causal reason (task B cannot start until task A is complete).
- Prefer fewer edges — do not add transitive or speculative dependencies.
- Do not create cycles.
- Only use task IDs from the provided list.
- A task cannot depend on itself.

Return your answer as JSON with this exact structure:
{
  "edges": [
    {"blocked_id": "<task that is blocked>", "blocker_id": "<task that must finish first>", "reason": "<short explanation>"}
  ],
  "summary": "<one paragraph summary of the dependency structure>"
}

Return ONLY the JSON object. No markdown fences, no commentary outside the JSON.

Here are the tasks:
`

// buildPrompt constructs the full prompt for dependency inference.
func buildPrompt(tasks []TaskSummary) (string, error) {
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal tasks: %w", err)
	}
	return inferDepsPrompt + string(data), nil
}

// InferDeps calls the Claude API to infer task dependencies.
func (c *Client) InferDeps(ctx context.Context, tasks []TaskSummary) (*InferDepsResult, error) {
	prompt, err := buildPrompt(tasks)
	if err != nil {
		return nil, err
	}

	resp, err := c.inner.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: int64(4096),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("claude API call: %w", err)
	}

	// Extract text from response
	var text string
	for _, block := range resp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	text = stripJSONFences(text)

	var result InferDepsResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("parse claude response: %w\nraw: %s", err, text)
	}

	return &result, nil
}

const summariseRunPrompt = `You are a technical project manager summarising a beadloom orchestration run.

You will receive:
1. A structured run summary (plan ID, status, waves, task outcomes).
2. Agent session logs for each task (truncated to the last ~2000 lines).

Produce a concise narrative summary covering:
- What each task accomplished (or why it failed/was skipped).
- Any notable issues, warnings, or unexpected behaviour.
- An overall assessment of the run.

Keep it concise — aim for 1-2 sentences per task and a short overall paragraph.
Do not repeat raw log content verbatim. Focus on the human-readable takeaway.
`

// SummariseRun sends a run summary and task logs to Claude and returns a
// human-readable narrative of what each agent accomplished or why it failed.
func (c *Client) SummariseRun(ctx context.Context, runSummary string, taskLogs map[string]string) (string, error) {
	var userContent strings.Builder
	userContent.WriteString("## Run Summary\n\n")
	userContent.WriteString(runSummary)
	userContent.WriteString("\n\n## Agent Session Logs\n\n")

	for taskID, log := range taskLogs {
		userContent.WriteString(fmt.Sprintf("### Task: %s\n```\n%s\n```\n\n", taskID, log))
	}

	resp, err := c.inner.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: int64(4096),
		System: []anthropic.TextBlockParam{
			{Text: summariseRunPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userContent.String())),
		},
	})
	if err != nil {
		return "", fmt.Errorf("claude API call: %w", err)
	}

	var text string
	for _, block := range resp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	return strings.TrimSpace(text), nil
}

// stripJSONFences removes markdown code fences that Claude sometimes adds.
func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	// Remove ```json ... ``` or ``` ... ```
	if strings.HasPrefix(s, "```") {
		// Strip opening fence line
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		// Strip closing fence
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}
	return s
}
