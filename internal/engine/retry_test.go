package engine

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// retrying stages options that retry a lock wait up to three times, from a 100ms first delay.
func retrying(t *testing.T, w *waits, specs ...string) (Options, *[]Retry) {
	t.Helper()

	opts, _ := staged(t, specs...)
	opts.Backoff = Exponential{Base: 100 * time.Millisecond, Cap: time.Second, Retries: 3}
	opts.Sleep = w.sleep

	notices := &[]Retry{}
	opts.Retried = func(r Retry) { *notices = append(*notices, r) }

	return opts, notices
}

// busyOn stages a session whose one statement gives up on its lock the given number of times.
func busyOn(t *testing.T, transactional bool, sql string, times int) *recorder {
	t.Helper()

	conn := newRecorder(t, transactional)
	conn.store.table = true
	conn.busy = map[string]int{sql: times}

	return conn
}

// execs counts how many times a statement was sent, whichever transaction it was sent in.
func execs(conn *recorder, sql string) int {
	count := 0

	for _, call := range conn.calls {
		if call == "exec "+sql || call == "exec-outside-any-transaction "+sql {
			count++
		}
	}

	return count
}

func TestALockWaitSpentIsTriedAgainAndTheMigrationApplies(t *testing.T) {
	w := &waits{}
	conn := busyOn(t, true, "b1", 2)
	opts, notices := retrying(t, w, "abc")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := result.Runs[0].Retries; got != 2 {
		t.Errorf("Retries = %d, want 2", got)
	}

	if !slices.Equal(w.asked, []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}) {
		t.Errorf("backed off %v, want 100ms then 200ms", w.asked)
	}

	if got := execs(conn, "a0"); got != 3 {
		t.Errorf("a0 was sent %d times, want 3: on transactional DDL the rollback undid it", got)
	}

	if len(*notices) != 2 || (*notices)[0].Statement != 2 || (*notices)[1].Retry != 2 {
		t.Errorf("retries reported %+v, want two naming statement 2", *notices)
	}

	if rows := conn.store.runs; len(rows) != 1 || rows[0].Outcome != history.Applied {
		t.Errorf("the history holds %+v, want one applied row and nothing for the attempts", rows)
	}

	t.Log("The whole migration reruns because the rollback released every lock its earlier")
	t.Log("statements took: retrying in place would hold them, and the readers behind them, for")
	t.Log("the length of the backoff.")
}

func TestOnAnEngineWhoseDDLCommitsTheRetryStartsAtTheStatementThatWaited(t *testing.T) {
	conn := busyOn(t, false, "c2", 1)
	opts, _ := retrying(t, &waits{}, "abcd")

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for sql, want := range map[string]int{"a0": 1, "b1": 1, "c2": 2, "d3": 1} {
		if got := execs(conn, sql); got != want {
			t.Errorf("%s was sent %d times, want %d", sql, got, want)
		}
	}

	t.Log("a0 and b1 committed as they ran, so sending them again would be applying them twice.")
}

func TestARetriedRunStillCountsEveryStatementItRan(t *testing.T) {
	conn := busyOn(t, false, "c2", 1)
	opts, _ := retrying(t, &waits{}, "abcd")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := result.Runs[0].Statements; got != 4 {
		t.Errorf("Statements = %d, want 4: the retry moved where it starts, not what the run did", got)
	}
}

func TestALockWaitThatOutlastsEveryRetryFailsNamingHowLongItWasTried(t *testing.T) {
	w := &waits{}
	conn := busyOn(t, true, "b1", 10)
	opts, _ := retrying(t, w, "abc")

	_, err := Apply(t.Context(), conn, opts)

	var exhausted *LockRetryError
	if !errors.As(err, &exhausted) {
		t.Fatalf("Apply returned %v, want a LockRetryError", err)
	}

	if exhausted.Attempts != 4 || exhausted.Waited != 700*time.Millisecond {
		t.Errorf("Attempts = %d, Waited = %s, want 4 and 700ms", exhausted.Attempts, exhausted.Waited)
	}

	if !errors.Is(err, driver.ErrStatementLockTimeout) {
		t.Error("the exhausted failure no longer reaches the lock timeout, which is what exit 3 reads")
	}

	var stopped *MigrationError
	if !errors.As(err, &stopped) || stopped.Operation == nil {
		t.Errorf("the failure names no operation: %v", err)
	}

	if row := onlyRun(t, conn); row.Outcome != history.Failed {
		t.Errorf("the history holds a %s row, want the one failed run", row.Outcome)
	}
}

