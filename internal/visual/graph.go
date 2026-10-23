package visual

import (
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/migration"
)

// EdgeKind is why one migration must come after another.
type EdgeKind string

// The two reasons a migration waits on another.
const (
	// EdgeDependency is a dependency the later migration declares, which orders one run.
	EdgeDependency EdgeKind = "dependency"
	// EdgePhase is a step of a change that runs only under a later release than the step before it.
	EdgePhase EdgeKind = "phase"
)

// Node is one migration as the graph draws it, with what it declares about itself.
type Node struct {
	// Migration is the file.
	Migration migration.Migration `json:"-"`
	// Version is the migration's version, which is what an edge names.
	Version string `json:"version"`
	// Filename is the file the migration is stored under.
	Filename string `json:"filename"`
	// Phase is the step of a change it declares itself, nil for none.
	Phase *migration.Phase `json:"phase,omitempty"`
	// Online is how it declares it runs against a live table, empty for a plain change.
	Online migration.Strategy `json:"online,omitempty"`
}

// Edge is one migration that must come before another, and why.
type Edge struct {
	// From is the version that comes first.
	From string `json:"from"`
	// To is the version that waits on it.
	To string `json:"to"`
	// Kind is why it waits.
	Kind EdgeKind `json:"kind"`
}

// Graph is every migration in run order and every edge between them.
type Graph struct {
	// Nodes are the migrations in the order a run uses.
	Nodes []Node `json:"migrations"`
	// Edges are dependencies first and then phases, each in the order of the migration that waits.
	Edges []Edge `json:"edges"`
}

// phaseLabel is what a diagram writes on a phase edge, the step's own name being in its box already.
const phaseLabel = "next deploy"

// DAG draws migrations in run order with every dependency they declare and every phase gate between them.
func DAG(ordered []migration.Migration, d migration.Declarations, gates map[string]migration.Gate) Graph {
	g := Graph{Nodes: make([]Node, 0, len(ordered)), Edges: []Edge{}}

	for _, m := range ordered {
		g.Nodes = append(g.Nodes, nodeOf(m, d))
		g.Edges = append(g.Edges, dependenciesOf(m, d.Dependencies)...)
	}

	for _, m := range ordered {
		if gate, gated := gates[m.Version]; gated {
			g.Edges = append(g.Edges, Edge{From: gate.Prior.Version, To: m.Version, Kind: EdgePhase})
		}
	}

	return g
}

// nodeOf records one migration with the phase and the online strategy it declares.
func nodeOf(m migration.Migration, d migration.Declarations) Node {
	n := Node{Migration: m, Version: m.Version, Filename: m.Filename(), Online: d.Online[m.Version]}

	if phase, declared := d.Phases[m.Version]; declared {
		n.Phase = &phase
	}

	return n
}

// dependenciesOf returns one migration's declared dependencies as edges, oldest version first.
func dependenciesOf(m migration.Migration, declared migration.Declared) []Edge {
	deps := slices.Sorted(slices.Values(declared[m.Version]))
	edges := make([]Edge, 0, len(deps))

	for _, dep := range deps {
		edges = append(edges, Edge{From: dep, To: m.Version, Kind: EdgeDependency})
	}

	return edges
}

// filenameOf names a version by its file, which every rendering prints rather than a bare timestamp.
func (g Graph) filenameOf(version string) string {
	for _, n := range g.Nodes {
		if n.Version == version {
			return n.Filename
		}
	}

	return version
}

// waitsOn returns the edges into one migration, which is what the text rendering lists under it.
func (g Graph) waitsOn(version string) []Edge {
	var into []Edge

	for _, e := range g.Edges {
		if e.To == version {
			into = append(into, e)
		}
	}

	return into
}

// Text renders the graph as the run order, each migration followed by what it waits on.
func (g Graph) Text() string {
	if len(g.Nodes) == 0 {
		return "No migrations.\n"
	}

	var out strings.Builder

	fmt.Fprintf(&out, "%d %s in run order:\n\n", len(g.Nodes), plural(len(g.Nodes), "migration"))

	width := len(fmt.Sprint(len(g.Nodes)))

	for i, n := range g.Nodes {
		fmt.Fprintf(&out, "  %*d  %s\n", width, i+1, n.Filename)

		for _, line := range g.detailOf(n) {
			fmt.Fprintf(&out, "  %*s     %s\n", width, "", line)
		}
	}

	return out.String()
}

