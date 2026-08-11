package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// stopping stages a session that refuses one statement, on a database with a history table already.
func stopping(t *testing.T, transactional bool, at string) *recorder {
	t.Helper()

	conn := newRecorder(t, transactional)
	conn.store.table = true
	conn.fail = at

	return conn
}

// onlyRun returns the single row a failed invocation left, failing when it left any other number.
func onlyRun(t *testing.T, conn *recorder) history.Record {
	t.Helper()

	if len(conn.store.runs) != 1 {
		t.Fatalf("the history holds %d rows, want the one the failed run is entitled to",
			len(conn.store.runs))
	}

	return conn.store.runs[0]
}

func TestAPartialFailureWithNoTransactionToUndoItIsRecordedDirty(t *testing.T) {
	conn := stopping(t, false, "d3")
	opts, _ := staged(t, "abcde")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrDirty) {
		t.Fatalf("Apply returned %v, want a failure reporting ErrDirty", err)
	}

	rec := onlyRun(t, conn)
	if rec.Outcome != history.Dirty {
		t.Fatalf("the run was recorded %q, want dirty", rec.Outcome)
	}

	if rec.LastStatement != 3 {
		t.Errorf("the row names statement %d, want the last one that succeeded", rec.LastStatement)
		t.Log("StatementError.Position is the statement that failed and LastStatement is the one")
		t.Log("before it. M9-T6 resumes from this index, so an off-by-one replays a statement.")
	}

	if !slices.Contains(conn.calls, "mark-dirty-outside-any-transaction "+versionAt(0)) {
		t.Errorf("the session was asked for %v, want a dirty row", conn.calls)
	}

	t.Log("The row is written after the transaction was discarded, which is the only order it")
	t.Log("can have -- and on a session like this the discard took nothing back with it.")
}

func TestAFailureAtTheFirstStatementIsFailedRatherThanDirty(t *testing.T) {
	conn := stopping(t, false, "a0")
	opts, _ := staged(t, "abc")

	_, err := Apply(t.Context(), conn, opts)
	if err == nil {
		t.Fatal("Apply reported success for a migration whose first statement was refused")
	}

	if errors.Is(err, ErrDirty) {
		t.Errorf("a run where nothing succeeded reports itself dirty: %v", err)
	}

	if rec := onlyRun(t, conn); rec.Outcome != history.Failed || rec.LastStatement != 0 {
		t.Errorf("the run was recorded %q at statement %d, want failed at none",
			rec.Outcome, rec.LastStatement)
		t.Log("A run where none succeeded left the schema unchanged, and Record.Validate refuses")
		t.Log("a dirty row that names no statement -- so the contract enforces this, not a habit.")
	}
}

func TestAPartialFailureOnASessionThatUndoesItIsStillMerelyFailed(t *testing.T) {
	conn := stopping(t, true, "d3")
	opts, _ := staged(t, "abcde")

	_, err := Apply(t.Context(), conn, opts)
	if err == nil {
		t.Fatal("Apply reported success for a migration whose fourth statement was refused")
	}

	if errors.Is(err, ErrDirty) {
		t.Errorf("a transaction that rolled back was reported as dirty: %v", err)
	}

	if rec := onlyRun(t, conn); rec.Outcome != history.Failed {
		t.Errorf("the run was recorded %q, want failed", rec.Outcome)
	}

	t.Log("This is the whole PostgreSQL story and it is unchanged by this task: the transaction")
	t.Log("took the three statements that succeeded back with it, so nothing survived.")
}

func TestAFailureAfterAnEarlierRunCommittedIsDirtyOnATransactionalSession(t *testing.T) {
	conn := stopping(t, true, "d3")
	opts, _ := staged(t, "a")

	exec := &executor{conn: conn, store: conn.store, opts: opts, batch: 1}
	groups := Groups(statements("aBcde"))

	survived, err := exec.runMigration(t.Context(), groups, exec.recorder(new(history.Record)))
	if err == nil {
		t.Fatal("runMigration reported success for a refused statement")
	}

	if survived != 2 {
		t.Errorf("the run reports %d statements left behind, want the one that committed and "+
			"the one that ran outside any transaction", survived)
		t.Log("The failing run is transactional on a transactional session, so its own statements")
		t.Log("went back -- but the two runs before it are still there. This is M13-T4's shape,")
		t.Log("and it is dirty on PostgreSQL, which the capability flag alone cannot see.")
	}
}

func TestAFailureInAStandaloneRunIsDirtyEvenWhereTheSessionUndoesTransactions(t *testing.T) {
	conn := stopping(t, true, "C2")
	opts, _ := staged(t, "a")

	exec := &executor{conn: conn, store: conn.store, opts: opts, batch: 1}
	groups := Groups(statements("aBCd"))

	survived, err := exec.runMigration(t.Context(), groups, exec.recorder(new(history.Record)))
	if err == nil {
		t.Fatal("runMigration reported success for a refused statement")
	}

	if survived != 2 {
		t.Errorf("the run reports %d statements left behind, want the two that ran outside a "+
			"transaction and the one that committed before them", survived)
		t.Log("The session declares transactional DDL, and this is still dirty: a standalone run's")
		t.Log("statements are not inside anything that could take them back. Branching on the")
		t.Log("capability alone would call this failed and leave a half-changed schema unrecorded.")
	}
}

