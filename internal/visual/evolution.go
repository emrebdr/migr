package visual

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/fold"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/schema"
)

// Read is what one migration's up half emitted, or why it could not be asked.
type Read struct {
	// Migration is the file.
	Migration migration.Migration
	// Operations are what its up half emitted, in order.
	Operations ir.Operations
	// Direct is how many statements it sends itself, which no projection reads.
	Direct int
	// Unread says why its operations could not be read, empty where they were.
	Unread string
}

// Change is one operation as the evolution lists it.
type Change struct {
	// Kind is the operation.
	Kind ir.Kind `json:"kind"`
	// Subject is the object it acts on, named the way the operation names it.
	Subject string `json:"subject"`
}

// Step is one migration's place in the evolution: what it does, and what that changes in the schema.
type Step struct {
	// Migration is the file.
	Migration migration.Migration `json:"-"`
	// Filename is the file the migration is stored under.
	Filename string `json:"filename"`
	// Changes are its operations, in the order it emitted them.
	Changes []Change `json:"operations"`
	// Direct is how many statements it sends itself, which no projection reads.
	Direct int `json:"direct,omitempty"`
	// Unread says why its operations could not be read, empty where they were.
	Unread string `json:"unread,omitempty"`
	// Known reports whether the schema after this step can be read from the migrations.
	Known bool `json:"known"`
	// Removed are the schema lines this step took away, empty once the schema is unknown.
	Removed []string `json:"removed,omitempty"`
	// Added are the schema lines this step brought, empty once the schema is unknown.
	Added []string `json:"added,omitempty"`
	// Ended says why the schema stops being known here, set on the one step that ended it.
	Ended string `json:"ended,omitempty"`
}

// Evolution is every migration's step, and the schema the last of them leaves.
type Evolution struct {
	// Steps are the migrations in run order.
	Steps []Step `json:"migrations"`
	// Known reports whether the schema after the last step can be read from the migrations.
	Known bool `json:"known"`
	// Schema is the schema the last step leaves, empty where it is not known.
	Schema schema.Snapshot `json:"schema"`
	// EndedBy is the step that ended the projection, nil where nothing did.
	EndedBy *Step `json:"-"`
}

// Evolve folds each migration's operations forward from nothing, recording what each one changes.
func Evolve(reads []Read) Evolution {
	e := Evolution{Steps: make([]Step, 0, len(reads)), Known: true}

	var held schema.Snapshot

	for _, r := range reads {
		step := Step{Migration: r.Migration, Filename: r.Migration.Filename(), Changes: changesOf(r.Operations),
			Direct: r.Direct, Unread: r.Unread}

		if e.Known {
			held, step = folded(held, r, step)
			e.Known = step.Known
		}

		e.Steps = append(e.Steps, step)
	}

	for i := range e.Steps {
		if e.Steps[i].Ended != "" {
			e.EndedBy = &e.Steps[i]
		}
	}

	if e.Known {
		e.Schema = held
	}

	return e
}

// folded applies one migration to the schema so far, or records why the schema stops being known.
func folded(held schema.Snapshot, r Read, step Step) (schema.Snapshot, Step) {
	if r.Unread != "" {
		step.Ended = r.Unread

		return held, step
	}

	after, refused := fold.Forward(held, r.Operations)
	if len(refused) > 0 {
		step.Ended = endedBy(refused)

		return held, step
	}

	step.Known = true
	removed, added := schema.Changes(schema.Render(held), schema.Render(after))
	step.Removed, step.Added = tableFirst(removed), tableFirst(added)

	return after, step
}

// endedBy says why a fold's refusals leave the schema unknowable, naming each operation refused.
func endedBy(refused []fold.Refusal) string {
	lines := make([]string, 0, len(refused))

	for _, r := range refused {
		if r.Unfoldable() {
			return fmt.Sprintf("operation %d (raw) is engine SQL, and what it does to the schema cannot be "+
				"read", r.Index)
		}

		lines = append(lines, r.Describe())
	}

	return "the schema cannot carry it: " + strings.Join(lines, "; ")
}

