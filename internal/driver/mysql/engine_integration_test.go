//go:build integration

package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// runRuntime is what the migrations these tests stage are written in, as a history row records it.
const runRuntime = "sql"

// runStart is the instant staged runs begin at, since internal/engine may not read a clock.
var runStart = time.Date(2026, 9, 27, 15, 30, 0, 0, time.UTC)

// staging hands the executor one migration's operations, and reports no authored undo for its down.
type staging struct {
	ops ir.Operations
}

// Emit returns what the staged migration performs, asking for a derived undo going down.
func (s staging) Emit(_ context.Context, _ migration.Migration, dir ir.Direction, _ engine.Mode,
) (engine.Emitted, error) {
	if dir == ir.Down {
		return engine.Emitted{}, engine.ErrNoDownAuthored
	}

	return engine.Emitted{Operations: s.ops}, nil
}

// compilerOnly compiles operations through this package's own compiler, with no session at all.
type compilerOnly struct{}

// Compile turns one operation into this engine's statements.
func (compilerOnly) Compile(op ir.Operation) ([]driver.Statement, error) {
	return compile(op)
}

// steadyClock returns a clock advancing a fixed step per reading, so a run's duration is never zero.
func steadyClock() engine.Clock {
	at := runStart

	return func() time.Time {
		now := at
		at = at.Add(100 * time.Millisecond)

		return now
	}
}

// staged builds what a run is given from one migration's operations, permitting every destruction.
func staged(t *testing.T, ops ir.Operations) engine.Options {
	t.Helper()

	m, err := migration.Parse("20260927T153000_run_partway.sql")
	if err != nil {
		t.Fatalf("parsing the staged filename: %v", err)
	}

	return engine.Options{
		Planner:    plan.Planner{Compiler: compilerOnly{}},
		Source:     staging{ops: ops},
		Migrations: []history.File{history.NewFile(m, []byte("-- staged\n"))},
		Clock:      steadyClock(),
		Runtime:    func(migration.Migration) string { return runRuntime },
		Allow:      engine.Allowance{All: true},
	}
}

// stopsPartway stages three statements where the second names a table nothing created.
func stopsPartway(t *testing.T, table string) engine.Options {
	t.Helper()

	return staged(t, ir.Operations{
		ir.CreateTable{
			Table:      table,
			Columns:    []ir.Column{{Name: "id", Type: mustType(t, "int64")}},
			PrimaryKey: []string{"id"},
		},
		ir.AddColumn{Table: table + "_absent", Column: ir.Column{
			Name: "orphan", Type: mustType(t, "int32"), Nullable: true,
		}},
		ir.AddIndex{Table: table, Name: table + "_idx", Columns: []string{"id"}},
	})
}

// fresh removes what a previous run of this case left behind, since one database remembers.
func fresh(t *testing.T, c *mysqlConn, table string) {
	t.Helper()

	held, ok := c.History().(mysqlHistory)
	if !ok {
		t.Fatalf("History returned %T, not this package's store", c.History())
	}

	dropLater(t, c, table, held.table)
	drop(t.Context(), c, table, held.table)
}

// onlyRun returns the single row a failed invocation left, failing when it left any other number.
func onlyRun(t *testing.T, c *mysqlConn) history.Record {
	t.Helper()

	runs, err := c.History().List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(runs) != 1 {
		t.Fatalf("the history holds %d rows, want the one the failed run is entitled to", len(runs))
	}

	return runs[0]
}

// tableExists reports whether the server still carries a table, through the catalog this engine has.
func tableExists(t *testing.T, c *mysqlConn, table string) bool {
	t.Helper()

	rows, err := c.Query(t.Context(), driver.Query{
		SQL: "SELECT TABLE_NAME FROM information_schema.TABLES " +
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
		Args: []string{table},
	})
	if err != nil {
		t.Fatalf("asking the catalog for %s: %v", table, err)
	}

	return len(rows.Values) == 1
}

