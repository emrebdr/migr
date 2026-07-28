package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// errRefused is what the recording session reports for the statement a test told it to fail.
var errRefused = errors.New("the server refused this statement")

// recorder is a session with no database that records what the executor asked it to do.
type recorder struct {
	caps  capability.Set
	calls []string
	fail  string
	inTx  bool
}

// newRecorder builds a session whose declaration says whether its DDL is transactional.
func newRecorder(t *testing.T, transactional bool) *recorder {
	t.Helper()

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(capability.Native(capability.LockFree))
	}

	facts := capability.Facts{Driver: "recorder", Server: "1.0", TransactionalDDL: transactional}

	set, err := capability.NewSet(facts, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	return &recorder{caps: set}
}

// Exec records the statement and where it ran, failing the one a test asked it to fail.
func (r *recorder) Exec(_ context.Context, stmt driver.Statement) error {
	r.calls = append(r.calls, r.describeExec(stmt))

	if stmt.SQL == r.fail {
		return errRefused
	}

	return nil
}

// describeExec records whether the statement ran inside the transaction or outside it.
func (r *recorder) describeExec(stmt driver.Statement) string {
	if r.inTx {
		return "exec " + stmt.SQL
	}

	return "exec-outside-any-transaction " + stmt.SQL
}

// Transact records the boundary and commits only when the work returns nil.
func (r *recorder) Transact(ctx context.Context, work driver.Work) error {
	r.calls = append(r.calls, "begin")
	r.inTx = true

	err := work(ctx)

	r.inTx = false

	if err != nil {
		r.calls = append(r.calls, "rollback")

		return err
	}

	r.calls = append(r.calls, "commit")

	return nil
}

// Lock hands back a release that succeeds, since this session holds nothing.
func (r *recorder) Lock(context.Context) (driver.Unlock, error) {
	return func(context.Context) error { return nil }, nil
}

// Capabilities reports the declaration this session was built with.
func (r *recorder) Capabilities() capability.Set { return r.caps }

// History reports no store, since this session has no database to keep one in.
func (r *recorder) History() history.Store { return nil }

// ServerVersion reports the version the declaration was resolved against.
func (r *recorder) ServerVersion() string { return r.caps.Server() }

// Close releases nothing, since this session holds nothing.
func (r *recorder) Close(context.Context) error { return nil }

// compile-time proof that the recorder satisfies the contract a real session does.
var _ driver.Conn = (*recorder)(nil)

func TestAMigrationRunsAsOneTransaction(t *testing.T) {
	conn := newRecorder(t, true)

	if err := Apply(t.Context(), conn, statements("abc")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{"begin", "exec a0", "exec b1", "exec c2", "commit"}
	if !slices.Equal(conn.calls, want) {
		t.Errorf("the session was asked for\n  %v\nwant\n  %v", conn.calls, want)
		t.Log("Every statement runs between the begin and the commit. One outside the boundary is")
		t.Log("a schema change a failure would not undo, which is this task's whole claim.")
	}
}

func TestAFailurePartwayCommitsNothingAndRunsNothingFurther(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "b1"

	err := Apply(t.Context(), conn, statements("abc"))
	if err == nil {
		t.Fatal("Apply reported success for a migration whose second statement failed")
	}

	want := []string{"begin", "exec a0", "exec b1", "rollback"}
	if !slices.Equal(conn.calls, want) {
		t.Errorf("the session was asked for\n  %v\nwant\n  %v", conn.calls, want)
		t.Log("A failure must discard the transaction and stop. Executing the statements after it")
		t.Log("would run them in an aborted transaction, and committing would keep nothing anyway.")
	}

	if !errors.Is(err, errRefused) {
		t.Errorf("Apply returned %v, which does not unwrap to what the session reported", err)
	}
}

func TestAFailureNamesTheStatementThatCausedIt(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "c2"

	var failure *StatementError
	if err := Apply(t.Context(), conn, statements("abc")); !errors.As(err, &failure) {
		t.Fatalf("Apply returned %v, which is not a *StatementError", err)
	}

	if failure.Position != 3 {
		t.Errorf("the failure reports position %d, want 3", failure.Position)
		t.Log("The position counts from one, as golden files number statements, and M4-T9 records")
		t.Log("position-1 as the last statement that succeeded.")
	}

	if failure.Statement.SQL != "c2" {
		t.Errorf("the failure names statement %q, want %q", failure.Statement.SQL, "c2")
	}
}

func TestAMigrationThatCompilesToNothingIsRefusedBeforeAnythingRuns(t *testing.T) {
	conn := newRecorder(t, true)

	err := Apply(t.Context(), conn, nil)
	if !errors.Is(err, ErrNothingToRun) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrNothingToRun", err)
	}

	if len(conn.calls) != 0 {
		t.Errorf("the session was asked for %v before the refusal", conn.calls)
	}

	t.Log("An empty migration reported as applied is the conformance suite's \"reports success and")
	t.Log("changes nothing\" failure, one layer up from the Compile it forbids it in.")
}

func TestASessionWithoutTransactionalDDLIsRefused(t *testing.T) {
	conn := newRecorder(t, false)

	err := Apply(t.Context(), conn, statements("abc"))
	if !errors.Is(err, ErrNotAtomic) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrNotAtomic", err)
	}

	if !strings.Contains(err.Error(), "M4-T9") {
		t.Errorf("the refusal does not name the task that lifts it: %v", err)
	}

	if len(conn.calls) != 0 {
		t.Errorf("the session was asked for %v before the refusal", conn.calls)
	}

	t.Log("The branch reads Capabilities().TransactionalDDL() and never a driver name. MySQL")
	t.Log("declares false in M9-T3, and CLAUDE.md §11 forbids the if driver == \"mysql\" this")
	t.Log("would otherwise become.")
}

func TestAMigrationThatCannotBeOneTransactionIsRefused(t *testing.T) {
	conn := newRecorder(t, true)

	err := Apply(t.Context(), conn, statements("aBc"))
	if !errors.Is(err, ErrNotAtomic) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrNotAtomic", err)
	}

	if !strings.Contains(err.Error(), "M13-T4") {
		t.Errorf("the refusal does not name the task that lifts it: %v", err)
	}

	if len(conn.calls) != 0 {
		t.Errorf("the session was asked for %v before the refusal", conn.calls)
	}

	t.Log("Three runs means two commits, and neither M3-T6's history store nor M4-T9's dirty state")
	t.Log("exists to record that a migration got halfway. Refusing keeps this task's done-when -- a")
	t.Log("single transaction -- a rule rather than a description of what happens to be compiled.")
}

func TestTheRefusalCountsTheStatementsThatForcedTheSplit(t *testing.T) {
	conn := newRecorder(t, true)

	err := Apply(t.Context(), conn, statements("aBcDDe"))
	if err == nil {
		t.Fatal("Apply accepted a migration that cannot run as one transaction")
	}

	if !strings.Contains(err.Error(), "3 of its statements") {
		t.Errorf("the refusal does not count the statements that cannot run in a transaction: %v", err)
	}
}
