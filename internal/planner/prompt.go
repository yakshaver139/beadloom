package planner

import (
	"bytes"
	"os"
	"text/template"
)

const defaultPromptTemplate = `You are working on task {{.TaskID}}: {{.Title}}

## Description
{{.Description}}

## Acceptance Criteria
{{.Acceptance}}
{{if .Design}}
## Design Notes
{{.Design}}
{{end}}
## Instructions
1. Implement the changes described above
2. Write or update tests as needed
3. Run existing tests to ensure nothing breaks
4. When done, run: bd close {{.TaskID}} --reason "COMPLETED: <summary>"
5. If you get blocked, run: bd update {{.TaskID}} --status blocked --append-notes "BLOCKER: <description>"

## Context
- You are in a git worktree at {{.WorktreePath}}
- Branch: {{.BranchName}}
- This task is part of wave {{.WaveIndex}} ({{.WaveSize}} tasks in parallel)
{{- if .IsCritical}}
- This task is on the CRITICAL PATH — it directly affects total project duration
{{- end}}
`

// PromptData holds the data used to render a prompt template.
type PromptData struct {
	TaskID       string
	Title        string
	Description  string
	Acceptance   string
	Design       string
	Notes        string
	WorktreePath string
	BranchName   string
	WaveIndex    int
	WaveSize     int
	IsCritical   bool
}

// RenderPrompt renders a prompt for a task using either a custom template file or the default.
func RenderPrompt(data PromptData, templatePath string) (string, error) {
	tmplStr := defaultPromptTemplate
	if templatePath != "" {
		content, err := os.ReadFile(templatePath)
		if err != nil {
			return "", err
		}
		tmplStr = string(content)
	}

	tmpl, err := template.New("prompt").Parse(tmplStr)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