func TestAMigrationThatStopsPartwayIsRecordedDirtyAgainstARealServer(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := siblingName(t)
		fresh(t, c, table)

		_, err := engine.Apply(t.Context(), c, stopsPartway(t, table))
		if !errors.Is(err, engine.ErrDirty) {
			t.Fatalf("Apply returned %v, want a failure reporting ErrDirty", err)
		}

		rec := onlyRun(t, c)
		if rec.Outcome != history.Dirty || rec.LastStatement != 1 {
			t.Fatalf("the run was recorded %q at statement %d, want dirty at 1",
				rec.Outcome, rec.LastStatement)
		}

		if !tableExists(t, c, table) {
			t.Error("the table the first statement created is gone, so nothing survived the failure")
			t.Log("A dirty run is dirty because its statements outlived the failure. On this engine")
			t.Log("the CREATE TABLE implicitly committed the transaction the executor opened, so")
			t.Log("the rollback took nothing back with it (ADR 0057).")
		}

		if strings.Contains(rec.Error, "\n") {
			t.Errorf("the row's error spans %d lines:\n%s",
				strings.Count(rec.Error, "\n")+1, rec.Error)
		}

		if !strings.Contains(rec.Error, "statement 2") {
			t.Errorf("the row does not name the statement that failed: %q", rec.Error)
		}

		t.Log("This is the first dirty row any real server has produced by failing. PostgreSQL and")
		t.Log("SQLite both declare transactional DDL, so ADR 0024 could only describe this half.")
	})
}

func TestTheNextRunAgainstThatServerIsRefusedWithTheRowItWrote(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := siblingName(t)
		fresh(t, c, table)

		if _, err := engine.Apply(t.Context(), c, stopsPartway(t, table)); err == nil {
			t.Fatal("Apply succeeded over a statement the server refuses")
		}

		rec := onlyRun(t, c)

		for name, run := range map[string]func() error{
			"Apply": func() error {
				_, err := engine.Apply(t.Context(), c, stopsPartway(t, table))

				return err
			},
			"Rollback": func() error {
				_, err := engine.Rollback(t.Context(), c, stopsPartway(t, table))

				return err
			},
		} {
			err := run()

			var between *engine.DirtyStateError
			if !errors.As(err, &between) {
				t.Errorf("%s returned %T, want the refusal a dirty database gets", name, err)

				continue
			}

			if between.LastStatement != rec.LastStatement || between.Cause != rec.Error {
				t.Errorf("%s reports statement %d and %q, want the row's %d and %q",
					name, between.LastStatement, between.Cause, rec.LastStatement, rec.Error)
			}

			if errors.As(err, new(*engine.DirtyError)) {
				t.Errorf("%s reads the refusal as a run that just became dirty", name)
			}

			if !strings.Contains(err.Error(), "statement 1 of the up run") {
				t.Errorf("%s does not say what survived:\n%v", name, err)
			}
		}

		if after := onlyRun(t, c); after != rec {
			t.Error("a refused run wrote a history row of its own, so the refusal is not a report")
		}
	})
}

func TestAFirstStatementFailureIsFailedRatherThanDirtyOnAnEngineThatAutoCommits(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := siblingName(t)
		fresh(t, c, table)

		_, err := engine.Apply(t.Context(), c, staged(t, ir.Operations{
			ir.AddColumn{Table: table + "_absent", Column: ir.Column{
				Name: "orphan", Type: mustType(t, "int32"), Nullable: true,
			}},
			ir.DropTable{Table: table + "_absent"},
		}))
		if err == nil {
			t.Fatal("Apply succeeded over a statement the server refuses")
		}

		if errors.Is(err, engine.ErrDirty) {
			t.Errorf("a run where nothing succeeded reports itself dirty: %v", err)
		}

		if rec := onlyRun(t, c); rec.Outcome != history.Failed || rec.LastStatement != 0 {
			t.Errorf("the run was recorded %q at statement %d, want failed at none",
				rec.Outcome, rec.LastStatement)
			t.Log("An engine whose DDL auto-commits still leaves the schema untouched when the")
			t.Log("first statement is the one refused, and Record.Validate refuses a dirty row")
			t.Log("that names no statement.")
		}

		_, next := engine.Apply(t.Context(), c, stopsPartway(t, table))
		if !errors.Is(next, engine.ErrDirty) {
			t.Errorf("the next Apply returned %v, want a run that reached the database", next)
			t.Log("A failed run leaves the migration pending, so the next up proceeds -- and this")
			t.Log("one proceeds far enough to go dirty. Refusing here would be the dirty rule")
			t.Log("applied to a run that changed nothing.")
		}
	})
}

