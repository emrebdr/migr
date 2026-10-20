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

// Both servers refuse a foreign key whose two sides are different types -- error 3780 on MySQL 8.0
// and 1005/150 on MariaDB 11.4, measured -- and the operation set carries no types for either side,
// so ir.AddForeignKey over a mismatch is a statement a pure compiler cannot refuse and no server
// will perform. It is what these fixtures fail at. A table nothing created was what they failed at
// until M11-T2, which engine.Unrestorable now refuses before anything compiles, and these cases are
// about what a failure the *server* produces does to a run.
//
// An index over a text column with no prefix length would have done on MySQL (ADR 0058, error 1170)
// and MariaDB accepts one -- measured here, and the first variant difference these fixtures met.

// refusedKey is the foreign key both servers reject, since its two sides are different types.
func refusedKey(table string) ir.AddForeignKey {
	return ir.AddForeignKey{
		Table: table, Name: table + "_mismatched",
		Columns:         []string{"pid"},
		ReferencesTable: table, ReferencesColumns: []string{"id"},
	}
}

// stopsAfterOne stages two statements against a table that is already there, the second refused.
func stopsAfterOne(t *testing.T, table string) engine.Options {
	t.Helper()

	return staged(t, ir.Operations{
		ir.AddIndex{Table: table, Name: table + "_idx", Columns: []string{"id"}},
		refusedKey(table),
	})
}