func TestALockWaitSpentPreparingTheHistoryIsTriedAgain(t *testing.T) {
	w := &waits{}
	conn := newRecorder(t, true)
	conn.store.ensureBusy = 2
	opts, notices := retrying(t, w, "abc")

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !slices.Equal(w.asked, []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}) {
		t.Errorf("backed off %v, want 100ms then 200ms", w.asked)
	}

	if len(*notices) != 2 || !(*notices)[0].History || (*notices)[1].Retry != 2 {
		t.Errorf("retries reported %+v, want two naming the history", *notices)
	}

	if row := onlyRun(t, conn); row.Outcome != history.Applied {
		t.Errorf("the history holds a %s row, want the migration applied", row.Outcome)
	}

	t.Log("SQLite has no lock queue: under steady writers a BEGIN IMMEDIATE can spend its whole")
	t.Log("busy_timeout without once finding the lock free, and that is a wait to back off from like any other.")
}

func TestALockWaitPreparingTheHistoryThatOutlastsEveryRetryStopsTheRun(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.ensureBusy = 10
	opts, _ := retrying(t, &waits{}, "abc")

	_, err := Apply(t.Context(), conn, opts)

	var exhausted *LockRetryError
	if !errors.As(err, &exhausted) || exhausted.Attempts != 4 {
		t.Fatalf("Apply returned %v, want a LockRetryError after 4 attempts", err)
	}

	if !errors.Is(err, driver.ErrStatementLockTimeout) {
		t.Error("the exhausted failure no longer reaches the lock timeout, which is what exit 3 reads")
	}

	if len(conn.store.runs) != 0 {
		t.Errorf("the history holds %+v, want nothing: no migration ran", conn.store.runs)
	}
}

func TestAFailureThatIsNotALockWaitIsNotRetried(t *testing.T) {
	w := &waits{}
	conn := stopping(t, true, "b1")
	opts, _ := retrying(t, w, "abc")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, errRefused) {
		t.Fatalf("Apply returned %v, want the refusal itself", err)
	}

	if len(w.asked) != 0 || execs(conn, "a0") != 1 {
		t.Errorf("a refused statement was retried: waited %v, sent a0 %d times", w.asked,
			execs(conn, "a0"))
	}

	var exhausted *LockRetryError
	if errors.As(err, &exhausted) {
		t.Error("a failure nothing retried was reported as retries spent")
	}
}

func TestARunWithNoBackoffNeverRetries(t *testing.T) {
	conn := busyOn(t, true, "b1", 1)
	opts, _ := staged(t, "abc")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, driver.ErrStatementLockTimeout) {
		t.Fatalf("Apply returned %v, want the lock timeout", err)
	}

	var exhausted *LockRetryError
	if errors.As(err, &exhausted) {
		t.Error("a run with no backoff reported retries")
	}
}

func TestACanceledBackoffStopsTheRunWithTheCancellation(t *testing.T) {
	conn := busyOn(t, true, "b1", 10)
	opts, _ := retrying(t, &waits{}, "abc")

	ctx, cancel := context.WithCancel(t.Context())
	opts.Retried = func(Retry) { cancel() }

	_, err := Apply(ctx, conn, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply returned %v, want the cancellation", err)
	}

	if got := execs(conn, "b1"); got != 1 {
		t.Errorf("b1 was sent %d times after the run was canceled during its backoff", got)
	}
}

