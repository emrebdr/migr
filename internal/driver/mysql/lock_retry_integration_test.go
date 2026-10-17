//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
)

// retryLockWait is safety.lock_timeout for these runs: lock_wait_timeout counts whole seconds.
const retryLockWait = time.Second

// readerMargin is what a reader may take beyond one lock wait, which scheduling and a container cost.
const readerMargin = 500 * time.Millisecond

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

// pinned opens one connection outside this driver, as an application's would be.
func pinned(t *testing.T, s server) *sql.Conn {
	t.Helper()

	db, err := sql.Open("mysql", s.dsnFor())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// startReader begins reading a table as an application would, on a session of its own.
func startReader(t *testing.T, s server, table string, bound time.Duration) *reader {
	t.Helper()

	conn := pinned(t, s)
	ctx, stop := context.WithCancel(context.WithoutCancel(t.Context()))
	r := &reader{bound: bound, stop: stop, done: make(chan struct{})}

	go r.loop(ctx, conn, "SELECT count(*) FROM "+quote(table))

	t.Cleanup(func() { r.finish() })

	return r
}

// loop reads until it is stopped, timing every query from send to answer.
func (r *reader) loop(ctx context.Context, conn *sql.Conn, query string) {
	defer close(r.done)

	for ctx.Err() == nil {
		var n int64

		late, err := timedRead(r.bound, func() error {
			return conn.QueryRowContext(ctx, query).Scan(&n)
		})
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

// holdTable opens a transaction that reads the table and keeps it open, which holds its metadata lock.
func holdTable(t *testing.T, s server, table string) *sql.Tx {
	t.Helper()

	tx, err := pinned(t, s).BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("beginning the holder: %v", err)
	}

	var n int64
	if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+quote(table)).Scan(&n); err != nil {
		t.Fatalf("taking the holder's lock: %v", err)
	}

	t.Cleanup(func() { _ = tx.Rollback() })

	return tx
}

// contended creates the table the run alters and opens a migration session bounded to retryLockWait.
func contended(t *testing.T, s server, tables ...string) *mysqlConn {
	t.Helper()

	c := openServer(t, s, driver.Options{HistoryTable: uniqueName(t), StatementLockWait: retryLockWait})

	for _, table := range tables {
		fresh(t, c, table)
		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
		}, PrimaryKey: []string{"id"}})
	}

	return c
}

// addedTo is an add_column on one table, which takes that table's exclusive metadata lock.
func addedTo(t *testing.T, table string) ir.AddColumn {
	t.Helper()

	return ir.AddColumn{Table: table, Column: ir.Column{Name: "added", Type: liveType(t, "int32"),
		Nullable: true}}
}

// retried builds the run's options with a real backoff and a real sleep, as migr up has.
func retried(t *testing.T, retries int, ops ...ir.Operation) engine.Options {
	t.Helper()

	opts := staged(t, ops)
	opts.Backoff = engine.Exponential{Base: 300 * time.Millisecond, Cap: time.Second, Retries: retries}

	return opts
}

// columnCount counts a table's columns through the catalog this engine has.
func columnCount(t *testing.T, c *mysqlConn, table string) int {
	t.Helper()

	rows, err := c.Query(t.Context(), driver.Query{
		SQL: "SELECT COLUMN_NAME FROM information_schema.COLUMNS " +
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
		Args: []string{table},
	})
	if err != nil {
		t.Fatalf("reading %s's columns: %v", table, err)
	}

	return len(rows.Values)
}

func TestReadersAreNotQueuedBehindAnAlterThatCannotGetItsLock(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, _ *mysqlConn) {
		table := uniqueName(t) + "_t"
		c := contended(t, s, table)

		holder := holdTable(t, s, table)
		r := startReader(t, s, table, retryLockWait+readerMargin)

		_, err := engine.Apply(t.Context(), c, retried(t, 3, addedTo(t, table)))

		late, reads, readErr := r.finish()
		_ = holder.Rollback()

		var exhausted *engine.LockRetryError
		if !errors.As(err, &exhausted) || exhausted.Attempts != 4 {
			t.Fatalf("Apply returned %v, want the lock still held after four attempts", err)
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

		if got := columnCount(t, c, table); got != 1 {
			t.Errorf("%s has %d columns after a run that applied nothing", table, got)
		}

		t.Logf("%d reads, none past %s, across four attempts and their backoff", reads,
			retryLockWait+readerMargin)
	})

	t.Log("A waiting ALTER holds a pending exclusive metadata lock, and every later reader queues")
	t.Log("behind it -- MySQL's version of the pileup. lock_wait_timeout bounds the wait.")
}

