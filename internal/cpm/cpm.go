package cpm

import (
	"fmt"
	"sort"

	"github.com/joshharrison/beadloom/internal/graph"
)

// Analyze performs critical path method analysis on a task graph.
// If a task has EstimateMins > 0, that is used as its duration; otherwise duration is 1.
func Analyze(g *graph.TaskGraph) (*CPMResult, error) {
	order, err := topoSort(g)
	if err != nil {
		return nil, err
	}

	durations := make(map[string]int)
	for id, t := range g.Tasks {
		if t.EstimateMins > 0 {
			durations[id] = t.EstimateMins
		} else {
			durations[id] = 1
		}
	}

	result := &CPMResult{
		Tasks:     make(map[string]*TaskSchedule),
		TopoOrder: order,
	}

	// Initialize schedules
	for _, id := range order {
		result.Tasks[id] = &TaskSchedule{TaskID: id}
	}

	// Forward pass: compute ES and EF
	for _, id := range order {
		ts := result.Tasks[id]
		// ES = max(EF of all predecessors)
		es := 0
		for _, pred := range g.RevAdj[id] {
			predTS := result.Tasks[pred]
			if predTS.EF > es {
				es = predTS.EF
			}
		}
		ts.ES = es
		ts.EF = es + durations[id]
	}

	// Total project duration
	totalDuration := 0
	for _, ts := range result.Tasks {
		if ts.EF > totalDuration {
			totalDuration = ts.EF
		}
	}
	result.TotalDuration = totalDuration

	// Backward pass: compute LS and LF
	// Initialize leaves with LF = totalDuration
	for _, id := range g.Leaves {
		if ts, ok := result.Tasks[id]; ok {
			ts.LF = totalDuration
			ts.LS = totalDuration - durations[id]
		}
	}

	// Process in reverse topological order
	for i := len(order) - 1; i >= 0; i-- {
		id := order[i]
		ts := result.Tasks[id]

		// If LF not set yet (non-leaf), compute from successors
		if ts.LF == 0 && len(g.Adj[id]) > 0 {
			minLS := totalDuration
			for _, succ := range g.Adj[id] {
				succTS := result.Tasks[succ]
				if succTS.LS < minLS {
					minLS = succTS.LS
				}
			}
			ts.LF = minLS
			ts.LS = minLS - durations[id]
		} else if ts.LF == 0 {
			// Leaf that wasn't in g.Leaves (shouldn't happen but defensive)
			ts.LF = totalDuration
			ts.LS = totalDuration - durations[id]
		}

		ts.Slack = ts.LS - ts.ES
		ts.IsCritical = ts.Slack == 0
	}

	// Build critical path (critical tasks in topological order)
	for _, id := range order {
		if result.Tasks[id].IsCritical {
			result.CriticalPath = append(result.CriticalPath, id)
		}
	}

	// Compute waves: group tasks by earliest start time
	result.Waves = computeWaves(result, g)

	return result, nil
}

// topoSort performs Kahn's algorithm for topological sorting.
func topoSort(g *graph.TaskGraph) ([]string, error) {
	inDegree := make(map[string]int)
	for id := range g.Tasks {
		inDegree[id] = len(g.RevAdj[id])
	}

	// Start with roots (in-degree 0), sorted for determinism
	var queue []string
	for id := range g.Tasks {
		if inDegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)

	var order []string
	for len(queue) > 0 {
		// Pop front
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)

		// Reduce in-degree of successors
		var newReady []string
		for _, succ := range g.Adj[node] {
			inDegree[succ]--
			if inDegree[succ] == 0 {
				newReady = append(newReady, succ)
			}
		}
		sort.Strings(newReady)
		queue = append(queue, newReady...)
	}

	if len(order) != len(g.Tasks) {
		return nil, fmt.Errorf("topological sort failed: graph has a cycle (%d of %d tasks sorted)", len(order), len(g.Tasks))
	}

	return order, nil
}

// computeWaves groups tasks by their earliest start time.
func computeWaves(result *CPMResult, g *graph.TaskGraph) []Wave {
	// Group tasks by ES
	esGroups := make(map[int][]string)
	for _, id := range result.TopoOrder {
		es := result.Tasks[id].ES
		esGroups[es] = append(esGroups[es], id)
	}

	// Sort ES values
	esValues := make([]int, 0, len(esGroups))
	for es := range esGroups {
		esValues = append(esValues, es)
	}
	sort.Ints(esValues)

	waves := make([]Wave, len(esValues))
	for i, es := range esValues {
		taskIDs := esGroups[es]
		sort.Strings(taskIDs)

		hasCritical := false
		for _, id := range taskIDs {
			result.Tasks[id].Wave = i
			if result.Tasks[id].IsCritical {
				hasCritical = true
			}
		}

		// Sort critical tasks first within wave
		sort.SliceStable(taskIDs, func(a, b int) bool {
			aCrit := result.Tasks[taskIDs[a]].IsCritical
			bCrit := result.Tasks[taskIDs[b]].IsCritical
			if aCrit != bCrit {
				return aCrit
			}
			return false
		})

		waves[i] = Wave{
			Index:      i,
			TaskIDs:    taskIDs,
			IsCritical: hasCritical,
		}
	}

	return waves
}