// A rendering sorts its lines whole, so a table's own line sorts after its columns -- "table" after
// "column" -- and a step that creates one would list the table last. Each line is the kind padded to
// a width and then the subject, so the table an element belongs to is the subject up to its first dot
// or space, and grouping on that with the table's own line first reads as the migration did.

// tableFirst orders schema lines by the table they belong to, the table's own line ahead of what it holds.
func tableFirst(lines []string) []string {
	ordered := slices.Clone(lines)

	slices.SortStableFunc(ordered, func(a, b string) int {
		return cmp.Or(cmp.Compare(tableOf(a), tableOf(b)), cmp.Compare(rankOf(a), rankOf(b)),
			cmp.Compare(a, b))
	})

	return ordered
}

// tableOf is the table a rendered line belongs to, which is its subject up to the first dot or space.
func tableOf(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return line
	}

	table, _, _ := strings.Cut(fields[1], ".")

	return table
}

// rankOf puts a table's own line first, everything it holds after it.
func rankOf(line string) int {
	if strings.HasPrefix(line, "table ") {
		return 0
	}

	return 1
}

// changesOf lists operations as their kind and subject.
func changesOf(ops ir.Operations) []Change {
	changes := make([]Change, 0, len(ops))

	for _, op := range ops {
		if op == nil {
			continue
		}

		changes = append(changes, Change{Kind: op.Kind(), Subject: engine.Subject(op)})
	}

	return changes
}

// Text renders the evolution as each migration's operations and what they change, then the schema left.
func (e Evolution) Text() string {
	if len(e.Steps) == 0 {
		return "No migrations.\n"
	}

	var out strings.Builder

	width := len(fmt.Sprint(len(e.Steps)))

	for i, s := range e.Steps {
		fmt.Fprintf(&out, "%*d  %s\n", width, i+1, s.Filename)
		writeStep(&out, strings.Repeat(" ", width+2), s)
		out.WriteString("\n")
	}

	e.writeSchema(&out)

	return out.String()
}

// writeStep writes one migration's operations, then what they change or why that cannot be read.
func writeStep(out *strings.Builder, indent string, s Step) {
	if s.Unread != "" && s.Ended == "" {
		fmt.Fprintf(out, "%soperations not read: %s\n", indent, s.Unread)
	}

	for _, c := range s.Changes {
		fmt.Fprintf(out, "%s%-17s %s\n", indent, c.Kind, c.Subject)
	}

	if s.Direct > 0 {
		fmt.Fprintf(out, "%s! sends %d %s itself, which the schema here does not read\n", indent, s.Direct,
			plural(s.Direct, "statement"))
	}

	switch {
	case s.Ended != "":
		fmt.Fprintf(out, "%s? the schema after this cannot be read from the migrations: %s\n", indent, s.Ended)
	case !s.Known:
		fmt.Fprintf(out, "%s? not projected\n", indent)
	case len(s.Removed) == 0 && len(s.Added) == 0:
		fmt.Fprintf(out, "%s= the schema is unchanged\n", indent)
	}

	for _, line := range s.Removed {
		fmt.Fprintf(out, "%s- %s\n", indent, line)
	}

	for _, line := range s.Added {
		fmt.Fprintf(out, "%s+ %s\n", indent, line)
	}
}

// writeSchema writes the schema the last migration leaves, or names the one past which nothing is known.
func (e Evolution) writeSchema(out *strings.Builder) {
	last := e.Steps[len(e.Steps)-1].Filename

	if !e.Known {
		fmt.Fprintf(out, "The schema after %s cannot be read from the migrations: %s ended the "+
			"projection.\n", last, e.EndedBy.Filename)

		return
	}

	fmt.Fprintf(out, "The schema after %s:\n", last)

	rendered := schema.Render(e.Schema)
	if rendered == "" {
		out.WriteString("  (no tables and no views)\n")

		return
	}

	for _, line := range tableFirst(strings.Split(rendered, "\n")) {
		fmt.Fprintf(out, "  %s\n", line)
	}
}