func TestWithoutABoundAReaderQueuesForAsLongAsTheAlterWaits(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, _ *mysqlConn) {
		table := uniqueName(t) + "_t"
		contended(t, s, table)

		holder := holdTable(t, s, table)
		failed := alterWaiting(t, s, "ALTER TABLE "+quote(table)+" ADD COLUMN `unbounded` int NULL")

		late := probeRead(t, s, table, 2*time.Second)
		err := <-failed
		_ = holder.Rollback()

		if !hasNumber(err, errLockWaitTimeout) {
			t.Fatalf("the ALTER ended with %v, want its own lock wait timeout", err)
		}

		if !late {
			t.Error("a read issued while the ALTER waited came back inside 2s of a 4s wait, so this " +
				"instrument cannot see a pileup and the test above proves nothing")
		}
	})

	t.Log("The control: the same holder, with the ALTER waiting 4s instead of 1s, and a read sent")
	t.Log("only once the server reports the ALTER waiting for its metadata lock. That read queues")
	t.Log("behind the pending exclusive lock -- the outage lock_wait_timeout bounds.")
}

// alterWaiting starts a statement under a 4s lock wait and returns once the server shows it waiting.
func alterWaiting(t *testing.T, s server, sql string) <-chan error {
	t.Helper()

	conn := pinned(t, s)

	if _, err := conn.ExecContext(t.Context(), "SET SESSION lock_wait_timeout = 4"); err != nil {
		t.Fatalf("setting the wait: %v", err)
	}

	failed := make(chan error, 1)

	go func() {
		_, err := conn.ExecContext(context.WithoutCancel(t.Context()), sql)
		failed <- err
	}()

	watcher := pinned(t, s)

	for range 250 {
		var waiting int
		if err := watcher.QueryRowContext(t.Context(), "SELECT count(*) FROM "+
			"information_schema.PROCESSLIST WHERE STATE = 'Waiting for table metadata lock' "+
			"AND INFO LIKE '%ADD COLUMN `unbounded`%'").Scan(&waiting); err != nil {
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
func probeRead(t *testing.T, s server, table string, bound time.Duration) bool {
	t.Helper()

	conn := pinned(t, s)

	var n int64

	late, err := timedRead(bound, func() error {
		return conn.QueryRowContext(t.Context(), "SELECT count(*) FROM "+quote(table)).Scan(&n)
	})
	if err != nil {
		t.Fatalf("the probe read failed: %v", err)
	}

	return late
}

func TestAnAlterThatGetsItsLockOnARetryApplies(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, _ *mysqlConn) {
		table := uniqueName(t) + "_t"
		c := contended(t, s, table)

		holder := holdTable(t, s, table)
		released := time.AfterFunc(1300*time.Millisecond, func() { _ = holder.Rollback() })
		defer released.Stop()

		result, err := engine.Apply(t.Context(), c, retried(t, 5, addedTo(t, table)))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if len(result.Runs) != 1 || result.Runs[0].Retries < 1 {
			t.Fatalf("the run reports %+v, want one migration applied after a retry", result.Runs)
		}

		if got := columnCount(t, c, table); got != 2 {
			t.Errorf("%s has %d columns, want the added one once", table, got)
		}
	})
}

func TestARetryStartsAtTheStatementThatWaitedSinceTheOnesBeforeItCommitted(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, _ *mysqlConn) {
		free, held := uniqueName(t)+"_f", uniqueName(t)+"_h"
		c := contended(t, s, free, held)

		holder := holdTable(t, s, held)
		released := time.AfterFunc(1300*time.Millisecond, func() { _ = holder.Rollback() })
		defer released.Stop()

		result, err := engine.Apply(t.Context(), c, retried(t, 5, addedTo(t, free), addedTo(t, held)))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if result.Runs[0].Retries < 1 || result.Runs[0].Statements != 2 {
			t.Errorf("the run reports %+v, want two statements after at least one retry", result.Runs[0])
		}

		for _, table := range []string{free, held} {
			if got := columnCount(t, c, table); got != 2 {
				t.Errorf("%s has %d columns, want the added one once", table, got)
			}
		}
	})

	t.Log("The first ALTER committed as it ran. Rerunning it would be error 1060, a duplicate")
	t.Log("column, so a retry that started over could not have applied at all.")
}
