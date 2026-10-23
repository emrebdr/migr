package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
)

// What a deferred operation costs and buys, with no database and no driver that emulates anything.
//
// The claim this file holds is ordering: an operation compiled against the database is compiled
// immediately before it runs, inside the transaction its migration is, so it reads the statements
// the operations ahead of it have already sent. The session records the expansion in the same list
// it records begin, exec and commit in, so the order is asserted rather than inferred.

// retype is the operation the fixture compiler defers, since this package has no emulating driver.
func retype(column string) ir.AlterColumn {
	return ir.AlterColumn{Table: stagedTable, Column: column, Nullable: pointerTo(false),
		From: &ir.ColumnState{Nullable: pointerTo(true)}}
}

// pointerTo returns a pointer to a value, for the operation fields that are tri-state.
func pointerTo[T any](v T) *T {
	return &v
}

// deferring compiles what the staged compiler does, and sends every alter_column to the session.
type deferring struct{}

// Compile defers one kind to the session and renders the rest the way every other fixture does.
func (deferring) Compile(op ir.Operation) ([]driver.Statement, error) {
	if op.Kind() == ir.KindAlterColumn {
		return nil, driver.NeedsSession("recorder", op.Kind(), "it reads the schema first")
	}

	return compiler{}.Compile(op)
}

// expanding is a session that records being asked and renders a statement naming the column.
type expanding struct {
	conn       *recorder
	standalone bool
	err        error
}

// Expand notes the ask where the session notes everything else, so the ordering is one list.
func (e *expanding) Expand(_ context.Context, op ir.Operation) ([]driver.Statement, error) {
	altered, ok := op.(ir.AlterColumn)
	if !ok {
		return nil, errors.New("the fixture session was given " + op.Kind().String())
	}

	e.conn.note("expand " + altered.Column)

	if e.err != nil {
		return nil, e.err
	}

	return []driver.Statement{{SQL: "~" + altered.Column, NoTransaction: e.standalone}}, nil
}

// emulating stages one migration whose operations a test chooses, over a session that expands them.
func emulating(t *testing.T, conn *recorder, ops ir.Operations) (Options, *expanding) {
	t.Helper()

	session := &expanding{conn: conn}

	opts, src := staged(t, "a")
	opts.Planner = plan.Planner{Compiler: deferring{}, Expander: session}
	src.ops[versionAt(0)] = ops

	conn.store.table = true

	return opts, session
}

func TestEachEmulatedOperationIsCompiledImmediatelyBeforeItRuns(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := emulating(t, conn, ir.Operations{
		addIndex("a0"), retype("tag"), addIndex("b2"), retype("note"),
	})

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{
		"lock", "ensure", "list", "latest",
		"introspect",
		"begin",
		"exec a0",
		"expand tag", "exec ~tag",
		"exec b2",
		"expand note", "exec ~note",
		"record " + versionAt(0), "commit",
		"unlock",
	}
	if !slices.Equal(conn.calls, want) {
		t.Fatalf("the session was asked for\n  %v\nwant\n  %v", conn.calls, want)
	}

	if result.Runs[0].Statements != 4 {
		t.Errorf("the run reports %d statements, want the four it sent", result.Runs[0].Statements)
	}

	t.Log("Every expansion sits between the statement before it and the statement after it, and")
	t.Log("all of them sit inside one begin and one commit. That second half is what makes this")
	t.Log("correct on an engine with transactional DDL: the catalog an expansion reads is the one")
	t.Log("the uncommitted statements ahead of it left, which both real expanders read through.")
}