// stopsPartway stages three statements where the second is one no server will perform.
func stopsPartway(t *testing.T, table string) engine.Options {
	t.Helper()

	return staged(t, ir.Operations{
		ir.CreateTable{
			Table: table,
			Columns: []ir.Column{
				{Name: "id", Type: mustType(t, "int64")},
				{Name: "pid", Type: mustType(t, "string(36)"), Nullable: true},
			},
			PrimaryKey: []string{"id"},
		},
		refusedKey(table),
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

	dropLater(t, c, table, held.table, history.ProgressTable(held.table), history.ReleaseTable(held.table))
	drop(t.Context(), c, table, held.table, history.ProgressTable(held.table), history.ReleaseTable(held.table))
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

		exec(t, c, "CREATE TABLE "+quote(table)+
			" (id bigint NOT NULL PRIMARY KEY, pid varchar(36))")
		dropLater(t, c, table)

		_, err := engine.Apply(t.Context(), c, staged(t, ir.Operations{
			refusedKey(table),
			ir.AddIndex{Table: table, Name: table + "_idx", Columns: []string{"id"}},
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

		_, next := engine.Apply(t.Context(), c, stopsAfterOne(t, table))
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

		exec(t, c, "ALTER TABLE "+quote(table)+" MODIFY `pid` bigint")

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

// The one operation these two servers answer differently, which both of them compile.
//
// MySQL writes a uuid default into every existing row, so it refuses the statement outright with
// error 1674 while a binary log is written -- which the compose server has on. MariaDB performs it.
// This is the only version- or configuration-dependent refusal any server in test/docker-compose.yml
// can produce, PostgreSQL 16 and 17 both having gen_random_uuid in core (M10-T2).
const defaultedColumn = "token"

// uuidDefaulted stages a table and then the column above, which is one migration of two operations.
func uuidDefaulted(t *testing.T, table string) engine.Options {
	t.Helper()

	return staged(t, ir.Operations{
		ir.CreateTable{
			Table:      table,
			Columns:    []ir.Column{{Name: "id", Type: mustType(t, "int64")}},
			PrimaryKey: []string{"id"},
		},
		ir.AddColumn{Table: table, Column: ir.Column{
			Name: defaultedColumn, Type: mustType(t, "uuid"), Nullable: true,
			Default: &ir.Default{Function: ir.FuncUUID},
		}},
	})
}

func TestTheDeclarationAndTheRunNeverDisagreeAboutWhatThisServerPerforms(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := siblingName(t)
		fresh(t, c, table)

		opts := uuidDefaulted(t, table)

		declared := c.Capabilities().Resolve(ir.AddColumn{Table: table, Column: ir.Column{
			Name: defaultedColumn, Type: mustType(t, "uuid"), Nullable: true,
			Default: &ir.Default{Function: ir.FuncUUID},
		}})

		_, err := engine.Apply(t.Context(), c, opts)

		if declared.Allowed() {
			if err != nil {
				t.Fatalf("this server declares it performs the column and the run failed: %v", err)
			}

			return
		}

		var refusal *engine.UnsupportedError
		if !errors.As(err, &refusal) {
			t.Fatalf("Apply returned %v, want the refusal this server's own declaration asks for", err)
		}

		if len(refusal.Found) != 1 || refusal.Found[0].Reason != declared.Reason() {
			t.Errorf("the refusal carries %+v, want the declaration's own reason", refusal.Found)
		}

		if refusal.Server != c.ServerVersion() || refusal.Found[0].Operation != 2 {
			t.Errorf("the refusal reports server %q at operation %d, want %q at 2",
				refusal.Server, refusal.Found[0].Operation, c.ServerVersion())
		}

		if tableExists(t, c, table) {
			t.Error("the table the first operation creates is in the catalog, so the run reached it")
			t.Log("The gate gets its answer with no I/O at all, before anything compiles -- which is")
			t.Log("what this engine makes load-bearing: its DDL commits, so a run that reached the")
			t.Log("second operation would leave the first one applied and the migration dirty.")
		}

		if runs, listErr := c.History().List(t.Context()); listErr != nil || len(runs) != 0 {
			t.Errorf("the history holds %d rows (err %v), want none for a migration that never ran",
				len(runs), listErr)
		}

		t.Log("Measured before the gate existed: this same run applied the first operation, sent")
		t.Log("the second, and was refused by the server with error 1674 -- leaving the table in")
		t.Log("the catalog and the migration recorded dirty at statement 1, which then refused")
		t.Log("both directions until migr resolve. That is what reading the declaration first buys.")
	})
}

// What M11-T7 changed, measured on the one engine in compose whose DDL commits as it goes.
//
// This driver emulates exactly one operation -- alter_column, which it performs by restating the
// column's whole definition (ADR 0058) -- so two of them in one migration is the shape where an
// expansion rendered from a stale catalog is observable: the second MODIFY restates the nullability
// the catalog holds, which is the one the first MODIFY had just changed.
//
// It is also the only engine that can leave a run dirty with operations still uncompiled, since
// every statement here auto-commits. That is what the two cases after this one are about.

// stagedOver stages a migration the way a run with a session does, so an emulated operation reaches it.
func stagedOver(t *testing.T, c *mysqlConn, ops ir.Operations) engine.Options {
	t.Helper()

	opts := staged(t, ops)
	opts.Planner = plan.Planner{Compiler: compilerOnly{}, Expander: c}

	return opts
}

// tightenedThenRetyped stages a table and then two redefinitions of one column, in that order.
func tightenedThenRetyped(t *testing.T, c *mysqlConn, table string) engine.Options {
	t.Helper()

	narrow, wide := mustType(t, "string(64)"), mustType(t, "string(128)")

	return stagedOver(t, c, ir.Operations{
		ir.CreateTable{
			Table: table,
			Columns: []ir.Column{
				{Name: "id", Type: mustType(t, "int64")},
				{Name: "tag", Type: narrow, Nullable: true},
			},
			PrimaryKey: []string{"id"},
		},
		ir.AlterColumn{Table: table, Column: "tag", Nullable: ptr(false),
			From: &ir.ColumnState{Nullable: ptr(true)}},
		ir.AlterColumn{Table: table, Column: "tag", Type: &wide,
			From: &ir.ColumnState{Type: &narrow}},
	})
}

// ptr returns a pointer to a value, for the operation fields that are tri-state.
func ptr[T any](v T) *T {
	return &v
}

// declaredColumn renders what the catalog holds about one column, which is what a redefinition reads.
func declaredColumn(t *testing.T, c *mysqlConn, table, column string) string {
	t.Helper()

	rows, err := c.Query(t.Context(), driver.Query{
		SQL: "SELECT concat(COLUMN_TYPE, ' ', IS_NULLABLE) FROM information_schema.COLUMNS " +
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?",
		Args: []string{table, column},
	})
	if err != nil {
		t.Fatalf("asking the catalog for %s.%s: %v", table, column, err)
	}

	if len(rows.Values) != 1 || len(rows.Values[0]) != 1 || rows.Values[0][0] == nil {
		t.Fatalf("the catalog answered %v for %s.%s", rows.Values, table, column)
	}

	return *rows.Values[0][0]
}

func TestTwoRedefinitionsOfOneColumnInOneMigrationBothLand(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := siblingName(t)
		fresh(t, c, table)

		if _, err := engine.Apply(t.Context(), c, tightenedThenRetyped(t, c, table)); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if got := declaredColumn(t, c, table, "tag"); got != "varchar(128) NO" {
			t.Errorf("the column reads %q, want varchar(128) and NOT NULL", got)
			t.Log("A redefinition restates every attribute it does not change, and it reads those")
			t.Log("from the catalog. Rendered before the first MODIFY ran, the second one restates")
			t.Log("the nullability the first had just tightened and the NOT NULL is gone.")
		}
	})
}

// retypedAfterARefusal stages a native statement, one no server performs, and an emulated one after.
func retypedAfterARefusal(t *testing.T, c *mysqlConn, table string) engine.Options {
	t.Helper()

	narrow, wide := mustType(t, "string(36)"), mustType(t, "string(128)")

	return stagedOver(t, c, ir.Operations{
		ir.AddIndex{Table: table, Name: table + "_idx", Columns: []string{"id"}},
		refusedKey(table),
		ir.AlterColumn{Table: table, Column: "pid", Type: &wide,
			From: &ir.ColumnState{Type: &narrow}},
	})
}

func TestADirtyRunSaysWhichOfItsOperationsItNeverCompiled(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := siblingName(t)
		fresh(t, c, table)

		exec(t, c, "CREATE TABLE "+quote(table)+
			" (id bigint NOT NULL PRIMARY KEY, pid varchar(36))")
		dropLater(t, c, table)

		_, err := engine.Apply(t.Context(), c, retypedAfterARefusal(t, c, table))

		var became *engine.DirtyError
		if !errors.As(err, &became) {
			t.Fatalf("Apply returned %T, want the run that just became dirty", err)
		}

		if became.Deferred != 1 || became.Statements != 2 {
			t.Fatalf("the failure reports %d statements and %d uncompiled operations, want 2 and 1",
				became.Statements, became.Deferred)
		}

		for _, want := range []string{"the 2 it had compiled", "compiles 1 of this migration's"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the failure does not say %q:\n%v", want, err)
			}
		}

		t.Log("The row holds how far the run got, which is unchanged. What a run can no longer say")
		t.Log("is how far it had to go, because the operations it never reached are compiled from a")
		t.Log("catalog it never read -- so the total is what it had compiled, and it says so.")
	})
}

