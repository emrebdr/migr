//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
)

// retryLockWait is safety.lock_timeout for these runs, short enough that four attempts fit in seconds.
const retryLockWait = 200 * time.Millisecond

// readerMargin is what a reader may take beyond one lock wait, which scheduling and a container cost.
const readerMargin = 300 * time.Millisecond

// A read is late when a timer armed for the bound fires before its answer arrives. That needs no
// clock, and forbidigo leaves a driver's tests none, for invariant #8's reason.

// reader runs one query in a loop on a connection of its own and counts the answers that came late.
type reader struct {
	bound time.Duration
	mu    sync.Mutex
	late  int
	reads int
	err   error
	stop  context.CancelFunc
	done  chan struct{}
}

// startReader begins reading a table as an application would, on a session with no lock bound at all.
func startReader(t *testing.T, dsn, table string, bound time.Duration) *reader {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting the reader: %v", err)
	}

	ctx, stop := context.WithCancel(context.WithoutCancel(t.Context()))
	r := &reader{bound: bound, stop: stop, done: make(chan struct{})}

	go r.loop(ctx, conn, "SELECT count(*) FROM "+table)

	t.Cleanup(func() {
		r.finish()
		_ = conn.Close(context.WithoutCancel(t.Context()))
	})

	return r
}

// loop reads until it is stopped, timing every query from send to answer.
func (r *reader) loop(ctx context.Context, conn *pgx.Conn, query string) {
	defer close(r.done)

	for ctx.Err() == nil {
		var n int64

		late, err := timedRead(r.bound, func() error { return conn.QueryRow(ctx, query).Scan(&n) })
		if err != nil {
			if ctx.Err() == nil {
				r.note(false, err)
			}

			return
		}

		r.note(late, nil)
	}
}

// note records one read and whether it came back past the bound, or the error that ended the loop.
func (r *reader) note(late bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err != nil {
		r.err = err

		return
	}

	r.reads++

	if late {
		r.late++
	}
}

// finish stops the loop and waits for the query in flight to come back.
func (r *reader) finish() (int, int, error) {
	r.stop()
	<-r.done

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.late, r.reads, r.err
}

// timedRead runs one read and reports whether it came back past the bound, which a timer decides.
func timedRead(bound time.Duration, read func() error) (bool, error) {
	var late atomic.Bool

	timer := time.AfterFunc(bound, func() { late.Store(true) })
	err := read()
	timer.Stop()

	return late.Load(), err
}

// holdTable opens a transaction that reads the table and keeps it open, as a long report would.
func holdTable(t *testing.T, dsn, table string) pgx.Tx {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting the holder: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })

	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatalf("beginning the holder: %v", err)
	}

	var n int64
	if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("taking the holder's lock: %v", err)
	}

	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) })

	return tx
}

// contended stages a schema whose accounts table exists, and a migration session bounded to retryLockWait.
func contended(t *testing.T, dsn string) (*pgConn, string) {
	t.Helper()

	target := scratchSchema(t, dsn)

	if _, err := engine.Apply(t.Context(), queueSession(t, dsn, target),
		queueOptions(t, createAccounts(t))); err != nil {
		t.Fatalf("creating the table: %v", err)
	}

	conn := open(t, dsn, driver.Options{Schema: target, HistoryTable: historyTable,
		StatementLockWait: retryLockWait})

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T", conn)
	}

	return session, quote(target) + ".accounts"
}

// retried builds the run's options with a real backoff and a real sleep, as migr up has.
func retried(t *testing.T, retries int, base time.Duration) engine.Options {
	t.Helper()

	opts := queueOptions(t, createAccounts(t), addNickname(t))
	opts.Backoff = engine.Exponential{Base: base, Cap: time.Second, Retries: retries}

	return opts
}

