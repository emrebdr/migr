package driver

import (
	"context"
	"fmt"
	"testing"

	"github.com/emrebdr/migr/internal/diff"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// M11-T3's done-when is that a diff emits a correct, ordered operation list, and nothing can fold IR
// onto a snapshot -- so the only thing that can judge "correct" is an engine. This suite introspects
// the schema the snapshot fixture builds, mutates it, asks the differ to close the gap, runs what it
// emits, and reads the result back: a wrong order fails at the server rather than at a hand-written
// expectation, which is what makes "create a table before its foreign keys" a measurement.
//
// Both sides of every comparison come from one engine, so nothing is normalized here. The mutation
// copies each new column's type from a column already there, which is what makes that possible --
// native spellings and certainties included, where a hand-written expectation could predict neither.
//
// A view and a check are deliberately absent, for the reason the snapshot fixture leaves them out:
// both are engine text (ADR 0065), so no target this suite could write down would read back as
// itself. internal/diff's own tests carry them.

// Diff holds one driver's engine to the operations this differ emits between two of its snapshots.
type Diff struct {
	// Driver is the driver under test, which is what compiles what the differ emits.
	Driver driver.Driver
	// Conn is an open session against a namespace this suite's prefix owns.
	Conn driver.Conn
	// Prefix makes the fixture's names unique on a server several runs share, and is required.
	Prefix string
}

// Run applies the fixture, diffs it against a mutation of itself, and runs the result both ways.
func (s Diff) Run(t *testing.T) {
	t.Helper()

	base := s.base()

	for _, problem := range base.checkSuite() {
		t.Error(problem)
	}

	if t.Failed() {
		return
	}

	s.clear(t.Context())

	t.Cleanup(func() { s.clear(context.WithoutCancel(t.Context())) })

	if err := base.apply(t.Context(), t); err != nil {
		t.Fatalf("applying the fixture: %v", err)
	}

	before := s.read(t)
	after := s.mutate(t, before)

	s.checkForward(t, before, after)
	s.checkBackward(t, before, after)
	s.checkSelf(t, before)
	s.checkRenamed(t)
}

// base is the snapshot suite this one borrows its fixture, its prefix scoping and its runner from.
func (s Diff) base() Snapshot {
	return Snapshot(s)
}

// checkForward asks the differ to reach the mutated schema, runs it, and reads back what it reached.
func (s Diff) checkForward(t *testing.T, before, after schema.Snapshot) {
	t.Helper()

	ops := s.proposed(t, "forward", before, after, diff.Renames{})
	if len(ops) == 0 {
		t.Fatal("the differ proposed nothing between two schemas that differ")
	}

	s.perform(t, "the forward diff", ops)
	s.compare(t, "the forward diff", after)
}

// checkBackward asks the differ for the way back, which is where the drops carry what they replace.
func (s Diff) checkBackward(t *testing.T, before, after schema.Snapshot) {
	t.Helper()

	ops := s.proposed(t, "backward", after, before, diff.Renames{})

	s.perform(t, "the backward diff", ops)
	s.compare(t, "the backward diff", before)
}

// checkSelf asserts the one claim M11-T6 rests on: a schema diffed against itself proposes nothing.
func (s Diff) checkSelf(t *testing.T, before schema.Snapshot) {
	t.Helper()

	ops, refused := diff.Diff(before, before, diff.Renames{})

	for _, one := range ops {
		t.Errorf("diffing this schema against itself proposed %s", one.Kind())
	}

	for _, one := range refused {
		t.Errorf("diffing this schema against itself refused %s", one.Describe())
	}
}

// A rename is the one thing in this differ that no amount of comparing two snapshots can settle, so
// the leg that proves it has to be the engine too: a schema differing from another by nothing but
// names is the one case where a confirmed rename emits two operations and an unconfirmed one emits
// four, two of which destroy the data. Both halves are asserted, and then the derived down is run --
// ir.ReverseAll of the forward list, which is what internal/engine hands a rollback when the
// migration migr diff wrote authored none. That is the claim the emission order's asymmetry is for.

// checkRenamed asks the differ to close a gap that is nothing but two renames, and runs both ways.
func (s Diff) checkRenamed(t *testing.T) {
	s.clear(context.WithoutCancel(t.Context()))

	if err := s.base().apply(t.Context(), t); err != nil {
		t.Fatalf("applying the fixture again: %v", err)
	}

	before := s.read(t)
	after := s.renamed(t, before)
	confirmed := s.confirmable(t, before, after)

	ops := s.proposed(t, "rename", before, after, confirmed)
	s.checkRenamesLead(t, ops)

	s.perform(t, "the confirmed renames", ops)
	s.compare(t, "the confirmed renames", after)

	undo, err := ir.ReverseAll(ops)
	if err != nil {
		t.Fatalf("deriving the inverse of a generated rename: %v", err)
	}

	s.perform(t, "the derived inverse of the renames", undo)
	s.compare(t, "the derived inverse of the renames", before)
}

// checkRenamesLead holds a confirmed rename to destroying nothing, and to preceding what needs it.
func (s Diff) checkRenamesLead(t *testing.T, ops ir.Operations) {
	t.Helper()

	for i, op := range ops {
		if op.Destructive() {
			t.Errorf("operation %d is a %s, and a confirmed rename is what keeps the data: %v",
				i+1, op.Kind(), kindsOf(ops))
		}
	}

	for i, want := range []ir.Kind{ir.KindRenameTable, ir.KindRenameColumn} {
		if i >= len(ops) {
			t.Fatalf("closing a gap of two renames and two dependents took %v", kindsOf(ops))
		}

		if got := ops[i].Kind(); got != want {
			t.Fatalf("operation %d is a %s, want %s -- a rename establishes the name every "+
				"operation after it speaks: %v", i+1, got, want, kindsOf(ops))
		}
	}

	if len(ops) < 4 {
		t.Fatalf("the two operations that have to follow the renames are missing: %v", kindsOf(ops))
	}
}

// renameRounds bounds the rounds this suite confirms in, a column rename inside a renamed table
// being detectable only once the table's own rename is confirmed. internal/cli drives the real loop.
const renameRounds = 3

// confirmable holds the heuristic to proposing both renames, and returns them as confirmed.
func (s Diff) confirmable(t *testing.T, before, after schema.Snapshot) diff.Renames {
	t.Helper()

	var confirmed diff.Renames

	for range renameRounds {
		found := diff.Candidates(before, after, confirmed)
		if len(found) == 0 {
			break
		}

		for _, one := range found {
			confirmed = confirmed.With(one)
		}
	}

	if len(confirmed.Tables) != 1 || len(confirmed.Columns) != 1 {
		t.Fatalf("the heuristic proposed %d table and %d column renames over a gap that is one of "+
			"each: %v", len(confirmed.Tables), len(confirmed.Columns),
			describeAll(diff.Candidates(before, after, diff.Renames{})))
	}

	return confirmed
}

// describeAll renders every candidate for a failure, since the names are the whole of the question.
func describeAll(found []diff.Candidate) []string {
	lines := make([]string, 0, len(found))
	for _, one := range found {
		lines = append(lines, one.Describe())
	}

	return lines
}

// kindsOf names a list's operations in order, which is what an ordering failure has to print.
func kindsOf(ops ir.Operations) []ir.Kind {
	kinds := make([]ir.Kind, 0, len(ops))
	for _, op := range ops {
		kinds = append(kinds, op.Kind())
	}

	return kinds
}

// proposed returns what the differ emits one way, failing on anything it would not propose.
func (s Diff) proposed(t *testing.T, way string, from, to schema.Snapshot,
	confirmed diff.Renames,
) ir.Operations {
	t.Helper()

	ops, refused := diff.Diff(from, to, confirmed)

	for _, one := range refused {
		t.Errorf("the %s diff of this engine's own schema refused %s", way, one.Describe())
	}

	if t.Failed() {
		t.FailNow()
	}

	return ops
}

// perform runs what the differ emitted, naming the operation a driver could not carry out.
func (s Diff) perform(t *testing.T, way string, ops ir.Operations) {
	t.Helper()

	if err := s.base().run(t.Context(), ops); err != nil {
		t.Fatalf("running %s: %v", way, err)
	}
}

// compare reads the schema back and reports every way it is not the one the diff was asked for.
func (s Diff) compare(t *testing.T, way string, want schema.Snapshot) {
	t.Helper()

	if problem := s.difference(t, way, want, s.read(t)); problem != "" {
		t.Error(problem)
	}
}

// difference says how what the engine holds is not what the diff was asked to reach, or nothing.
func (s Diff) difference(t *testing.T, way string, want, got schema.Snapshot) string {
	t.Helper()

	if got.Equal(want) {
		return ""
	}

	return fmt.Sprintf("%s did not reach the schema it was asked for\n\nwant:\n%s\n\ngot:\n%s",
		way, render(t, want), render(t, got))
}

// read introspects the session and keeps the part of it this suite's prefix owns.
func (s Diff) read(t *testing.T) schema.Snapshot {
	t.Helper()

	snapshot, err := s.Conn.Introspect(t.Context())
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}

	if err := snapshot.Validate(); err != nil {
		t.Fatalf("this driver read a snapshot that is not usable: %v", err)
	}

	return s.base().own(snapshot)
}

// clear drops what every leg of this suite leaves behind, the referencing tables before the keyed one.
func (s Diff) clear(ctx context.Context) {
	for _, table := range []string{s.extra(), s.base().child(), s.moved()} {
		stmts, err := s.Driver.Compile(ir.DropTable{Table: table})
		if err != nil {
			continue
		}

		for _, stmt := range stmts {
			_ = s.Conn.Exec(ctx, stmt)
		}
	}

	s.base().clear(ctx)
}