func TestAChunkThatWaitedIsRunAgainAlone(t *testing.T) {
	opts, _, _, table := chunked(t, 10, 4)
	table.busyAt = 2

	var notices []Retry

	opts.Backoff = Exponential{Base: 100 * time.Millisecond, Retries: 3}
	opts.Retried = func(r Retry) { notices = append(notices, r) }

	result, err := Apply(t.Context(), table.session, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.Runs[0].Retries != 1 || len(notices) != 1 || notices[0].Chunk != 2 {
		t.Errorf("Retries = %d, notices %+v, want one retry of chunk 2", result.Runs[0].Retries,
			notices)
	}

	if !table.writtenOnce() {
		t.Error("a row was written twice or not at all: the retried chunk's transaction was rolled back")
	}

	t.Log("Every chunk commits on its own, so a chunk that waited is the only thing to run again.")
}

func TestTheBackoffDoublesFromItsBaseAndStopsAtItsCap(t *testing.T) {
	x := Exponential{Base: 500 * time.Millisecond, Cap: 3 * time.Second, Retries: 5}

	var got []time.Duration

	for retry := 1; ; retry++ {
		d, again := x.Delay(retry)
		if !again {
			break
		}

		got = append(got, d)
	}

	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 3 * time.Second,
		3 * time.Second}
	if !slices.Equal(got, want) {
		t.Errorf("delays = %v, want %v", got, want)
	}
}

func TestJitterRandomizesOnlyTheUpperHalfOfADelay(t *testing.T) {
	low := Exponential{Base: time.Second, Retries: 1, Jitter: func(time.Duration) time.Duration { return 0 }}
	high := Exponential{Base: time.Second, Retries: 1, Jitter: func(s time.Duration) time.Duration { return 10 * s }}

	if d, _ := low.Delay(1); d != 500*time.Millisecond {
		t.Errorf("the least jitter gives %s, want 500ms: a retry must never fire at once", d)
	}

	if d, _ := high.Delay(1); d != time.Second {
		t.Errorf("a jitter past its span gives %s, want it held to 1s", d)
	}

	t.Log("Equal jitter: half the delay is fixed, so two runs colliding on one lock spread out")
	t.Log("without either retrying straight back into the queue it just left.")
}

func TestABackoffWithNoRetriesOrNoBaseAllowsNone(t *testing.T) {
	for name, x := range map[string]Exponential{
		"no retries": {Base: time.Second},
		"no base":    {Retries: 5},
	} {
		if _, again := x.Delay(1); again {
			t.Errorf("%s allowed a retry", name)
		}
	}
}

func TestATransactionalRerunCompilesItsEmulatedOperationsAgain(t *testing.T) {
	conn := newRecorder(t, true)
	conn.busy = map[string]int{"b2": 1}
	opts, _ := emulating(t, conn, ir.Operations{retype("tag"), addIndex("b2")})
	opts.Backoff = Exponential{Base: time.Millisecond, Retries: 1}
	opts.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	asked := 0

	for _, call := range conn.calls {
		if call == "expand tag" {
			asked++
		}
	}

	if asked != 2 {
		t.Errorf("tag was expanded %d times, want 2: the rollback undid the catalog it read", asked)
	}

	t.Log("An expansion is rendered from the catalog the steps ahead of it left. The rollback put")
	t.Log("that catalog back, so the second attempt renders it again rather than replaying a")
	t.Log("statement compiled against a moment that no longer exists.")
}

func TestARerunPastCommittedStatementsKeepsTheExpansionThatRan(t *testing.T) {
	conn := newRecorder(t, false)
	conn.busy = map[string]int{"b2": 1}
	opts, _ := emulating(t, conn, ir.Operations{retype("tag"), addIndex("b2")})
	opts.Backoff = Exponential{Base: time.Millisecond, Retries: 1}
	opts.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := execs(conn, "~tag"); got != 1 {
		t.Errorf("the redefinition was sent %d times, want once: it committed when it ran", got)
	}

	t.Log("On an engine whose DDL commits, the redefinition is applied and its catalog moved on;")
	t.Log("expanding it again would render a statement against a column it already changed.")
}
