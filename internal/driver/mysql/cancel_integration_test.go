//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// cancelBound is how long a statement runs before its context ends, in every case here.
const cancelBound = 500 * time.Millisecond

// threadBusy reports whether a connection is still running a statement, as the server's processlist says.
func threadBusy(t *testing.T, s server, id int64) bool {
	t.Helper()

	var command string

	err := pinned(t, s).QueryRowContext(t.Context(), "SELECT COMMAND FROM information_schema.PROCESSLIST "+
		"WHERE ID = ?", id).Scan(&command)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}

	if err != nil {
		t.Fatalf("reading the thread's state: %v", err)
	}

	return command == "Query"
}

// threadOf asks a connection which thread it is, which is what KILL QUERY names.
func threadOf(t *testing.T, conn *sql.Conn) int64 {
	t.Helper()

	var id int64
	if err := conn.QueryRowContext(t.Context(), "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		t.Fatalf("reading the connection id: %v", err)
	}

	return id
}

func TestAContextCancelLeftToTheLibraryLeavesTheStatementOnTheServer(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, _ *mysqlConn) {
		table := uniqueName(t) + "_t"
		contended(t, s, table)
		holder := holdTable(t, s, table)

		conn := pinned(t, s)
		id := threadOf(t, conn)

		if _, err := conn.ExecContext(t.Context(), "SET SESSION lock_wait_timeout = 30"); err != nil {
			t.Fatalf("setting the wait: %v", err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), cancelBound)
		defer cancel()

		if _, err := conn.ExecContext(ctx, "ALTER TABLE "+quote(table)+
			" ADD COLUMN `canceled` int NULL"); err == nil {
			t.Fatal("the ALTER got its lock while the holder held the table")
		}

		busy := threadBusy(t, s, id)
		alive := conn.PingContext(t.Context()) == nil
		_ = holder.Rollback()

		if !busy {
			t.Fatal("the server had already stopped the statement when the library returned, so it " +
				"does not show the failure mode this task exists to prevent")
		}

		if alive {
			t.Fatal("the library kept its connection through a cancel, so the session the migration " +
				"lock belongs to was never at risk")
		}
	})

	t.Log("The control, and the baseline M13-T3 measured: go-sql-driver answers a canceled context")
	t.Log("by closing its connection, and the server is not told. MySQL 8.0 left the ALTER running")
	t.Log("for the whole of its lock_wait_timeout -- still queued, still holding up every reader --")
	t.Log("while MariaDB 11.4 polls a waiting client and stopped it about 650ms later. On both the")
	t.Log("session that held migr's lock was gone.")
}

func TestACanceledStatementIsKilledOnTheServerAndTheSessionCarriesOn(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, _ *mysqlConn) {
		table := uniqueName(t) + "_t"
		c := patientSession(t, s, table)
		holder := holdTable(t, s, table)

		ctx, cancel := context.WithTimeout(t.Context(), cancelBound)
		defer cancel()

		err := c.Transact(ctx, func(ctx context.Context) error {
			return c.Exec(ctx, addStatement(t, table))
		})
		if err == nil {
			t.Fatal("the ALTER got its lock while the holder held the table")
		}

		if threadBusy(t, s, c.id) {
			t.Error("the thread is still running the statement its context gave up on")
		}

		_ = holder.Rollback()

		if err := c.Exec(t.Context(), driver.Statement{SQL: "SELECT 1"}); err != nil {
			t.Errorf("the session is unusable after the cancel: %v", err)
		}

		t.Logf("the canceled statement reported: %v", err)
	})

	t.Log("The session sends the library a context that cannot be canceled, and on the real one")
	t.Log("ending it sends KILL QUERY from a second connection, waiting for the statement to come")
	t.Log("back with 1317 -- so the connection, and the GET_LOCK it holds, survive.")
}

func TestAStatementPastStatementTimeoutIsKilledOnTheServer(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, _ *mysqlConn) {
		table := uniqueName(t) + "_t"
		c := patientSession(t, s, table)
		holdTable(t, s, table)

		opts := retried(t, 0, addedTo(t, table))
		opts.StatementTimeout = cancelBound

		_, err := engine.Apply(t.Context(), c, opts)

		var stopped *engine.TimeoutError
		if !errors.As(err, &stopped) || stopped.Bound != engine.BoundStatement {
			t.Fatalf("Apply returned %v, want the statement bound", err)
		}

		if threadBusy(t, s, c.id) {
			t.Error("the statement the bound stopped is still on the server")
		}

		if row := onlyRun(t, c); row.Outcome != history.Failed {
			t.Errorf("the history holds a %s row, want the stopped migration recorded failed", row.Outcome)
		}
	})
}

// patientSession creates the table and opens a migration session whose statements wait far past any bound here.
func patientSession(t *testing.T, s server, table string) *mysqlConn {
	t.Helper()

	c := openServer(t, s, driver.Options{HistoryTable: uniqueName(t), StatementLockWait: 30 * time.Second})
	fresh(t, c, table)
	apply(t, c, addedTable(t, table))

	return c
}

