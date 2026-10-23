package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// ofTheRun returns what a session was asked after the history was read, which is where a migration runs.
func ofTheRun(conn *recorder) []string {
	for i, call := range conn.calls {
		if strings.HasPrefix(call, "begin") || strings.HasPrefix(call, "exec") {
			return conn.calls[i:]
		}
	}

	return nil
}

// outcomes lists the rows a history holds as outcome, ordinal and last statement.
type outcome struct {
	outcome history.Outcome
	ordinal int32
	last    int32
}

// outcomesOf reads the rows a journal holds back as outcomes.
func outcomesOf(conn *recorder) []outcome {
	out := make([]outcome, 0, len(conn.store.runs))

	for _, row := range conn.store.runs {
		out = append(out, outcome{row.Outcome, row.Ordinal, row.LastStatement})
	}

	return out
}

func TestAMigrationSplitsAroundAStatementThatCannotRunInATransaction(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "aBc")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply returned %v, want the migration to run in three parts", err)
	}

	v := versionAt(0)
	want := []string{
		"begin", "exec a0", "mark-dirty " + v, "commit",
		"exec-outside-any-transaction B1", "mark-dirty-outside-any-transaction " + v,
		"begin", "exec c2", "record " + v, "commit",
		"unlock",
	}
	if got := ofTheRun(conn); !slices.Equal(got, want) {
		t.Fatalf("the session was asked for\n  %v\nwant\n  %v", got, want)
	}

	rows := []outcome{{history.Dirty, 1, 1}, {history.Dirty, 2, 2}, {history.Applied, 3, 0}}
	if got := outcomesOf(conn); !slices.Equal(got, rows) {
		t.Errorf("the history holds %+v, want %+v", got, rows)
	}

	if run := result.Runs[0]; run.Ordinal != 3 || run.Statements != 3 {
		t.Errorf("the run reports ordinal %d and %d statements, want 3 and 3", run.Ordinal, run.Statements)
	}

	t.Log("The part before the standalone statement commits its own dirty row, so a crash during")
	t.Log("CREATE INDEX CONCURRENTLY leaves a database whose history says statement 1 ran -- and")
	t.Log("the row after the statement says 2, which is the one window no transaction closes.")
}

func TestTheMigrationAfterASplitOneTakesTheOrdinalsAfterItsRows(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "aBc", "d")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := result.Runs[1].Ordinal; got != 4 {
		t.Errorf("the second migration was recorded at ordinal %d, want 4: the first wrote three rows", got)
		t.Log("The history is keyed by (batch, ordinal), so a row at an ordinal another holds is refused.")
	}
}

func TestAMigrationOfNothingButStandaloneStatementsRunsWithNoTransaction(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "AB")

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply returned %v, want the migration to run outside any transaction", err)
	}

	v := versionAt(0)
	want := []string{
		"exec-outside-any-transaction A0", "mark-dirty-outside-any-transaction " + v,
		"exec-outside-any-transaction B1", "record-outside-any-transaction " + v,
		"unlock",
	}
	if got := ofTheRun(conn); !slices.Equal(got, want) {
		t.Fatalf("the session was asked for\n  %v\nwant\n  %v", got, want)
	}

	t.Log("There is no transaction for the applied row to be written inside, so it follows the last")
	t.Log("statement: what M4-T5 refused for having nowhere to write it is the dirty model's to describe.")
}

func TestAFailurePartwayThroughASplitRecordsWhatTheCommittedPartsLeft(t *testing.T) {
	cases := []struct {
		fail string
		want history.Outcome
		last int32
	}{
		{fail: "a0", want: history.Failed},
		{fail: "B1", want: history.Dirty, last: 1},
		{fail: "c2", want: history.Dirty, last: 2},
	}

	for _, tc := range cases {
		t.Run(tc.fail, func(t *testing.T) {
			conn := newRecorder(t, true)
			conn.fail = tc.fail
			opts, _ := staged(t, "aBc")

			_, err := Apply(t.Context(), conn, opts)
			if err == nil {
				t.Fatal("Apply succeeded over a statement the server refused")
			}

			rows := conn.store.runs
			stopped := rows[len(rows)-1]

			if stopped.Outcome != tc.want || stopped.LastStatement != tc.last {
				t.Errorf("the last row is %s at statement %d, want %s at %d", stopped.Outcome,
					stopped.LastStatement, tc.want, tc.last)
			}

			var dirty *DirtyError
			if got := errors.As(err, &dirty); got != (tc.want == history.Dirty) {
				t.Fatalf("Apply returned %v, a DirtyError %v, want %v", err, got, tc.want == history.Dirty)
			}

			if dirty != nil && (!dirty.Split || !strings.Contains(err.Error(), "cannot run inside a transaction")) {
				t.Errorf("the dirty run does not say the split is why it left statements behind: %v", err)
			}

			if dirty != nil && dirty.Ordinal != stopped.Ordinal {
				t.Errorf("the error names ordinal %d and the row is at %d", dirty.Ordinal, stopped.Ordinal)
			}
		})
	}

	t.Log("On transactional DDL a failing part rolls back alone: what survives is every part")
	t.Log("before it, which is why a failure at the last statement leaves two, not none.")
}