func TestAResumeIsRefusedWhereAnOperationIsCompiledAsItRuns(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := siblingName(t)
		fresh(t, c, table)

		exec(t, c, "CREATE TABLE "+quote(table)+
			" (id bigint NOT NULL PRIMARY KEY, pid varchar(36))")
		dropLater(t, c, table)

		opts := retypedAfterARefusal(t, c, table)

		if _, err := engine.Apply(t.Context(), c, opts); !errors.Is(err, engine.ErrDirty) {
			t.Fatalf("Apply returned %v, want a failure reporting ErrDirty", err)
		}

		_, err := engine.Resolve(t.Context(), c, opts, engine.Repair{Mode: engine.RepairResume})
		if !errors.Is(err, engine.ErrNotResumable) {
			t.Fatalf("Resolve --resume returned %v, want %v", err, engine.ErrNotResumable)
		}

		for _, want := range []string{"--applied", "--rolled-back"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not name the repair %q:\n%v", want, err)
			}
		}

		settles(t, c, opts, engine.Repair{
			Mode: engine.RepairRolledBack, Reason: "dropped the index by hand",
		})

		if state, _ := reconciledState(t, c, opts); state != history.StatePending {
			t.Errorf("the migration reconciled to %q, want pending", state)
		}

		t.Log("ADR 0062's own argument, one layer further in: the recorded index names a statement")
		t.Log("in the list that run emitted, and an expansion's statements are a function of a")
		t.Log("schema this run has already changed -- so re-rendering them here numbers a different")
		t.Log("list. Before this it re-rendered them silently and resumed into the wrong place.")
	})
}