func TestAFailureThatIsNotAStatementLeavesEveryStatementBehind(t *testing.T) {
	conn := newRecorder(t, false)
	conn.store.table = true
	conn.store.record = errors.New("the history table is gone")

	opts, _ := staged(t, "abc")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrDirty) {
		t.Fatalf("Apply returned %v, want a failure reporting ErrDirty", err)
	}

	if rec := onlyRun(t, conn); rec.LastStatement != 3 {
		t.Errorf("the row names statement %d, want all three", rec.LastStatement)
		t.Log("The history write failed after every statement had already auto-committed, so the")
		t.Log("schema carries all three and nothing recorded that until this row. There is no")
		t.Log("StatementError to read a position from, and the counter is the honest answer.")
	}
}

func TestARollbackThatStopsPartwayIsRecordedDirtyGoingDown(t *testing.T) {
	conn := stopping(t, false, "b1")
	carried(t, conn, 0)

	opts, _ := staged(t, "abc")

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, ErrDirty) {
		t.Fatalf("Rollback returned %v, want a failure reporting ErrDirty", err)
	}

	rec := conn.store.runs[len(conn.store.runs)-1]
	if rec.Outcome != history.Dirty || rec.Direction != ir.Down {
		t.Fatalf("the run was recorded %q %q, want a dirty down", rec.Direction, rec.Outcome)
	}

	if rec.LastStatement != 1 {
		t.Errorf("the row names statement %d, want the one that succeeded", rec.LastStatement)
	}

	var dirty *DirtyError
	if !errors.As(err, &dirty) {
		t.Fatalf("errors.As found no *DirtyError in %T", err)
	}

	if dirty.Direction != ir.Down {
		t.Errorf("the failure reports direction %q, want the one the run was performing",
			dirty.Direction)
		t.Log("An operator undoing a migration is told what is still applied of the undo, which")
		t.Log("is a different sentence from the one an apply gets, and the row already says down.")
	}

	t.Log("M4-T6 routed both directions through one perform, so one MarkDirty call site serves")
	t.Log("the apply and the rollback -- a down that stops partway is the same window.")
}

func TestADirtyFailureSaysHowFarItGotAndWhatIsStillThere(t *testing.T) {
	conn := stopping(t, false, "d3")
	opts, _ := staged(t, "abcde")

	_, err := Apply(t.Context(), conn, opts)

	var dirty *DirtyError
	if !errors.As(err, &dirty) {
		t.Fatalf("errors.As found no *DirtyError in %T", err)
	}

	if dirty.LastStatement != 3 || dirty.Statements != 5 {
		t.Errorf("the failure reports %d of %d, want 3 of 5", dirty.LastStatement, dirty.Statements)
	}

	if dirty.Direction != ir.Up || dirty.Ordinal != 1 || dirty.Batch != 1 {
		t.Errorf("the failure reports %q batch %d ordinal %d, want an up at batch 1 ordinal 1",
			dirty.Direction, dirty.Batch, dirty.Ordinal)
	}

	message := err.Error()

	for _, want := range []string{
		migrationAt(t, 0).Filename(),
		"statements 1 through 3 of\n5 succeeded",
		"statement 4",
		"dirty at statement 3",
		"no transactional DDL",
		"resolved explicitly",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not say %q:\n%s", want, message)
		}
	}

	if !errors.Is(err, errRefused) {
		t.Error("the failure no longer reaches what the driver reported")
	}
}

func TestATransactionalSessionIsNotToldItHasNoTransactionalDDL(t *testing.T) {
	dirty := &DirtyError{LastStatement: 2, Statements: 4, TransactionalDDL: true, Err: errRefused}

	if strings.Contains(dirty.Error(), "no transactional DDL") {
		t.Errorf("a session that declares transactional DDL is told it does not:\n%s", dirty.Error())
		t.Log("Reaching this state on such a session means a standalone run or an earlier commit,")
		t.Log("not a driver-wide declaration, and naming the wrong cause sends the operator to")
		t.Log("check a setting that is already correct.")
	}
}

func TestABookkeepingFailureOnADirtyRunIsJoinedRatherThanReplacing(t *testing.T) {
	conn := stopping(t, false, "d3")
	conn.store.dirty = errors.New("the history table is gone")

	opts, _ := staged(t, "abcde")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, errRefused) {
		t.Error("the failure that stopped the migration was replaced by the bookkeeping failure")
	}

	if !strings.Contains(err.Error(), "the history table is gone") {
		t.Errorf("the failure does not report that no dirty row was written: %v", err)
		t.Log("Reporting only the migration's failure hides that migr status will show this")
		t.Log("database as pending while its schema is between states, which is the worse lie.")
	}
}

func TestTheDirtyWritePathDoesNotInheritTheContextThatCanceledTheRun(t *testing.T) {
	conn := stopping(t, false, "b1")
	opts, _ := staged(t, "abc")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := Apply(ctx, conn, opts); err == nil {
		t.Fatal("Apply reported success for a refused statement")
	}

	for _, call := range conn.calls {
		if strings.HasSuffix(call, "-on-a-canceled-context") {
			t.Errorf("the session was asked for %v, and %q inherited the canceled context",
				conn.calls, call)
		}
	}

	if rec := onlyRun(t, conn); rec.Outcome != history.Dirty {
		t.Errorf("the run was recorded %q, want the dirty row written despite the cancellation",
			rec.Outcome)
		t.Log("Ctrl-C during a migration on a driver with no transactional DDL is the case this")
		t.Log("path exists for: the statements that ran are still there, and a row nobody wrote")
		t.Log("leaves the next up believing the migration never started.")
	}
}