// addedTable is the table the cancel cases alter, keyed so every probe has something to read.
func addedTable(t *testing.T, table string) ir.CreateTable {
	t.Helper()

	return ir.CreateTable{Table: table, Columns: []ir.Column{{Name: "id", Type: liveType(t, "int64")}},
		PrimaryKey: []string{"id"}}
}

// addStatement compiles the one ALTER the cancel cases run, so no case composes it by hand.
func addStatement(t *testing.T, table string) driver.Statement {
	t.Helper()

	stmts, err := compile(addedTo(t, table))
	if err != nil || len(stmts) != 1 {
		t.Fatalf("compiling the ALTER: %v (%d statements)", err, len(stmts))
	}

	return stmts[0]
}

func TestAUserWithoutPROCESSCanStillKillItsOwnStatement(t *testing.T) {
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			table := uniqueName(t) + "_t"
			patientSession(t, s, table)

			conn := openUnprivileged(t, s)
			plain, ok := conn.(*mysqlConn)
			if !ok {
				t.Fatalf("Open returned %T", conn)
			}

			holder := holdTable(t, s, table)

			ctx, cancel := context.WithTimeout(t.Context(), cancelBound)
			defer cancel()

			err := plain.Exec(ctx, addStatement(t, table))
			if errors.Is(err, ErrKill) {
				t.Fatalf("the kill was refused for a user's own thread: %v", err)
			}

			if threadBusy(t, s, plain.id) {
				t.Error("the statement is still running after its own user killed it")
			}

			_ = holder.Rollback()
		})
	}

	t.Log("KILL QUERY on a thread of one's own needs no privilege at all -- CONNECTION_ADMIN or")
	t.Log("SUPER is for other users' threads -- so migr needs no grant a deployment lacks to stop")
	t.Log("what it started.")
}

func TestACanceledRunLeavesTheMigrationLockFreeAndTheStatementGone(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, _ *mysqlConn) {
		table := uniqueName(t) + "_t"
		c := patientSession(t, s, table)
		holder := holdTable(t, s, table)

		ctx, cancel := context.WithTimeout(t.Context(), cancelBound)
		defer cancel()

		_, err := engine.Apply(ctx, c, retried(t, 0, addedTo(t, table)))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Apply returned %v, want the cancellation", err)
		}

		if threadBusy(t, s, c.id) {
			t.Error("the canceled run's statement is still on the server")
		}

		if got := heldBy(t, c, c.lock); got != "free" {
			t.Errorf("the migration lock is held by %s after the run was canceled", got)
		}

		if row := onlyRun(t, c); row.Outcome != history.Failed {
			t.Errorf("the history holds a %s row, want the canceled run recorded failed", row.Outcome)
		}

		_ = holder.Rollback()
	})

	t.Log("A canceled run -- Ctrl-C, SIGTERM -- reaches this as a context ending, which is the path")
	t.Log("the operator takes most. Before M13-T3 it closed the session: the ALTER kept waiting on")
	t.Log("the server, and the run could not record its own failure.")
}

func TestARebuildKilledPartwayLeavesTheTableAsItWas(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, _ *mysqlConn) {
		table := uniqueName(t) + "_t"
		c := patientSession(t, s, table)

		exec(t, c, "ALTER TABLE "+quote(table)+" ADD COLUMN `pad` varchar(200) NULL")

		if c.version.variant != MariaDB {
			exec(t, c, "SET SESSION cte_max_recursion_depth = 400001")
		}

		source := "(WITH RECURSIVE `q`(`i`) AS (SELECT 1 UNION ALL SELECT `i`+1 FROM `q` WHERE `i` < 400000) " +
			"SELECT `i` FROM `q`) AS `s`"
		if c.version.variant == MariaDB {
			source = "(SELECT `seq` AS `i` FROM seq_1_to_400000) AS `s`"
		}

		exec(t, c, "INSERT INTO "+quote(table)+" (`id`, `pad`) SELECT `i`, repeat('x', 200) FROM "+source)

		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()

		err := c.Exec(ctx, driver.Statement{SQL: "ALTER TABLE " + quote(table) + " FORCE, ALGORITHM=COPY"})
		if err == nil {
			t.Fatal("the rebuild of 400000 rows finished inside 200ms, so nothing was interrupted " +
				"and the assertions below would hold for the wrong reason")
		}

		if got := read(t, c, "SELECT count(*) FROM "+quote(table))[0]; got != "400000" {
			t.Errorf("the table holds %s rows after an interrupted rebuild, want 400000", got)
		}

		leftovers := read(t, c, "SELECT count(*) FROM information_schema.TABLES "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME LIKE '#sql%'")[0]
		if leftovers != "0" {
			t.Errorf("an interrupted rebuild left %s temporary tables behind", leftovers)
		}

		t.Logf("the interrupted rebuild reported: %v", err)
	})

	t.Log("MySQL 8's DDL is atomic and MariaDB's copy works in a temporary table it drops when")
	t.Log("killed: measured, an ALTER ... FORCE stopped partway left the table whole and nothing")
	t.Log("beside it.")
}