// detailOf lists what one migration declares and waits on, one line each.
func (g Graph) detailOf(n Node) []string {
	var lines []string

	for _, e := range g.waitsOn(n.Version) {
		if e.Kind == EdgeDependency {
			lines = append(lines, "depends on  "+g.filenameOf(e.From))
		}
	}

	if n.Phase != nil {
		line := "step        " + n.Phase.Change + ": " + n.Phase.Name

		for _, e := range g.waitsOn(n.Version) {
			if e.Kind == EdgePhase {
				line += ", a deploy after " + g.filenameOf(e.From)
			}
		}

		lines = append(lines, line)
	}

	if n.Online != "" {
		lines = append(lines, "online      "+string(n.Online))
	}

	return lines
}

// Mermaid renders the graph as a Mermaid flowchart, top to bottom in run order.
func (g Graph) Mermaid() string {
	var out strings.Builder

	out.WriteString("flowchart TD\n")

	for _, n := range g.Nodes {
		fmt.Fprintf(&out, "  %s[\"%s\"]\n", mermaidID(n.Version), mermaidText(nodeLabel(n, "<br/>")))
	}

	for i := 1; i < len(g.Nodes); i++ {
		fmt.Fprintf(&out, "  %s ~~~ %s\n", mermaidID(g.Nodes[i-1].Version), mermaidID(g.Nodes[i].Version))
	}

	for _, e := range g.Edges {
		switch e.Kind {
		case EdgePhase:
			fmt.Fprintf(&out, "  %s -.->|%s| %s\n", mermaidID(e.From), phaseLabel, mermaidID(e.To))
		default:
			fmt.Fprintf(&out, "  %s --> %s\n", mermaidID(e.From), mermaidID(e.To))
		}
	}

	return out.String()
}

// DOT renders the graph as a Graphviz digraph, top to bottom in run order.
func (g Graph) DOT() string {
	var out strings.Builder

	out.WriteString("digraph migrations {\n  rankdir=TB;\n  node [shape=box];\n")

	for _, n := range g.Nodes {
		fmt.Fprintf(&out, "  %s [label=%s];\n", dotID(n.Version), dotID(nodeLabel(n, "\n")))
	}

	for i := 1; i < len(g.Nodes); i++ {
		fmt.Fprintf(&out, "  %s -> %s [style=invis];\n", dotID(g.Nodes[i-1].Version), dotID(g.Nodes[i].Version))
	}

	for _, e := range g.Edges {
		switch e.Kind {
		case EdgePhase:
			fmt.Fprintf(&out, "  %s -> %s [style=dashed, label=%s];\n", dotID(e.From), dotID(e.To),
				dotID(phaseLabel))
		default:
			fmt.Fprintf(&out, "  %s -> %s;\n", dotID(e.From), dotID(e.To))
		}
	}

	out.WriteString("}\n")

	return out.String()
}

// nodeLabel is what a diagram writes in a migration's box: its file, then its step and its strategy.
func nodeLabel(n Node, br string) string {
	label := n.Filename

	if n.Phase != nil {
		label += br + "step " + n.Phase.Change + ": " + n.Phase.Name
	}

	if n.Online != "" {
		label += br + string(n.Online)
	}

	return label
}

// mermaidID names a migration's node, a version alone being a word Mermaid reads as an identifier.
func mermaidID(version string) string {
	return "m" + version
}

// mermaidText escapes a label for a quoted Mermaid string, which ends at the first quote.
func mermaidText(text string) string {
	return strings.ReplaceAll(text, `"`, "#quot;")
}

// dotID quotes a Graphviz identifier, escaping what would end it and keeping a newline a line break.
func dotID(text string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(text)

	return `"` + escaped + `"`
}

// plural returns a noun for a count, which every summary line reads.
func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}

	return noun + "s"
}