func TestAPreviewCompilesTheFirstEmulatedOperationAndDefersTheRest(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := emulating(t, conn, ir.Operations{retype("tag"), retype("note")})

	previewed, err := Preview(t.Context(), conn, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	steps := previewed.Steps[0].Plan.Steps

	if steps[0].Deferred || len(steps[0].Statements) != 1 {
		t.Errorf("the first operation was not compiled: %+v", steps[0])
	}

	if !steps[1].Deferred || len(steps[1].Statements) != 0 {
		t.Errorf("the second operation was compiled from a catalog it will not run against: %+v",
			steps[1])
	}

	if slices.Contains(conn.calls, "exec ~tag") {
		t.Errorf("a preview executed something: %v", conn.calls)
	}

	t.Log("A preview applies nothing, so only the first operation of the first migration reads the")
	t.Log("catalog it will actually run against. Printing what the rest compile to now would print")
	t.Log("SQL that is not the SQL that runs, which is the rule migr plan already follows for a")
	t.Log("bound argument it counts rather than splices.")
}

func TestAMigrationOfNothingButEmulatedOperationsIsNotReadAsCompilingToNothing(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := emulating(t, conn, ir.Operations{retype("tag")})

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply returned %v, want the migration to run", err)
	}

	if !slices.Contains(conn.calls, "exec ~tag") {
		t.Errorf("the session was asked for %v, want the expansion to have run", conn.calls)
	}

	t.Log("ErrNothingToRun reads a statement list, and a plan holding only deferred operations has")
	t.Log("none yet. Refusing it would make every SQLite migration of one rebuild unrunnable.")
}

func TestAnExpansionThatCannotShareTheTransactionIsRefusedAndTakesNothingWithIt(t *testing.T) {
	conn := newRecorder(t, true)
	opts, session := emulating(t, conn, ir.Operations{addIndex("a0"), retype("tag")})
	session.standalone = true

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrNotAtomic) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrNotAtomic", err)
	}

	var failed *plan.CompileError
	if !errors.As(err, &failed) || failed.Kind != ir.KindAlterColumn {
		t.Errorf("the refusal does not name the operation: %v", err)
	}

	if !slices.Contains(conn.calls, "rollback") {
		t.Errorf("the session was asked for %v, want the transaction discarded", conn.calls)
	}

	t.Log("The known statements are grouped before the lock, as they always were, and an expansion")
	t.Log("is held to the same rule as it arrives -- which is the one moment it can be, since its")
	t.Log("statements do not exist until then. Inside the transaction the refusal is a rollback.")
}

func TestAFailedExpansionIsTheMigrationsFailure(t *testing.T) {
	conn := newRecorder(t, true)
	opts, session := emulating(t, conn, ir.Operations{addIndex("a0"), retype("tag")})
	session.err = errors.New("no such table")

	_, err := Apply(t.Context(), conn, opts)
	if err == nil {
		t.Fatal("Apply reported success for an operation nothing could compile")
	}

	var failed *MigrationError
	if !errors.As(err, &failed) || failed.Migration.Version != versionAt(0) {
		t.Fatalf("the failure does not name the migration: %v", err)
	}

	if rec := onlyRun(t, conn); rec.LastStatement != 0 {
		t.Errorf("the row names statement %d, want none: the transaction took the first back",
			rec.LastStatement)
	}
}

// stoppedWithOneUncompiled stages a run that fails before an emulated operation it never compiles.
func stoppedWithOneUncompiled(t *testing.T) (*recorder, Options) {
	t.Helper()

	conn := stopping(t, false, "b1")
	opts, _ := emulating(t, conn, ir.Operations{addIndex("a0"), addIndex("b1"), retype("tag")})

	return conn, opts
}

func TestADirtyRunSaysHowManyOperationsItNeverCompiled(t *testing.T) {
	conn, opts := stoppedWithOneUncompiled(t)

	_, err := Apply(t.Context(), conn, opts)

	var became *DirtyError
	if !errors.As(err, &became) {
		t.Fatalf("Apply returned %T, want the run that just became dirty", err)
	}

	if became.Deferred != 1 || became.Statements != 2 {
		t.Fatalf("the failure reports %d statements and %d uncompiled operations, want 2 and 1",
			became.Statements, became.Deferred)
	}

	for _, want := range []string{"the 2 it had compiled", "compiles 1 of this migration's"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q:\n%v", want, err)
		}
	}

	t.Log("The row holds how far the run got, which is unchanged. What a run can no longer say is")
	t.Log("how far it had to go, so the total is what it had compiled and the message says so --")
	t.Log("because the operations it never reached are rendered from a catalog it never read.")
}

