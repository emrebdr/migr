package migration

import (
	"slices"
	"strings"
)

// Declared is what each migration says it must follow, keyed and valued by version.
type Declared map[string][]string

// Order returns migrations in one sequence honoring both their timestamps and their dependencies.
func Order(found []Migration, declared Declared) ([]Migration, error) {
	g := newGraph(found, declared)

	ordered, stuck := g.drain()
	if problem := g.report(stuck); problem != nil {
		return nil, problem
	}

	return ordered, nil
}

// graph is one set of declarations over a sorted migration list, addressed by position, not name.
type graph struct {
	// nodes are the migrations, sorted by version, and a node is its index into this.
	nodes []Migration
	// needs holds each node's dependencies as positions, ascending, dangling edges already dropped.
	needs [][]int
	// feeds is the reverse: the nodes waiting on each one, ascending.
	feeds [][]int
	// unknown are the declared edges naming a version no migration here carries.
	unknown []UnknownDependency
}

// newGraph resolves declared versions to positions, setting aside the ones that name nobody.
func newGraph(found []Migration, declared Declared) *graph {
	nodes := slices.Clone(found)
	slices.SortStableFunc(nodes, byVersion)

	at := make(map[string]int, len(nodes))
	for i, m := range nodes {
		at[m.Version] = i
	}

	g := &graph{
		nodes:   nodes,
		needs:   make([][]int, len(nodes)),
		feeds:   make([][]int, len(nodes)),
		unknown: orphanedDeclarations(declared, at),
	}

	for i, m := range nodes {
		g.link(i, declared[m.Version], at)
	}

	return g
}

// link records one migration's dependencies as edges, collecting the ones that name nobody.
func (g *graph) link(node int, deps []string, at map[string]int) {
	for _, version := range sortedUnique(deps) {
		dep, known := at[version]
		if !known {
			g.unknown = append(g.unknown,
				UnknownDependency{From: g.nodes[node].Filename(), Version: version})

			continue
		}

		g.needs[node] = append(g.needs[node], dep)
		g.feeds[dep] = append(g.feeds[dep], node)
	}
}

// orphanedDeclarations reports declarations whose own key names no migration that was discovered.
func orphanedDeclarations(declared Declared, at map[string]int) []UnknownDependency {
	var orphans []UnknownDependency

	for version := range declared {
		if _, known := at[version]; !known {
			orphans = append(orphans, UnknownDependency{Version: version})
		}
	}

	slices.SortFunc(orphans, byDeclaredVersion)

	return orphans
}

// byDeclaredVersion orders orphaned declarations, since a map is what they were collected from.
func byDeclaredVersion(a, b UnknownDependency) int {
	return strings.Compare(a.Version, b.Version)
}

// sortedUnique returns a dependency list in a fixed order, so a caller's own order changes nothing.
func sortedUnique(deps []string) []string {
	ordered := slices.Clone(deps)
	slices.Sort(ordered)

	return slices.Compact(ordered)
}

// drain runs the ready nodes out in version order, returning what came out and what could not.
func (g *graph) drain() ([]Migration, []int) {
	waiting := make([]int, len(g.nodes))

	var ready []int

	for node := range g.nodes {
		waiting[node] = len(g.needs[node])
		if waiting[node] == 0 {
			ready = append(ready, node)
		}
	}

	ordered := make([]Migration, 0, len(g.nodes))
	done := make([]bool, len(g.nodes))

	for len(ready) > 0 {
		node := ready[0]
		ready = ready[1:]
		done[node] = true
		ordered = append(ordered, g.nodes[node])

		for _, waiter := range g.feeds[node] {
			waiting[waiter]--
			if waiting[waiter] == 0 {
				ready = release(ready, waiter)
			}
		}
	}

	return ordered, undone(done)
}

// release makes a node ready, keeping the queue in the version order positions already encode.
func release(ready []int, node int) []int {
	at, _ := slices.BinarySearch(ready, node)

	return slices.Insert(ready, at, node)
}

// undone returns the nodes that never came out, ascending, which is every node in or behind a cycle.
func undone(done []bool) []int {
	var stuck []int

	for node, out := range done {
		if !out {
			stuck = append(stuck, node)
		}
	}

	return stuck
}

// report renders everything wrong with the graph at once, or nothing when it ordered cleanly.
func (g *graph) report(stuck []int) error {
	cycles := g.cycles(stuck)
	if len(g.unknown) == 0 && len(cycles) == 0 {
		return nil
	}

	return &GraphError{Unknown: g.unknown, Cycles: cycles}
}

// walk is one depth-first traversal in progress, looking for the edge that closes a loop.
type walk struct {
	// open marks the nodes on the current path, whose re-entry is what a cycle is.
	open []bool
	// closed marks the nodes already explored, so no node is walked twice.
	closed []bool
	// path is the current chain of nodes, deepest last.
	path []int
	// found are the cycles collected so far, each rendered in filenames.
	found [][]string
}

// cycles returns a path for every edge that closes a loop among the nodes that never ordered.
func (g *graph) cycles(stuck []int) [][]string {
	if len(stuck) == 0 {
		return nil
	}

	w := &walk{
		open:   make([]bool, len(g.nodes)),
		closed: make([]bool, len(g.nodes)),
	}

	for _, node := range stuck {
		if !w.closed[node] {
			g.descend(w, node)
		}
	}

	return w.found
}

// descend follows one node's dependencies, recording the loop when an edge reaches the path again.
func (g *graph) descend(w *walk, node int) {
	w.open[node] = true
	w.path = append(w.path, node)

	for _, dep := range g.needs[node] {
		switch {
		case w.open[dep]:
			g.collect(w, dep)
		case !w.closed[dep]:
			g.descend(w, dep)
		}
	}

	w.path = w.path[:len(w.path)-1]
	w.open[node] = false
	w.closed[node] = true
}

// collect records the loop an edge just closed, as the path from the migration it reached back to.
func (g *graph) collect(w *walk, dep int) {
	loop := rotated(w.path[slices.Index(w.path, dep):])

	named := make([]string, len(loop))
	for i, node := range loop {
		named[i] = g.nodes[node].Filename()
	}

	w.found = append(w.found, named)
}

// rotated turns a loop to start at its earliest migration, so the path reads the same however found.
func rotated(loop []int) []int {
	first := slices.Index(loop, slices.Min(loop))

	turned := make([]int, 0, len(loop))
	turned = append(turned, loop[first:]...)

	return append(turned, loop[:first]...)
}
