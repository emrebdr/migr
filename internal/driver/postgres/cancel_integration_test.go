//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
)

// cancelBound is how long a statement runs before its context ends, in every case here.
const cancelBound = 300 * time.Millisecond

// backendBusy reports whether a backend is still running or waiting on a statement, as the server says.
func backendBusy(t *testing.T, dsn string, pid uint32) bool {
	t.Helper()

	watcher, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting the watcher: %v", err)
	}

	defer func() { _ = watcher.Close(context.WithoutCancel(t.Context())) }()

	var state string

	err = watcher.QueryRow(t.Context(), "SELECT coalesce(state, '') FROM pg_stat_activity WHERE pid = $1",
		int32(pid)).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}

	if err != nil {
		t.Fatalf("reading the backend's state: %v", err)
	}

	return state == "active"
}

// blockedAlter is the statement every case runs, which waits on the holder for far longer than its bound.
func blockedAlter(table string) string {
	return "ALTER TABLE " + table + " ADD COLUMN canceled integer"
}

func TestAContextCancelLeftToTheLibraryEndsTheSession(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			_, table := contended(t, dsn)
			holdTable(t, dsn, table)

			conn, err := pgx.Connect(t.Context(), dsn)
			if err != nil {
				t.Fatalf("connecting: %v", err)
			}

			defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()

			if _, err := conn.Exec(t.Context(), "SET lock_timeout = '30s'"); err != nil {
				t.Fatalf("setting the wait: %v", err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), cancelBound)
			defer cancel()

			if _, err := conn.Exec(ctx, blockedAlter(table)); err == nil {
				t.Fatal("the ALTER got its lock while the holder held the table")
			}

			if !conn.IsClosed() {
				t.Fatal("the library kept its session through a cancel, so it no longer shows the " +
					"failure this driver's handler exists for and the test of the fix proves nothing")
			}
		})
	}

	t.Log("The control, and the baseline M13-T3 measured -- which was not the one the task names.")
	t.Log("pgx's default handler puts a deadline on its own socket; the dead connection then sends a")
	t.Log("cancel request from a goroutine and closes. So the statement does stop on the server, but")
	t.Log("only after Exec has returned, and the session is gone: a run cannot record its own")
	t.Log("failure on it, and nothing after the cancel can run.")
}

func TestACanceledStatementIsStoppedOnTheServerAndTheSessionCarriesOn(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			session, table, _ := contendedFor(t, dsn, 30*time.Second)
			holder := holdTable(t, dsn, table)
			pid := session.conn.PgConn().PID()

			ctx, cancel := context.WithTimeout(t.Context(), cancelBound)
			defer cancel()

			err := session.Transact(ctx, func(ctx context.Context) error {
				return session.Exec(ctx, driver.Statement{SQL: blockedAlter(table)})
			})
			if err == nil {
				t.Fatal("the ALTER got its lock while the holder held the table")
			}

			if busy := backendBusy(t, dsn, pid); busy {
				t.Error("the backend is still running the statement its context gave up on")
			}

			_ = holder.Rollback(t.Context())

			if err := session.Exec(t.Context(), driver.Statement{SQL: "SELECT 1"}); err != nil {
				t.Errorf("the session is unusable after the cancel: %v", err)
			}

			t.Logf("the canceled statement reported: %v", err)
		})
	}

	t.Log("pgconn's cancel-request handler sends the protocol's cancel request -- pg_cancel_backend")
	t.Log("by another route -- and waits for the backend to answer 57014 before returning, so the")
	t.Log("transaction aborts, Transact rolls it back, and the lock queue behind it moves on.")
}

func TestAStatementPastStatementTimeoutIsStoppedOnTheServer(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			session, table, _ := contendedFor(t, dsn, 30*time.Second)
			holdTable(t, dsn, table)
			pid := session.conn.PgConn().PID()

			opts := retried(t, 0, time.Second)
			opts.StatementTimeout = cancelBound

			_, err := engine.Apply(t.Context(), session, opts)

			var stopped *engine.TimeoutError
			if !errors.As(err, &stopped) || stopped.Bound != engine.BoundStatement {
				t.Fatalf("Apply returned %v, want the statement bound", err)
			}

			if backendBusy(t, dsn, pid) {
				t.Error("the statement the bound stopped is still on the server")
			}

			runs := recordedRuns(t, session)
			if len(runs) < 2 || runs[len(runs)-1].Outcome != history.Failed {
				t.Errorf("the history holds %+v, want the stopped migration recorded failed on the "+
					"session that ran it", runs)
			}
		})
	}
}

func TestACanceledRunLeavesTheMigrationLockFreeAndTheStatementGone(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			session, table, target := contendedFor(t, dsn, 30*time.Second)
			holder := holdTable(t, dsn, table)
			pid := session.conn.PgConn().PID()

			ctx, cancel := context.WithTimeout(t.Context(), cancelBound)
			defer cancel()

			_, err := engine.Apply(ctx, session, retried(t, 0, time.Second))
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Apply returned %v, want the cancellation", err)
			}

			if backendBusy(t, dsn, pid) {
				t.Error("the canceled run's statement is still on the server")
			}

			runs := recordedRuns(t, session)
			if len(runs) < 2 || runs[len(runs)-1].Outcome != history.Failed {
				t.Errorf("the history holds %+v, want the canceled run recorded failed", runs)
			}

			_ = holder.Rollback(t.Context())

			next := open(t, dsn, driver.Options{Schema: target, HistoryTable: historyTable,
				LockWait: time.Second})

			unlock, err := next.Lock(t.Context())
			if err != nil {
				t.Fatalf("another run could not take the migration lock after the cancel: %v", err)
			}

			_ = unlock(context.WithoutCancel(t.Context()))
		})
	}

	t.Log("Before M13-T3 the cancel took the session with it: the run could not record its failure,")
	t.Log("and its advisory lock was released only because the backend went away.")
}
