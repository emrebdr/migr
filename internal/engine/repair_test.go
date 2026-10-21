package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// fakeRepairer answers every repair the same way and records what it was asked about.
type fakeRepairer struct {
	// answer is what every repair reports.
	answer driver.Repaired
	// err is what every repair fails with, nil for none.
	err error
	// asked records each repair as its moment and the statement.
	asked []string
}

// Repair records the request and answers as it was told to.
func (f *fakeRepairer) Repair(_ context.Context, _ ir.Operation, stmt driver.Statement, when driver.RepairTime,
	_ error,
) (driver.Repaired, error) {
	f.asked = append(f.asked, string(when)+" "+stmt.SQL)

	return f.answer, f.err
}

func TestARepairedStandaloneStatementThatGaveUpOnALockIsTriedAgain(t *testing.T) {
	w := &waits{}
	conn := busyOn(t, true, "B1", 1)
	opts, notices := retrying(t, w, "aBc")
	repairer := &fakeRepairer{answer: driver.Repaired{Action: "dropped the INVALID index", Rerunnable: true}}
	opts.Repairer = repairer

	var told []Cleanup
	opts.Repaired = func(c Cleanup) { told = append(told, c) }

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := execs(conn, "B1"); got != 2 || len(*notices) != 1 {
		t.Errorf("B1 was sent %d times after %d retries, want twice and one", got, len(*notices))
	}

	if !slices.Equal(repairer.asked, []string{"after_failure B1"}) {
		t.Errorf("the repairer was asked %v", repairer.asked)
	}

	repairs := result.Runs[0].Repairs
	if len(told) != 1 || len(repairs) != 1 || repairs[0].Action != "dropped the INVALID index" || repairs[0].Position != 2 {
		t.Errorf("the repair was told %v and reported %+v", told, repairs)
	}
}

func TestAStandaloneStatementWhoseRepairLeavesItUnsafeToRerunIsNotTriedAgain(t *testing.T) {
	w := &waits{}
	conn := busyOn(t, true, "B1", 1)
	opts, _ := retrying(t, w, "aBc")
	opts.Repairer = &fakeRepairer{}

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, driver.ErrStatementLockTimeout) || !strings.Contains(err.Error(), "not tried again") {
		t.Fatalf("Apply returned %v, want the lock wait refused a retry", err)
	}

	if got := execs(conn, "B1"); got != 1 {
		t.Errorf("B1 was sent %d times, want once", got)
	}
}

func TestARepairThatFailsStopsTheRunNamingBothFailures(t *testing.T) {
	w := &waits{}
	conn := busyOn(t, true, "B1", 1)
	opts, _ := retrying(t, w, "aBc")
	opts.Repairer = &fakeRepairer{err: errors.New("the drop timed out too")}

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, driver.ErrStatementLockTimeout) || !strings.Contains(err.Error(), "the drop timed out too") {
		t.Fatalf("Apply returned %v, want the lock wait and the failed repair", err)
	}

	if got := execs(conn, "B1"); got != 1 {
		t.Errorf("B1 was sent %d times, want once", got)
	}
}

func TestAStatementInsideATransactionIsNeverRepaired(t *testing.T) {
	w := &waits{}
	conn := busyOn(t, true, "b1", 1)
	opts, _ := retrying(t, w, "abc")
	repairer := &fakeRepairer{answer: driver.Repaired{Rerunnable: true}}
	opts.Repairer = repairer

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(repairer.asked) != 0 {
		t.Errorf("a statement its transaction rolled back was repaired: %v", repairer.asked)
	}
}

// resumeAt resumes the staged migration "aBc" past its first statement, repairing before its second.
func resumeAt(t *testing.T, answer driver.Repaired) (*recorder, *fakeRepairer, Run) {
	t.Helper()

	conn := newRecorder(t, true)
	opts, _ := staged(t, "aBc")
	repairer := &fakeRepairer{answer: answer}
	opts.Repairer = repairer

	e, err := newExecutor(conn, opts, ModeApply)
	if err != nil {
		t.Fatalf("newExecutor: %v", err)
	}

	conn.store.table = true
	e.batch, e.from, e.recheck = 1, 1, 2

	ready, err := e.compileFor(t.Context(), queued{migration: migrationAt(t, 0), ordinal: 1}, ir.Up,
		Emitted{Operations: operations("aBc")})
	if err != nil {
		t.Fatalf("compileFor: %v", err)
	}

	run, err := e.perform(t.Context(), ready)
	if err != nil {
		t.Fatalf("perform: %v", err)
	}

	return conn, repairer, run
}