func TestAResumeIsRefusedWhereAnOperationIsCompiledAsItRuns(t *testing.T) {
	conn, opts := stoppedWithOneUncompiled(t)

	if _, err := Apply(t.Context(), conn, opts); !errors.Is(err, ErrDirty) {
		t.Fatalf("the run that was supposed to stop partway returned %v", err)
	}

	conn.calls = nil
	conn.fail = ""

	_, err := Resolve(t.Context(), conn, opts, Repair{Mode: RepairResume})
	if !errors.Is(err, ErrNotResumable) {
		t.Fatalf("Resolve --resume returned %v, want %v", err, ErrNotResumable)
	}

	for _, want := range []string{"--applied", "--rolled-back"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name the repair %q:\n%v", want, err)
		}
	}

	if slices.Contains(conn.calls, "exec b1") {
		t.Errorf("the refused resume ran %v", conn.calls)
	}

	t.Log("The resume refusal's own argument, one layer further in: the recorded index names a statement in")
	t.Log("the list that run emitted, and an expansion's statements are a function of a schema")
	t.Log("this run has already changed -- so re-rendering them here numbers a different list.")
	t.Log("The two repairs that do not compile anything are what the refusal sends an operator to.")
}

func TestAPreviewCompilesNothingPastTheFirstMigration(t *testing.T) {
	conn := newRecorder(t, true)
	session := &expanding{conn: conn}

	opts, src := staged(t, "a", "b")
	opts.Planner = plan.Planner{Compiler: deferring{}, Expander: session}
	src.ops[versionAt(0)] = ir.Operations{retype("tag")}
	src.ops[versionAt(1)] = ir.Operations{retype("note")}
	conn.store.table = true

	previewed, err := Preview(t.Context(), conn, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if previewed.Steps[0].Plan.Deferred() != 0 {
		t.Error("the first migration's own first operation was not compiled")
	}

	if previewed.Steps[1].Plan.Deferred() != 1 {
		t.Error("the second migration's first operation was compiled, and it cannot be")
		t.Log("The schema it would run against is the one the migration before it leaves, which")
		t.Log("is judgedFirst's reason for judging only the first migration.")
	}
}

func TestAPreviewCompilesNothingForAMigrationThatWritesFirst(t *testing.T) {
	conn := newRecorder(t, true)
	session := &expanding{conn: conn}

	opts, src := staged(t, "a")
	opts.Planner = plan.Planner{Compiler: deferring{}, Expander: session}
	src.ops[versionAt(0)] = ir.Operations{retype("tag")}
	src.direct[versionAt(0)] = []driver.Query{{SQL: "UPDATE staged SET tag = 'x'"}}
	conn.store.table = true

	previewed, err := Preview(t.Context(), conn, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if previewed.Steps[0].Plan.Deferred() != 1 {
		t.Error("an operation was compiled behind a statement the migration sends itself")
		t.Log("The escape hatch runs before the operations and outside their transaction,")
		t.Log("so a migration holding one changes the schema before its first operation")
		t.Log("reads it -- which is the one thing that makes even the first operation inexact.")
	}
}

func TestAPreviewDoesNotFailOverAnExpansionItOnlyAskedForToPrintMore(t *testing.T) {
	conn := newRecorder(t, true)
	opts, session := emulating(t, conn, ir.Operations{retype("tag")})
	session.err = errors.New("no such table")

	previewed, err := Preview(t.Context(), conn, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview returned %v, and a preview refuses nothing", err)
	}

	if previewed.Steps[0].Plan.Deferred() != 1 {
		t.Error("a failed expansion left the step reading as compiled")
	}

	t.Log("judgedFirst drops a failed read for the same reason: a preview must not fail over a")
	t.Log("question it only asked in order to annotate. migr up asks the same question and does")
	t.Log("fail over it, which is where an operator finds out.")
}