func TestReadersAreNotQueuedBehindAnAlterThatCannotGetItsLock(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			session, table := contended(t, dsn)

			holder := holdTable(t, dsn, table)
			r := startReader(t, dsn, table, retryLockWait+readerMargin)

			_, err := engine.Apply(t.Context(), session, retried(t, 3, 300*time.Millisecond))

			late, reads, readErr := r.finish()
			_ = holder.Rollback(t.Context())

			var exhausted *engine.LockRetryError
			if !errors.As(err, &exhausted) || exhausted.Attempts != 4 {
				t.Fatalf("Apply returned %v, want the lock still held after four attempts", err)
			}

			if !errors.Is(err, driver.ErrStatementLockTimeout) {
				t.Errorf("the failure no longer reaches the lock timeout: %v", err)
			}

			if readErr != nil {
				t.Fatalf("the reader failed: %v", readErr)
			}

			if late > 0 {
				t.Errorf("%d of %d reads waited past the %s one lock wait allows", late, reads,
					retryLockWait+readerMargin)
			}

			if reads < 10 {
				t.Errorf("the reader answered %d times in the whole run, so it was not reading", reads)
			}

			if live := accountColumns(t, session); len(live) != 1 {
				t.Errorf("accounts has %v after a run that applied nothing", live)
			}

			t.Logf("%d reads, none past %s, across four attempts and their backoff", reads,
				retryLockWait+readerMargin)
		})
	}

	t.Log("The ALTER queues behind the holder and every later reader queues behind the ALTER, so a")
	t.Log("reader waits as long as the ALTER does. lock_timeout bounds that wait, and backing off")
	t.Log("between attempts is what lets the queue behind the holder drain rather than refill.")
}

func TestWithoutABoundAReaderQueuesForAsLongAsTheAlterWaits(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			_, table := contended(t, dsn)

			holder := holdTable(t, dsn, table)
			failed := alterWaiting(t, dsn, "SET lock_timeout = '3s'; ALTER TABLE "+table+
				" ADD COLUMN unbounded integer")

			late := probeRead(t, dsn, table, 1500*time.Millisecond)
			err := <-failed
			_ = holder.Rollback(t.Context())

			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != lockNotAvailable {
				t.Fatalf("the ALTER ended with %v, want its own lock timeout", err)
			}

			if !late {
				t.Error("a read issued while the ALTER waited came back inside 1.5s of a 3s wait, so " +
					"this instrument cannot see a pileup and the test above proves nothing")
			}
		})
	}

	t.Log("The control: the same holder, with the ALTER waiting 3s instead of 200ms, and a read sent")
	t.Log("only once the server reports the ALTER waiting on its lock. That read queues behind it --")
	t.Log("which is the outage a lock timeout bounds, measured rather than claimed.")
}

// alterWaiting starts a statement on a session of its own and returns once the server shows it waiting.
func alterWaiting(t *testing.T, dsn, sql string) <-chan error {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })

	failed := make(chan error, 1)

	go func() {
		_, err := conn.Exec(context.WithoutCancel(t.Context()), sql)
		failed <- err
	}()

	watcher, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting the watcher: %v", err)
	}

	defer func() { _ = watcher.Close(context.WithoutCancel(t.Context())) }()

	for range 250 {
		var waiting int
		if err := watcher.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity "+
			"WHERE wait_event_type = 'Lock' AND query LIKE '%ADD COLUMN unbounded%' "+
			"AND pid <> pg_backend_pid()").Scan(&waiting); err != nil {
			t.Fatalf("asking whether the ALTER waits: %v", err)
		}

		if waiting > 0 {
			return failed
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("the ALTER never waited on a lock, so there is nothing for a reader to queue behind")

	return nil
}

// probeRead sends one read on a session of its own and reports whether it came back past the bound.
func probeRead(t *testing.T, dsn, table string, bound time.Duration) bool {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting the probe: %v", err)
	}

	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()

	var n int64

	late, err := timedRead(bound, func() error {
		return conn.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n)
	})
	if err != nil {
		t.Fatalf("the probe read failed: %v", err)
	}

	return late
}

func TestAnAlterThatGetsItsLockOnARetryApplies(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			session, table := contended(t, dsn)

			holder := holdTable(t, dsn, table)

			released := time.AfterFunc(350*time.Millisecond, func() {
				_ = holder.Rollback(context.WithoutCancel(t.Context()))
			})
			defer released.Stop()

			result, err := engine.Apply(t.Context(), session, retried(t, 5, 300*time.Millisecond))
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if len(result.Runs) != 1 || result.Runs[0].Retries < 1 {
				t.Fatalf("the run reports %+v, want one migration applied after a retry", result.Runs)
			}

			if live := accountColumns(t, session); len(live) != 2 {
				t.Errorf("accounts has %v, want the nickname added once", live)
			}
		})
	}
}

// accountColumns names the accounts table's columns as this session's snapshot reads them.
func accountColumns(t *testing.T, session *pgConn) []string {
	t.Helper()

	snapshot, err := session.Introspect(t.Context())
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}

	for _, found := range snapshot.Tables {
		if found.Name != "accounts" {
			continue
		}

		names := make([]string, 0, len(found.Columns))
		for _, c := range found.Columns {
			names = append(names, c.Name)
		}

		return names
	}

	return nil
}