func TestAResumeRepairsBeforeItRerunsAndSkipsWorkAlreadyDone(t *testing.T) {
	conn, repairer, run := resumeAt(t, driver.Repaired{Action: "rebuilt it with REINDEX", Done: true})

	if got := execs(conn, "B1"); got != 0 || !slices.Equal(repairer.asked, []string{"before_resume B1"}) {
		t.Errorf("B1 was sent %d times and the repairer asked %v, want it skipped", got, repairer.asked)
	}

	if execs(conn, "c2") != 1 || len(run.Repairs) != 1 || !run.Repairs[0].Done {
		t.Errorf("the run carried on as %v, reporting %+v", conn.calls, run.Repairs)
	}

	conn, _, _ = resumeAt(t, driver.Repaired{Rerunnable: true})
	if got := execs(conn, "B1"); got != 1 {
		t.Errorf("a resume whose repair found nothing done sent B1 %d times, want once", got)
	}
}

func TestAStatementThatBlocksNoWriteIsBoundedByTheConcurrentTimeoutAlone(t *testing.T) {
	e := &executor{opts: Options{StatementTimeout: time.Millisecond}}

	for _, tc := range []struct {
		name    string
		stmt    driver.Statement
		limited bool
	}{
		{"a statement that blocks no write, with no concurrent_timeout", driver.Statement{Unblocking: true, NoTransaction: true}, false},
		{"a raw statement outside any transaction", driver.Statement{NoTransaction: true}, true},
		{"an ordinary statement", driver.Statement{}, true},
	} {
		bound, cancel := e.boundFor(t.Context(), tc.stmt)
		_, set := bound.Deadline()
		cancel()

		if set != tc.limited {
			t.Errorf("%s: bounded %v, want %v", tc.name, set, tc.limited)
		}
	}

	e.opts.ConcurrentTimeout = time.Hour

	bound, cancel := e.boundFor(t.Context(), driver.Statement{Unblocking: true})
	defer cancel()

	if deadline, set := bound.Deadline(); !set || time.Until(deadline) < 59*time.Minute {
		t.Errorf("a statement that blocks no write is bounded at %v, want safety.concurrent_timeout", deadline)
	}
}

func TestAFailureTheRepairSaysWasTheLeftoverItselfIsTriedAgainOnlyWhenItSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		retry bool
		sent  int
	}{
		{"a repair saying the leftover caused it", true, 4},
		{"a repair that only cleaned up", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newRecorder(t, true)
			conn.store.table = true
			conn.fail = "B1"

			opts, _ := retrying(t, &waits{}, "aBc")
			opts.Repairer = &fakeRepairer{answer: driver.Repaired{Action: "dropped it", Rerunnable: true, Retry: tc.retry}}

			if _, err := Apply(t.Context(), conn, opts); err == nil {
				t.Fatal("Apply over a statement that always fails succeeded")
			}

			if got := execs(conn, "B1"); got != tc.sent {
				t.Errorf("B1 was sent %d times, want %d", got, tc.sent)
			}
		})
	}
}

func TestAFailedStatementTheRepairFindsAlreadyDoneCountsAsRun(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.fail = "B1"

	opts, _ := staged(t, "aBc")
	opts.Repairer = &fakeRepairer{answer: driver.Repaired{Action: "found it already built", Done: true}}

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := execs(conn, "B1"); got != 1 || len(result.Runs[0].Repairs) != 1 || execs(conn, "c2") != 1 {
		t.Errorf("B1 was sent %d times and the run reported %+v", got, result.Runs[0].Repairs)
	}
}
