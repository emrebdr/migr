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
}

// base is the snapshot suite this one borrows its fixture, its prefix scoping and its runner from.
func (s Diff) base() Snapshot {
	return Snapshot(s)
}

// checkForward asks the differ to reach the mutated schema, runs it, and reads back what it reached.
func (s Diff) checkForward(t *testing.T, before, after schema.Snapshot) {
	t.Helper()

	ops := s.proposed(t, "forward", before, after)
	if len(ops) == 0 {
		t.Fatal("the differ proposed nothing between two schemas that differ")
	}

	s.perform(t, "the forward diff", ops)
	s.compare(t, "the forward diff", after)
}

// checkBackward asks the differ for the way back, which is where the drops carry what they replace.
func (s Diff) checkBackward(t *testing.T, before, after schema.Snapshot) {
	t.Helper()

	ops := s.proposed(t, "backward", after, before)

	s.perform(t, "the backward diff", ops)
	s.compare(t, "the backward diff", before)
}

// checkSelf asserts the one claim M11-T6 rests on: a schema diffed against itself proposes nothing.
func (s Diff) checkSelf(t *testing.T, before schema.Snapshot) {
	t.Helper()

	ops, refused := diff.Diff(before, before)

	for _, one := range ops {
		t.Errorf("diffing this schema against itself proposed %s", one.Kind())
	}

	for _, one := range refused {
		t.Errorf("diffing this schema against itself refused %s", one.Describe())
	}
}

// proposed returns what the differ emits one way, failing on anything it would not propose.
func (s Diff) proposed(t *testing.T, way string, from, to schema.Snapshot) ir.Operations {
	t.Helper()

	ops, refused := diff.Diff(from, to)

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

// clear drops what both fixtures leave behind, the referencing table first so the key goes with it.
func (s Diff) clear(ctx context.Context) {
	stmts, err := s.Driver.Compile(ir.DropTable{Table: s.extra()})
	if err == nil {
		for _, stmt := range stmts {
			_ = s.Conn.Exec(ctx, stmt)
		}
	}

	s.base().clear(ctx)
}