func TestAStandaloneStatementThatGivesUpOnALockIsNotTriedAgain(t *testing.T) {
	w := &waits{}
	conn := busyOn(t, true, "B1", 1)
	opts, notices := retrying(t, w, "aBc")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, driver.ErrStatementLockTimeout) {
		t.Fatalf("Apply returned %v, want the lock wait reported", err)
	}

	if got := execs(conn, "B1"); got != 1 || len(*notices) != 0 {
		t.Errorf("B1 was sent %d times after %d retries, want once and none", got, len(*notices))
	}

	if !strings.Contains(err.Error(), "not tried again") {
		t.Errorf("the failure does not say why it was not retried: %v", err)
	}

	t.Log("Nothing rolls back a statement that ran outside a transaction. CREATE INDEX")
	t.Log("CONCURRENTLY that gives up leaves an INVALID index behind, and a second attempt fails")
	t.Log("on the name -- so the run stops, dirty, and M13-T11 owns cleaning that index up.")
}

func TestALockWaitInALaterPartRetriesThatPartAndNothingCommittedBeforeIt(t *testing.T) {
	w := &waits{}
	conn := busyOn(t, true, "c2", 1)
	opts, notices := retrying(t, w, "aBc")

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for sql, want := range map[string]int{"a0": 1, "B1": 1, "c2": 2} {
		if got := execs(conn, sql); got != want {
			t.Errorf("%s was sent %d times, want %d", sql, got, want)
		}
	}

	if len(*notices) != 1 || (*notices)[0].Statement != 3 {
		t.Errorf("retries reported %+v, want one naming statement 3", *notices)
	}

	rows := []outcome{{history.Dirty, 1, 1}, {history.Dirty, 2, 2}, {history.Applied, 3, 0}}
	if got := outcomesOf(conn); !slices.Equal(got, rows) {
		t.Errorf("the history holds %+v, want %+v", got, rows)
	}

	t.Log("The rollback undid only the part that waited. Rerunning the parts before it would build")
	t.Log("the index a second time -- so the retry starts where what survived ends, as it does")
	t.Log("on an engine whose DDL commits.")
}

func TestAnExpansionAfterASplitRunsInsideItsOwnPartsTransaction(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := emulating(t, conn, ir.Operations{addIndex("A0"), retype("tag"), addIndex("b2")})

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	v := versionAt(0)
	want := []string{
		"exec-outside-any-transaction A0", "mark-dirty-outside-any-transaction " + v,
		"begin", "expand tag", "exec ~tag", "exec b2", "record " + v, "commit",
		"unlock",
	}
	if got := ofTheRun(conn); !slices.Equal(got, want) {
		t.Fatalf("the session was asked for\n  %v\nwant\n  %v", got, want)
	}

	t.Log("The expansion reads the catalog after the standalone statement committed and before")
	t.Log("anything in its own part ran -- exactly what the steps before it left.")
}

func TestAPartTheRunAlreadyCommittedIsSkippedWholeOnAResume(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "aBc")

	e, err := newExecutor(conn, opts, ModeApply)
	if err != nil {
		t.Fatalf("newExecutor: %v", err)
	}

	conn.store.table = true
	e.batch, e.from = 1, 2

	ready, err := e.compileFor(t.Context(), queued{migration: migrationAt(t, 0), ordinal: 1}, ir.Up,
		Emitted{Operations: operations("aBc")})
	if err != nil {
		t.Fatalf("compileFor: %v", err)
	}

	if _, err := e.perform(t.Context(), ready); err != nil {
		t.Fatalf("perform: %v", err)
	}

	want := []string{"begin", "exec c2", "record " + versionAt(0), "commit"}
	if got := ofTheRun(conn); !slices.Equal(got, want) {
		t.Fatalf("the session was asked for\n  %v\nwant\n  %v", got, want)
	}

	t.Log("A resume from a row saying statement 2 opens no transaction for the part it skips")
	t.Log("and writes no second row for the standalone statement that already ran.")
}

func TestARollbackSplitsAroundItsOwnStandaloneStatementWhateverItsUpDid(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "ab")
	src.down[versionAt(0)] = ir.Operations{addIndex("X0"), addIndex("y1")}

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback returned %v, want the down to run in two parts", err)
	}

	v := versionAt(0)
	want := []string{
		"exec-outside-any-transaction X0", "mark-dirty-outside-any-transaction " + v,
		"begin", "exec y1", "record " + v, "commit",
		"unlock",
	}
	if got := ofTheRun(conn); !slices.Equal(got, want) {
		t.Fatalf("the session was asked for\n  %v\nwant\n  %v", got, want)
	}

	t.Log("An inverse groups independently of what it inverts: CREATE INDEX runs in a transaction")
	t.Log("and DROP INDEX CONCURRENTLY cannot. Both refusals M4-T6 inherited fired before the first")
	t.Log("statement of the whole batch, so a migration that applied cleanly could not be undone.")
}

func TestOnAnEngineWhoseDDLCommitsAFailedPartKeepsWhatItRan(t *testing.T) {
	conn := newRecorder(t, false)
	conn.fail = "d3"
	opts, _ := staged(t, "aBcd")

	_, err := Apply(t.Context(), conn, opts)

	var dirty *DirtyError
	if !errors.As(err, &dirty) || dirty.LastStatement != 3 {
		t.Fatalf("Apply returned %v, want a dirty run at statement 3", err)
	}

	t.Log("Where DDL commits as it goes, c2 survived the failure of the part it shared with d3,")
	t.Log("as it would in a migration with no split at all.")
}
