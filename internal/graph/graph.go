package graph

import (
	"fmt"
	"log"
	"sort"

	"github.com/joshharrison/beadloom/internal/bd"
)

// Build constructs a TaskGraph from the bd client by listing open tasks
// and fetching their dependencies.
func Build(client *bd.Client) (*TaskGraph, error) {
	rawTasks, err := client.ListOpen()
	if err != nil {
		return nil, fmt.Errorf("list open tasks: %w", err)
	}

	// Fetch dependencies for each task
	for i := range rawTasks {
		blocks, blockedBy, err := client.Deps(rawTasks[i].ID)
		if err != nil {
			log.Printf("warning: failed to fetch deps for %s: %v", rawTasks[i].ID, err)
			continue
		}
		rawTasks[i].Blocks = blocks
		rawTasks[i].BlockedBy = blockedBy
	}

	return BuildFromRaw(rawTasks)
}

// BuildFromRaw constructs a TaskGraph from pre-fetched raw tasks.
func BuildFromRaw(rawTasks []bd.RawTask) (*TaskGraph, error) {
	g := &TaskGraph{
		Tasks:  make(map[string]*Task),
		Adj:    make(map[string][]string),
		RevAdj: make(map[string][]string),
	}

	// Index all tasks
	for i := range rawTasks {
		rt := &rawTasks[i]
		g.Tasks[rt.ID] = &Task{
			ID:           rt.ID,
			Title:        rt.Title,
			Status:       rt.Status,
			Priority:     rt.Priority,
			Type:         rt.Type,
			Labels:       rt.Labels,
			BlockedBy:    rt.BlockedBy,
			Blocks:       rt.Blocks,
			EstimateMins: rt.Estimate,
			Description:  rt.Description,
			Acceptance:   rt.Acceptance,
			Notes:        rt.Notes,
			Design:       rt.Design,
		}
	}

	// Build adjacency lists from both Blocks and BlockedBy for resilience.
	// If one direction of dep lookup fails, the other can still provide edges.
	edgeSet := make(map[[2]string]bool)
	addEdge := func(from, to string) {
		key := [2]string{from, to}
		if edgeSet[key] {
			return
		}
		edgeSet[key] = true
		g.Adj[from] = append(g.Adj[from], to)
		g.RevAdj[to] = append(g.RevAdj[to], from)
	}

	for id, task := range g.Tasks {
		for _, blocked := range task.Blocks {
			if _, ok := g.Tasks[blocked]; ok {
				addEdge(id, blocked)
			}
		}
		for _, blocker := range task.BlockedBy {
			if _, ok := g.Tasks[blocker]; ok {
				addEdge(blocker, id)
			}
		}
	}

	// Sort adjacency lists for deterministic ordering
	for k := range g.Adj {
		sort.Strings(g.Adj[k])
	}
	for k := range g.RevAdj {
		sort.Strings(g.RevAdj[k])
	}

	// Find roots (no blockers within the graph)
	for id := range g.Tasks {
		if len(g.RevAdj[id]) == 0 {
			g.Roots = append(g.Roots, id)
		}
	}
	sort.Strings(g.Roots)

	// Find leaves (blocks nothing within the graph)
	for id := range g.Tasks {
		if len(g.Adj[id]) == 0 {
			g.Leaves = append(g.Leaves, id)
		}
	}
	sort.Strings(g.Leaves)

	// Check for cycles
	if cycle := g.DetectCycle(); cycle != nil {
		return nil, fmt.Errorf("dependency cycle detected: %v", cycle)
	}

	return g, nil
}

// DetectCycle returns the cycle path if one exists, or nil if the graph is acyclic.
// Uses DFS with coloring: white (unvisited), gray (in progress), black (done).
func (g *TaskGraph) DetectCycle() []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)

	color := make(map[string]int)
	parent := make(map[string]string)

	var dfs func(node string) []string
	dfs = func(node string) []string {
		color[node] = gray
		for _, next := range g.Adj[node] {
			if color[next] == gray {
				// Found a cycle — reconstruct it
				cycle := []string{next, node}
				cur := node
				for cur != next {
					cur = parent[cur]
					cycle = append(cycle, cur)
				}
				// Reverse to get forward order
				for i, j := 0, len(cycle)-1; i < j; i, j = i+1, j-1 {
					cycle[i], cycle[j] = cycle[j], cycle[i]
				}
				return cycle
			}
			if color[next] == white {
				parent[next] = node
				if cycle := dfs(next); cycle != nil {
					return cycle
				}
			}
		}
		color[node] = black
		return nil
	}

	// Sort keys for deterministic detection
	ids := make([]string, 0, len(g.Tasks))
	for id := range g.Tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if color[id] == white {
			if cycle := dfs(id); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

// TaskCount returns the number of tasks in the graph.
func (g *TaskGraph) TaskCount() int {
	return len(g.Tasks)
}

// Filter returns a new TaskGraph containing only tasks matching the predicate.
// Dependencies are re-wired to skip filtered-out tasks.
func (g *TaskGraph) Filter(pred func(*Task) bool) (*TaskGraph, error) {
	var filtered []bd.RawTask
	for _, t := range g.Tasks {
		if pred(t) {
			filtered = append(filtered, bd.RawTask{
				ID:          t.ID,
				Title:       t.Title,
				Status:      t.Status,
				Priority:    t.Priority,
				Type:        t.Type,
				Labels:      t.Labels,
				BlockedBy:   t.BlockedBy,
				Blocks:      t.Blocks,
				Estimate:    t.EstimateMins,
				Description: t.Description,
				Acceptance:  t.Acceptance,
				Notes:       t.Notes,
				Design:      t.Design,
			})
		}
	}
	return BuildFromRaw(filtered)
}
