package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/history"
)

// slowOn stages a session whose one statement runs until something stops it.
func slowOn(t *testing.T, sql string) *recorder {
	t.Helper()

	conn := newRecorder(t, true)
	conn.store.table = true
	conn.slow = sql

	return conn
}

func TestAStatementPastItsBoundIsStoppedAndNamesTheBound(t *testing.T) {
	conn := slowOn(t, "b1")
	opts, _ := retrying(t, &waits{}, "abc")
	opts.StatementTimeout = 20 * time.Millisecond

	_, err := Apply(t.Context(), conn, opts)

	var stopped *TimeoutError
	if !errors.As(err, &stopped) || stopped.Bound != BoundStatement {
		t.Fatalf("Apply returned %v, want the statement bound named", err)
	}

	if stopped.Limit != 20*time.Millisecond {
		t.Errorf("the bound reports %s, want 20ms", stopped.Limit)
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("the failure no longer reaches the deadline that stopped it")
	}

	var failure *MigrationError
	if !errors.As(err, &failure) || failure.Operation == nil {
		t.Errorf("the failure names no operation: %v", err)
	}

	if got := execs(conn, "b1"); got != 1 {
		t.Errorf("b1 was sent %d times: a statement that ran too long is not a lock wait to retry", got)
	}

	if row := onlyRun(t, conn); row.Outcome != history.Failed {
		t.Errorf("the history holds a %s row, want the failed run", row.Outcome)
	}
}

func TestAMigrationWhoseRetriesOutlastItsBoundNamesTheMigrationBound(t *testing.T) {
	conn := busyOn(t, true, "b1", 100)
	opts, _ := staged(t, "abc")
	opts.Backoff = Exponential{Base: time.Hour, Retries: 5}
	opts.MigrationTimeout = 30 * time.Millisecond

	_, err := Apply(t.Context(), conn, opts)

	var stopped *TimeoutError
	if !errors.As(err, &stopped) || stopped.Bound != BoundMigration {
		t.Fatalf("Apply returned %v, want the migration bound named", err)
	}

	t.Log("The backoff is an hour and the migration's bound is 30ms, so it is the bound that ends")
	t.Log("the wait: retries count against the migration, not beside it.")
}

func TestACanceledRunIsReportedAsCanceledRatherThanAsABound(t *testing.T) {
	conn := slowOn(t, "b1")
	opts, _ := staged(t, "abc")
	opts.StatementTimeout = time.Hour
	opts.MigrationTimeout = time.Hour

	ctx, cancel := context.WithCancel(t.Context())
	stopping := time.AfterFunc(20*time.Millisecond, cancel)
	defer stopping.Stop()

	_, err := Apply(ctx, conn, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply returned %v, want the cancellation", err)
	}

	var stopped *TimeoutError
	if errors.As(err, &stopped) {
		t.Errorf("an operator's interrupt was reported as %s", stopped.Bound)
	}

	t.Log("Ctrl-C and a bound both end the context. Only the cause says which, and only a bound")
	t.Log("names a key to raise.")
}

func TestAChunkPastTheStatementBoundIsStopped(t *testing.T) {
	opts, _, _, table := chunked(t, 10, 4)
	table.slowAt = 2
	opts.StatementTimeout = 20 * time.Millisecond

	_, err := Apply(t.Context(), table.session, opts)

	var stopped *TimeoutError
	if !errors.As(err, &stopped) || stopped.Bound != BoundStatement {
		t.Fatalf("Apply returned %v, want the statement bound named", err)
	}

	var chunk *ChunkError
	if !errors.As(err, &chunk) || chunk.Chunk != 2 {
		t.Errorf("the failure does not name chunk 2: %v", err)
	}
}

func TestNoBoundMeansNoDeadline(t *testing.T) {
	ctx, cancel := bounded(t.Context(), 0, errStatementTimeout)
	defer cancel()

	if _, has := ctx.Deadline(); has {
		t.Error("a zero bound set a deadline")
	}
}
