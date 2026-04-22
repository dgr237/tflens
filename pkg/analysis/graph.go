package analysis

import (
	"fmt"
	"sort"
)

// Cycles returns all cycles in the dependency graph.
// Each cycle is reported as a path of entity IDs where the first and last
// element are the same: ["a", "b", "c", "a"].
// Returns nil if the graph is acyclic.
func (m *Module) Cycles() [][]string {
	const (
		unvisited = 0
		inStack   = 1
		done      = 2
	)
	state := make(map[string]int, len(m.entities))
	stack := make([]string, 0, len(m.entities))
	var cycles [][]string

	var dfs func(id string)
	dfs = func(id string) {
		state[id] = inStack
		stack = append(stack, id)

		for _, dep := range m.Dependencies(id) {
			switch state[dep] {
			case inStack:
				// Back edge — extract the cycle from the current stack.
				start := 0
				for i, s := range stack {
					if s == dep {
						start = i
						break
					}
				}
				cycle := make([]string, len(stack)-start+1)
				copy(cycle, stack[start:])
				cycle[len(cycle)-1] = dep // close the loop
				cycles = append(cycles, cycle)
			case unvisited:
				dfs(dep)
			}
		}

		stack = stack[:len(stack)-1]
		state[id] = done
	}

	// Visit in deterministic order.
	ids := make([]string, 0, len(m.entities))
	for _, e := range m.entities {
		ids = append(ids, e.ID())
	}
	sort.Strings(ids)
	for _, id := range ids {
		if state[id] == unvisited {
			dfs(id)
		}
	}
	return cycles
}

// TopoSort returns entities in dependency order: every entity's dependencies
// appear before the entity itself (i.e. safe evaluation order).
// Returns an error if the graph contains cycles — call Cycles() for details.
func (m *Module) TopoSort() ([]Entity, error) {
	// Build in-degree map and reverse-adjacency list in one pass.
	inDeg := make(map[string]int, len(m.entities))
	// revDeps[dep] = list of entities that depend on dep
	revDeps := make(map[string][]string, len(m.entities))

	for _, e := range m.entities {
		id := e.ID()
		if _, ok := inDeg[id]; !ok {
			inDeg[id] = 0
		}
		for _, dep := range m.Dependencies(id) {
			inDeg[id]++
			revDeps[dep] = append(revDeps[dep], id)
		}
	}

	// Seed the queue with all zero-in-degree nodes, sorted for determinism.
	queue := make([]string, 0, len(m.entities))
	for id, deg := range inDeg {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)

	result := make([]Entity, 0, len(m.entities))
	for len(queue) > 0 {
		// Pop front (queue is kept sorted so we always pick the lexically smallest).
		id := queue[0]
		queue = queue[1:]

		if e, ok := m.byID[id]; ok {
			result = append(result, e)
		}

		// Reduce in-degree for every entity that depends on id.
		dependents := revDeps[id]
		sort.Strings(dependents) // keep output deterministic
		for _, dependent := range dependents {
			inDeg[dependent]--
			if inDeg[dependent] == 0 {
				// Insert in sorted position to maintain determinism.
				pos := sort.SearchStrings(queue, dependent)
				queue = append(queue, "")
				copy(queue[pos+1:], queue[pos:])
				queue[pos] = dependent
			}
		}
	}

	if len(result) != len(m.entities) {
		return nil, fmt.Errorf("dependency graph has cycles; %d of %d entities sorted",
			len(result), len(m.entities))
	}
	return result, nil
}

// Impact returns the IDs of every entity that would be affected if id changed —
// i.e. all entities that transitively depend on id, in topological order
// (closest dependents first where possible).
// The id itself is not included in the result.
// Returns nil if nothing depends on id.
func (m *Module) Impact(id string) []string {
	// BFS over the reverse-dependency graph starting from id's direct dependents.
	visited := make(map[string]bool)
	queue := m.Dependents(id)
	for _, d := range queue {
		visited[d] = true
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, d := range m.Dependents(current) {
			if !visited[d] {
				visited[d] = true
				queue = append(queue, d)
			}
		}
	}

	if len(visited) == 0 {
		return nil
	}

	// Return in topological order when possible, otherwise alphabetical.
	sorted, err := m.TopoSort()
	if err != nil {
		// Graph has cycles — fall back to alphabetical.
		out := make([]string, 0, len(visited))
		for id := range visited {
			out = append(out, id)
		}
		sort.Strings(out)
		return out
	}
	out := make([]string, 0, len(visited))
	for _, e := range sorted {
		if visited[e.ID()] {
			out = append(out, e.ID())
		}
	}
	return out
}

// Unreferenced returns entities that are declared but never referenced by any
// other entity in the same file.
//
// Variables are the most actionable case — an unreferenced variable is likely
// dead configuration. Locals that nothing reads are also flagged. Data sources
// and modules that nothing depends on are included too.
// Outputs are excluded: by design nothing in the same file depends on them.
func (m *Module) Unreferenced() []Entity {
	var out []Entity
	for _, e := range m.entities {
		if e.Kind == KindOutput {
			continue
		}
		if len(m.Dependents(e.ID())) == 0 {
			out = append(out, e)
		}
	}
	return out
}