// settles asks for one repair against a server, failing the test rather than returning the refusal.
func settles(t *testing.T, c *mysqlConn, opts engine.Options, ask engine.Repair) engine.Resolution {
	t.Helper()

	settled, err := engine.Resolve(t.Context(), c, opts, ask)
	if err != nil {
		t.Fatalf("Resolve %v: %v", ask.Mode, err)
	}

	return settled
}

// indexed reports whether a table carries one index, which is what the resumed statement creates.
func indexed(t *testing.T, c *mysqlConn, table, index string) bool {
	t.Helper()

	rows, err := c.Query(t.Context(), driver.Query{
		SQL: "SELECT INDEX_NAME FROM information_schema.STATISTICS " +
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?",
		Args: []string{table, index},
	})
	if err != nil {
		t.Fatalf("asking the catalog for %s: %v", index, err)
	}

	return len(rows.Values) == 1
}

func TestResumingADirtyRunAgainstARealServerFinishesTheMigration(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := siblingName(t)
		fresh(t, c, table)

		opts := stopsPartway(t, table)

		if _, err := engine.Apply(t.Context(), c, opts); !errors.Is(err, engine.ErrDirty) {
			t.Fatalf("Apply returned %v, want a failure reporting ErrDirty", err)
		}

		exec(t, c, "CREATE TABLE "+quote(table+"_absent")+" (id bigint)")
		dropLater(t, c, table+"_absent")

		settled := settles(t, c, opts, engine.Repair{Mode: engine.RepairResume})

		if settled.Stopped.LastStatement != 1 || settled.Pending() != 2 {
			t.Fatalf("the resume reports %d pending from statement %d, want 2 from 1",
				settled.Pending(), settled.Stopped.LastStatement)
		}

		if !indexed(t, c, table, table+"_idx") {
			t.Error("the statement the stopped run never reached is still not in the catalog")
			t.Log("This is the half M9-T5 could not reach: a real server driven into a dirty state")
			t.Log("and back out of it, with the catalog saying every statement landed.")
		}

		if state, rec := reconciledState(t, c, opts); state != history.StateApplied ||
			rec.Outcome != history.Applied {
			t.Errorf("the migration reconciled to %q anchored on a %q run, want applied on applied",
				state, rec.Outcome)
		}

		if _, err := engine.Apply(t.Context(), c, opts); err != nil {
			t.Errorf("an apply over the resolved database failed: %v", err)
			t.Log("Nothing is pending and nothing is between states, so there is nothing to do.")
		}
	})
}

func TestAnAssertedRepairAgainstARealServerRecordsTheFourthOutcome(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := siblingName(t)
		fresh(t, c, table)

		opts := stopsPartway(t, table)

		if _, err := engine.Apply(t.Context(), c, opts); err == nil {
			t.Fatal("Apply succeeded over a statement the server refuses")
		}

		settles(t, c, opts, engine.Repair{
			Mode: engine.RepairRolledBack, Reason: "dropped the table by hand",
		})

		runs, err := c.History().List(t.Context())
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		last := runs[len(runs)-1]
		if last.Outcome != history.Resolved || last.Error != "dropped the table by hand" {
			t.Fatalf("the row reads %q / %q, want resolved and the reason", last.Outcome, last.Error)
		}

		if last.Direction != ir.Down {
			t.Errorf("the row records a %q run, want the direction the operator asserted", last.Direction)
		}

		if state, _ := reconciledState(t, c, opts); state != history.StatePending {
			t.Errorf("the migration reconciled to %q, want pending", state)
		}

		t.Log("A fourth outcome survives the string(16) column and the read path on both servers.")
		t.Log("An older migr meeting one reports history.ErrCorrupt naming the column and the")
		t.Log("value, which is the designed answer to a database a newer build has resolved.")
	})
}

// reconciledState folds what this server now holds against the one migration these tests stage.
func reconciledState(t *testing.T, c *mysqlConn, opts engine.Options) (history.State, history.Record) {
	t.Helper()

	runs, err := c.History().List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	statuses, present := history.Reconcile(opts.Migrations, history.NewLog(runs)).Resolved()
	if !present || len(statuses) != 1 {
		t.Fatalf("the report holds %d statuses (present %t)", len(statuses), present)
	}

	return statuses[0].State, statuses[0].Recorded
}
