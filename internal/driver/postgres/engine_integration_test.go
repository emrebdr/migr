//go:build integration

package postgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// queueRuntime is what the migrations these tests stage are written in, as a history row records it.
const queueRuntime = "sql"

// queueStart is the instant staged runs begin at, since internal/engine may not read a clock.
var queueStart = time.Date(2026, 7, 27, 15, 30, 0, 0, time.UTC)

// staged is one migration a queue test runs: its filename, its file, and what it emits.
type staged struct {
	// version is the migration's UTC timestamp.
	version string
	// name is its snake_case name.
	name string
	// ops are the operations it emits, which the executor asks for only if it will run.
	ops ir.Operations
	// down is the undo it authored, and staging none makes the executor derive one.
	down ir.Operations
	// body overrides what its file holds, so a test can edit a migration after applying it.
	body string
	// probe runs while the executor is under the migration lock, when a test wants to look.
	probe func(t *testing.T)
}

// queueSource hands the executor what a test staged, and remembers which migrations were asked.
type queueSource struct {
	t      *testing.T
	staged []staged
	asked  []string
}

// Emit returns the staged operations, running the staged probe first where there is one.
func (s *queueSource) Emit(_ context.Context, m migration.Migration, dir ir.Direction) (ir.Operations, error) {
	s.asked = append(s.asked, m.Version)

	for _, item := range s.staged {
		if item.version != m.Version {
			continue
		}

		if item.probe != nil {
			item.probe(s.t)
		}

		return staging(item, dir)
	}

	return nil, errors.New("nothing staged this migration")
}

// staging returns what a staged migration performs in one direction, or asks for a derived undo.
func staging(item staged, dir ir.Direction) (ir.Operations, error) {
	if dir != ir.Down {
		return item.ops, nil
	}

	if item.down == nil {
		return nil, engine.ErrNoDownAuthored
	}

	return item.down, nil
}

// queueOptions builds what an apply run is given from the migrations a test staged, in order.
func queueOptions(t *testing.T, items ...staged) engine.Options {
	t.Helper()

	files := make([]history.File, 0, len(items))

	for _, item := range items {
		files = append(files, history.NewFile(parseStaged(t, item), []byte(stagedBody(item))))
	}

	return engine.Options{
		Planner:    plan.Planner{Compiler: compilerOnly{}},
		Source:     &queueSource{t: t, staged: items},
		Migrations: files,
		Clock:      steadyClock(),
		Runtime:    queueRuntime,
	}
}

// compilerOnly compiles operations through this package's own compiler, with no session at all.
type compilerOnly struct{}

// Compile turns one operation into this engine's statements.
func (compilerOnly) Compile(op ir.Operation) ([]driver.Statement, error) {
	return compile(op)
}

// steadyClock returns a clock advancing a fixed step per reading, so a duration is never zero.
func steadyClock() engine.Clock {
	at := queueStart

	return func() time.Time {
		now := at
		at = at.Add(100 * time.Millisecond)

		return now
	}
}

// parseStaged reads a staged migration's filename through the grammar a real one comes from.
func parseStaged(t *testing.T, item staged) migration.Migration {
	t.Helper()

	m, err := migration.Parse(item.version + "_" + item.name + ".sql")
	if err != nil {
		t.Fatalf("staging %s: %v", item.version, err)
	}

	return m
}

// stagedBody is what a staged migration's file holds, which its source digest covers.
func stagedBody(item staged) string {
	if item.body != "" {
		return item.body
	}

	return "-- migration " + item.version + "\n"
}

// rewritten returns the same migration with its file edited, as someone amending it in place would.
func rewritten(item staged) staged {
	item.body = stagedBody(item) + "-- and a line its author added afterwards\n"

	return item
}

// createAccounts is the migration every queue test starts from, and the one later ones need.
func createAccounts(t *testing.T) staged {
	t.Helper()

	return staged{
		version: "20260727T153000",
		name:    "create_accounts",
		ops: ir.Operations{ir.CreateTable{
			Table:      "accounts",
			Columns:    []ir.Column{{Name: "id", Type: probeType(t, "int64")}},
			PrimaryKey: []string{"id"},
		}},
	}
}

// addNickname alters the table createAccounts made, so it only succeeds if that migration landed.
func addNickname(t *testing.T) staged {
	t.Helper()

	return staged{
		version: "20260727T153100",
		name:    "add_nickname",
		ops: ir.Operations{ir.AddColumn{Table: "accounts", Column: ir.Column{
			Name:     "nickname",
			Type:     probeType(t, "string(64)"),
			Nullable: true,
		}}},
	}
}

// alterNothing names a table nothing created, so its third statement is rejected by the server.
func alterNothing(t *testing.T) staged {
	t.Helper()

	return staged{
		version: "20260727T153200",
		name:    "alter_nothing",
		ops: ir.Operations{
			ir.AddIndex{Table: "accounts", Name: "idx_accounts_id", Columns: []string{"id"}},
			ir.AddColumn{Table: "no_such_table", Column: ir.Column{
				Name:     "orphan",
				Type:     probeType(t, "int32"),
				Nullable: true,
			}},
		},
	}
}

// createLater is queued behind a failure, so its absence is what proves the queue was abandoned.
func createLater(t *testing.T) staged {
	t.Helper()

	return staged{
		version: "20260727T153300",
		name:    "create_later",
		ops: ir.Operations{ir.CreateTable{
			Table:   "later",
			Columns: []ir.Column{{Name: "id", Type: probeType(t, "int64")}},
		}},
	}
}

// queueSession opens a session pointed at one schema and at a history table of its own.
func queueSession(t *testing.T, dsn, schema string) *pgConn {
	t.Helper()

	return historyIn(t, dsn, schema)
}

// recordedRuns reads the history back through the neutral contract, needing no SQL of its own.
func recordedRuns(t *testing.T, session *pgConn) []history.Record {
	t.Helper()

	runs, err := session.History().List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	return runs
}

func TestTheExecutorAppliesAQueueAndRecordsEveryMigrationInOneBatch(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)

			session := queueSession(t, dsn, target)
			observer := queueSession(t, dsn, target)

			empty := describeSchema(t, observer, target)

			result, err := engine.Apply(t.Context(), session,
				queueOptions(t, createAccounts(t), addNickname(t)))
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if describeSchema(t, observer, target) == empty {
				t.Fatal("a queue that reported success changed nothing another session can see, so " +
					"every assertion below would pass for the wrong reason")
			}

			if result.Batch != 1 || len(result.Runs) != 2 {
				t.Fatalf("the run reports batch %d and %d migrations, want batch 1 and 2",
					result.Batch, len(result.Runs))
			}

			runs := recordedRuns(t, session)
			if len(runs) != 2 {
				t.Fatalf("the history holds %d rows, want one per migration", len(runs))
			}

			for i, rec := range runs {
				if rec.Batch != 1 || rec.Ordinal != int32(i)+1 {
					t.Errorf("row %d is (batch %d, ordinal %d), want (1, %d)",
						i, rec.Batch, rec.Ordinal, i+1)
				}

				if rec.Outcome != history.Applied || rec.Direction != ir.Up {
					t.Errorf("row %d is %s %s, want an applied up", i, rec.Direction, rec.Outcome)
				}

				if rec.Driver != Name || rec.Runtime != queueRuntime {
					t.Errorf("row %d records driver %q runtime %q, want %q and %q",
						i, rec.Driver, rec.Runtime, Name, queueRuntime)
				}
			}

			t.Log("The second migration alters the table the first created, so a queue that ran them")
			t.Log("out of order or skipped one would fail against the server rather than here.")
			t.Logf("The digest is this package's own, not internal/schema's snapshot: %s replaces "+
				"this with the neutral comparison.", snapshotSupersededBy)
		})
	}
}

func TestAQueueOnADatabaseWithNoHistoryCreatesOneRatherThanReportingNothingToDo(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)

	result, err := engine.Apply(t.Context(), session, queueOptions(t, createAccounts(t)))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(result.Runs) != 1 {
		t.Fatalf("the run applied %d migrations against an empty database, want 1", len(result.Runs))
	}

	t.Log("history.Read maps a missing table to an absent log, and an absent log reconciles to")
	t.Log("zero statuses rather than to every migration pending (ADR 0019). Reading before")
	t.Log("ensuring would therefore report nothing to do on every fresh database, forever.")
}

func TestAFailureMidQueueLeavesTheEarlierMigrationsAppliedAndTheLaterOnesUntouched(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)

			session := queueSession(t, dsn, target)
			observer := queueSession(t, dsn, target)

			if _, err := engine.Apply(t.Context(), session,
				queueOptions(t, createAccounts(t))); err != nil {
				t.Fatalf("the groundwork migration failed: %v", err)
			}

			before := describeSchema(t, observer, target)

			opts := queueOptions(t, createAccounts(t), alterNothing(t), createLater(t))

			result, err := engine.Apply(t.Context(), session, opts)
			if err == nil {
				t.Fatal("the queue reported success though its second migration names no table")
			}

			if after := describeSchema(t, observer, target); after != before {
				t.Errorf("the schema changed across an aborted queue.\nbefore:\n%s\nafter:\n%s",
					before, after)
			}

			if len(result.Runs) != 0 {
				t.Errorf("the result claims %d runs, want none: the only pending migration failed",
					len(result.Runs))
			}

			asked := opts.Source.(*queueSource).asked
			if len(asked) != 1 || asked[0] != alterNothing(t).version {
				t.Errorf("the source was asked about %v, want only the migration that failed", asked)
			}

			runs := recordedRuns(t, session)
			if len(runs) != 2 {
				t.Fatalf("the history holds %d rows, want the first run and the failed one", len(runs))
			}

			if runs[1].Outcome != history.Failed || runs[1].Version != alterNothing(t).version {
				t.Errorf("the second row is %s for %s, want a failed run of the migration that failed",
					runs[1].Outcome, runs[1].Version)
			}

			t.Log("The failing migration's first statement succeeds and its second does not, so the")
			t.Log("unchanged digest is a rollback rather than a migration that never started. The")
			t.Log("migration behind it was never asked what it does, let alone run.")
		})
	}
}

func TestASecondRunSkipsWhatTheFirstAppliedAndOpensANewBatch(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)

	if _, err := engine.Apply(t.Context(), session, queueOptions(t, createAccounts(t))); err != nil {
		t.Fatalf("the first run failed: %v", err)
	}

	opts := queueOptions(t, createAccounts(t), addNickname(t))

	result, err := engine.Apply(t.Context(), session, opts)
	if err != nil {
		t.Fatalf("the second run failed: %v", err)
	}

	if result.Batch != 2 {
		t.Errorf("the second run recorded under batch %d, want 2", result.Batch)
	}

	if len(result.Runs) != 1 || result.Runs[0].Ordinal != 1 {
		t.Fatalf("the second run applied %v, want only the pending migration at ordinal 1",
			result.Runs)
	}

	asked := opts.Source.(*queueSource).asked
	if len(asked) != 1 || asked[0] != addNickname(t).version {
		t.Errorf("the source was asked about %v, want only the pending migration", asked)
	}

	runs := recordedRuns(t, session)
	if len(runs) != 2 || runs[1].Batch != 2 || runs[1].Ordinal != 1 {
		t.Fatalf("the history holds %v, want a second batch starting at ordinal 1", runs)
	}

	t.Log("Re-running the first migration would fail against the server, so this is also the")
	t.Log("proof that reconciliation ran rather than the queue being taken at face value.")
}

func TestAFailureNamesTheMigrationTheOperationAndTheStatementThatCausedIt(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)

	if _, err := engine.Apply(t.Context(), session, queueOptions(t, createAccounts(t))); err != nil {
		t.Fatalf("the groundwork migration failed: %v", err)
	}

	_, err := engine.Apply(t.Context(), session,
		queueOptions(t, createAccounts(t), alterNothing(t)))

	var failure *engine.MigrationError
	if !errors.As(err, &failure) {
		t.Fatalf("the queue failed with %v, which names no migration", err)
	}

	if failure.Migration.Version != alterNothing(t).version {
		t.Errorf("the failure names %s, want %s", failure.Migration.Version, alterNothing(t).version)
	}

	if failure.Operation == nil || failure.Operation.Kind() != ir.KindAddColumn {
		t.Errorf("the failure names operation %v, want the add_column that was rejected",
			failure.Operation)
	}

	var statement *engine.StatementError
	if !errors.As(err, &statement) {
		t.Fatalf("the queue failed with %v, which names no statement", err)
	}

	if statement.Position != 2 {
		t.Errorf("the failure reports statement %d, want the second", statement.Position)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("the queue failed with %v, which is not a server error -- a client-side failure "+
			"would leave the server never having entered an aborted transaction", err)
	}

	if pgErr.Code != undefinedTable {
		t.Errorf("the server rejected the statement with SQLSTATE %s, want %s",
			pgErr.Code, undefinedTable)
	}

	t.Log("Invariant #9 wants the migration and the offending operation. A driver reports a")
	t.Log("statement position and has never heard of either, so plan.Plan.At is what turns that")
	t.Log("position back into the operation the migration declared.")
}

// discardAfterWork wraps a session so a migration's transaction is thrown away once its work is done.
type discardAfterWork struct {
	*pgConn
	// err is what the wrapper returns after the work succeeded, turning the commit into a rollback.
	err error
}

// Transact runs the migration's work and then fails, so everything inside is discarded.
func (d discardAfterWork) Transact(ctx context.Context, work driver.Work) error {
	return d.pgConn.Transact(ctx, func(ctx context.Context) error {
		if err := work(ctx); err != nil {
			return err
		}

		return d.err
	})
}

func TestTheHistoryRowIsDiscardedWithTheMigrationItDescribes(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)
	observer := queueSession(t, dsn, target)

	if _, err := engine.Apply(t.Context(), session, queueOptions(t, createAccounts(t))); err != nil {
		t.Fatalf("the groundwork migration failed: %v", err)
	}

	before := describeSchema(t, observer, target)

	discarded := errors.New("this run is thrown away after its row was written")
	wrapped := discardAfterWork{pgConn: session, err: discarded}

	if _, err := engine.Apply(t.Context(), wrapped,
		queueOptions(t, createAccounts(t), addNickname(t))); !errors.Is(err, discarded) {
		t.Fatalf("Apply returned %v, want the failure the wrapper injected after the row was written",
			err)
	}

	if after := describeSchema(t, observer, target); after != before {
		t.Errorf("the discarded migration survived.\nbefore:\n%s\nafter:\n%s", before, after)
	}

	for _, rec := range recordedRuns(t, session) {
		if rec.Version == addNickname(t).version && rec.Outcome == history.Applied {
			t.Fatal("a row claiming the migration was applied survived the rollback that discarded " +
				"the migration, so this store wrote it outside the caller's transaction")
		}
	}

	t.Log("ADR 0013's record-before-commit rests on a driver's store writing through the open")
	t.Log("transaction. Nothing in the contract can express that, so it is checked here: a store")
	t.Log("on a second connection would leave this row behind describing a migration that is gone.")
}

func TestTheMigrationLockIsHeldForTheWholeQueueAndGivenBackAfterward(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)

	contend := func(t *testing.T) {
		t.Helper()

		rival := lockedConnWith(t, dsn, driver.Options{
			Schema:       target,
			HistoryTable: historyTable,
			LockWait:     250 * time.Millisecond,
		})

		if _, err := rival.Lock(t.Context()); !errors.Is(err, driver.ErrLockTimeout) {
			t.Errorf("a second session acquired the lock mid-queue, or failed for another reason: %v",
				err)
		}
	}

	second := addNickname(t)
	second.probe = contend

	if _, err := engine.Apply(t.Context(), session,
		queueOptions(t, createAccounts(t), second)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	after := lockedConnWith(t, dsn, driver.Options{Schema: target, HistoryTable: historyTable})

	unlock, err := after.Lock(t.Context())
	if err != nil {
		t.Fatalf("the lock was not given back when the queue finished: %v", err)
	}

	if err := unlock(t.Context()); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	t.Log("The probe runs while the executor is asking what the second migration does, which is")
	t.Log("under the lock by construction. That is what makes 'reconciled under the lock' a")
	t.Log("property rather than a claim: what is pending cannot change while the queue runs.")
}

// partialAccountsIndex creates an index no v1 operation can express, which is what raw is for.
const partialAccountsIndex = `CREATE INDEX "idx_accounts_partial" ON "accounts" ("id") WHERE "id" > 0`

// addPartialIndex is a migration nothing can invert, because raw's effect is opaque to the framework.
func addPartialIndex() staged {
	return staged{
		version: "20260727T153400",
		name:    "add_partial_index",
		ops:     ir.Operations{ir.Raw{Driver: Name, SQL: partialAccountsIndex}},
	}
}

// archiveAccounts undoes createAccounts by renaming rather than dropping, which no inverse derives.
func archiveAccounts(t *testing.T) staged {
	t.Helper()

	item := createAccounts(t)
	item.down = ir.Operations{ir.RenameTable{From: "accounts", To: "accounts_archived"}}

	return item
}

func TestApplyingAQueueAndRollingItBackRestoresTheSchemaDigestExactly(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)

			session := queueSession(t, dsn, target)
			observer := queueSession(t, dsn, target)

			if _, err := engine.Apply(t.Context(), session,
				queueOptions(t, createAccounts(t))); err != nil {
				t.Fatalf("preparing the history table: %v", err)
			}

			empty := describeSchema(t, observer, target)

			if _, err := engine.Rollback(t.Context(), session,
				queueOptions(t, createAccounts(t))); err != nil {
				t.Fatalf("clearing the schema: %v", err)
			}

			bare := describeSchema(t, observer, target)
			if bare == empty {
				t.Fatal("rolling back the groundwork migration changed nothing another session " +
					"can see, so every assertion below would pass for the wrong reason")
			}

			opts := queueOptions(t, createAccounts(t), addNickname(t))

			if _, err := engine.Apply(t.Context(), session, opts); err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if describeSchema(t, observer, target) == bare {
				t.Fatal("the queue that reported success changed nothing")
			}

			if _, err := engine.Rollback(t.Context(), session, opts); err != nil {
				t.Fatalf("Rollback: %v", err)
			}

			if restored := describeSchema(t, observer, target); restored != bare {
				t.Errorf("rolling the queue back did not restore the schema it found:\n%s",
					diffLines(bare, restored))
			}

			t.Log("This is the property the whole task exists for, and it is asserted against the")
			t.Log("server rather than against the operations: the inverses are derived, compiled")
			t.Log("and run, and what a second session sees afterwards is what it saw before.")
			t.Logf("The digest is this package's own, not internal/schema's snapshot: %s replaces "+
				"this with the neutral comparison.", snapshotSupersededBy)
		})
	}
}

func TestARollbackUndoesTheMigrationsNewestFirstAgainstTheServer(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)

	opts := queueOptions(t, createAccounts(t), addNickname(t))

	if _, err := engine.Apply(t.Context(), session, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	result, err := engine.Rollback(t.Context(), session, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if len(result.Runs) != 2 {
		t.Fatalf("the rollback undid %d migrations, want two", len(result.Runs))
	}

	if result.Runs[0].Migration.Version != addNickname(t).version {
		t.Errorf("the rollback undid %s first, want the newest migration",
			result.Runs[0].Migration.Version)
	}

	t.Log("The server is the assertion as well as the order: undone oldest first, the first")
	t.Log("statement would drop the table and the second would try to drop a column of it.")
}

func TestARollbackRecordsOneDownRunPerMigrationInANewBatch(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)

	opts := queueOptions(t, createAccounts(t), addNickname(t))

	if _, err := engine.Apply(t.Context(), session, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := engine.Rollback(t.Context(), session, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	runs := recordedRuns(t, session)
	if len(runs) != 4 {
		t.Fatalf("the history holds %d rows, want an up and a down for each migration", len(runs))
	}

	for i, rec := range runs[:2] {
		if rec.Direction != ir.Up || rec.Batch != 1 {
			t.Errorf("row %d is a %s in batch %d, want an up in batch 1", i, rec.Direction, rec.Batch)
		}
	}

	for i, rec := range runs[2:] {
		if rec.Direction != ir.Down || rec.Batch != 2 || rec.Ordinal != int32(i)+1 {
			t.Errorf("row %d is a %s at (batch %d, ordinal %d), want a down at (2, %d)",
				i+2, rec.Direction, rec.Batch, rec.Ordinal, i+1)
		}
	}

	if runs[2].Version != addNickname(t).version {
		t.Errorf("the first down undoes %s, want the newest migration", runs[2].Version)
	}

	t.Log("The log is append-only, so a rollback adds rows rather than deleting the ones it")
	t.Log("undoes -- and it takes its own batch, which is what makes (batch, ordinal) unique.")
}

func TestAnIrreversibleMigrationHaltsTheRollbackWithTheSchemaUntouched(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)
	observer := queueSession(t, dsn, target)

	opts := queueOptions(t, createAccounts(t), addPartialIndex(), addNickname(t))

	if _, err := engine.Apply(t.Context(), session, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	before := describeSchema(t, observer, target)

	_, err := engine.Rollback(t.Context(), session, opts)
	if !errors.Is(err, ir.ErrIrreversible) {
		t.Fatalf("Rollback returned %v, want a refusal reporting ir.ErrIrreversible", err)
	}

	if after := describeSchema(t, observer, target); after != before {
		t.Errorf("the refused rollback changed the schema:\n%s", diffLines(before, after))
	}

	for _, rec := range recordedRuns(t, session) {
		if rec.Direction == ir.Down {
			t.Errorf("a down run was recorded for %s despite the refusal", rec.Version)
		}
	}

	t.Log("The irreversible migration is in the middle, so a lazy rollback would have undone the")
	t.Log("newest one and then refused. raw is opaque to the framework by construction, which is")
	t.Log("why its own refusal tells the author to write the undo as a raw operation in Down.")
}

func TestARolledBackMigrationIsAppliedAgainFromItsUp(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)
	observer := queueSession(t, dsn, target)

	opts := queueOptions(t, createAccounts(t), addNickname(t))

	if _, err := engine.Apply(t.Context(), session, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	applied := describeSchema(t, observer, target)

	if _, err := engine.Rollback(t.Context(), session, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	result, err := engine.Apply(t.Context(), session, opts)
	if err != nil {
		t.Fatalf("re-applying: %v", err)
	}

	if len(result.Runs) != 2 || result.Batch != 3 {
		t.Fatalf("the re-apply ran %d migrations under batch %d, want two under batch 3",
			len(result.Runs), result.Batch)
	}

	if again := describeSchema(t, observer, target); again != applied {
		t.Errorf("re-applying after a rollback reached a different schema:\n%s",
			diffLines(applied, again))
	}

	var directions []ir.Direction
	for _, rec := range recordedRuns(t, session) {
		directions = append(directions, rec.Direction)
	}

	want := []ir.Direction{ir.Up, ir.Up, ir.Down, ir.Down, ir.Up, ir.Up}
	if !slices.Equal(directions, want) {
		t.Errorf("the log reads %v, want %v", directions, want)
	}

	t.Log("Rolling forward again runs the original Up rather than reversing the Down that undid")
	t.Log("it, which is why a down's inverse is never needed and why the log keeps both.")
}

func TestAnAuthoredDownIsRunInsteadOfADerivedOne(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)
	observer := queueSession(t, dsn, target)

	derived := queueOptions(t, createAccounts(t))

	if _, err := engine.Apply(t.Context(), session, derived); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	applied := describeSchema(t, observer, target)

	if _, err := engine.Rollback(t.Context(), session,
		queueOptions(t, archiveAccounts(t))); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	archived := describeSchema(t, observer, target)
	if archived == applied {
		t.Fatal("the authored down changed nothing another session can see")
	}

	if !strings.Contains(archived, "accounts_archived") {
		t.Errorf("the schema does not hold the table the authored down renamed to:\n%s", archived)
	}

	if strings.Contains(archived, "\"accounts\"") {
		t.Errorf("the derived inverse ran as well, dropping what the authored down kept:\n%s",
			archived)
	}

	t.Log("The derived inverse of create_table is drop_table, so a run that ignored the authored")
	t.Log("down would have destroyed the table this one deliberately kept.")
}

func TestAnEditedMigrationRefusesTheRunAndReportsBothChecksums(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)

			session := queueSession(t, dsn, target)
			observer := queueSession(t, dsn, target)

			first := createAccounts(t)
			if _, err := engine.Apply(t.Context(), session, queueOptions(t, first)); err != nil {
				t.Fatalf("Apply: %v", err)
			}

			applied := describeSchema(t, observer, target)
			recorded := recordedRuns(t, session)

			_, err := engine.Apply(t.Context(), session,
				queueOptions(t, rewritten(first), addNickname(t)))
			if !errors.Is(err, engine.ErrDrifted) {
				t.Fatalf("Apply returned %v, want a refusal reporting ErrDrifted", err)
			}

			var drift *engine.DriftError
			if !errors.As(err, &drift) {
				t.Fatalf("errors.As found no *engine.DriftError in %T", err)
			}

			if drift.Recorded != recorded[0].Checksum {
				t.Errorf("the refusal reports %v recorded, want %v -- the value in the table",
					drift.Recorded, recorded[0].Checksum)
			}

			if drift.Source != checksum.OfSource([]byte(stagedBody(rewritten(first)))) {
				t.Errorf("the refusal reports %v on disk, want what the edited file hashes to",
					drift.Source)
			}

			if drift.Source == drift.Recorded.Source {
				t.Fatal("the two halves the message compares are equal, so the message would " +
					"report drift while showing an operator two identical digests")
			}

			for _, want := range []string{drift.Recorded.String(), drift.Source.String()} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message does not carry %s in full:\n%v", want, err)
				}
			}

			if after := describeSchema(t, observer, target); after != applied {
				t.Errorf("the refused run changed the schema:\n%s", diffLines(applied, after))
			}

			if runs := recordedRuns(t, session); len(runs) != len(recorded) {
				t.Errorf("the history grew from %d rows to %d despite the refusal",
					len(recorded), len(runs))
			}

			t.Log("The pending migration queued behind the edited one is what makes this a whole-run")
			t.Log("refusal rather than a per-migration one: it would otherwise have applied first.")
		})
	}
}

func TestAnEditedMigrationRefusesARollbackToo(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)
	observer := queueSession(t, dsn, target)

	first := createAccounts(t)
	if _, err := engine.Apply(t.Context(), session, queueOptions(t, first)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	applied := describeSchema(t, observer, target)

	_, err := engine.Rollback(t.Context(), session, queueOptions(t, rewritten(first)))
	if !errors.Is(err, engine.ErrDrifted) {
		t.Fatalf("Rollback returned %v, want a refusal reporting ErrDrifted", err)
	}

	if !strings.Contains(err.Error(), "stronger reason") {
		t.Errorf("the refusal does not say why a rollback's is the stronger one:\n%v", err)
	}

	if after := describeSchema(t, observer, target); after != applied {
		t.Errorf("the refused rollback changed the schema:\n%s", diffLines(applied, after))
	}

	t.Log("Going down, the undo about to run would be derived from bytes that are not the ones")
	t.Log("that ran -- so the inverse would describe a schema change this database never made.")
}

func TestARolledBackMigrationIsFreeToBeEdited(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)

	first := createAccounts(t)
	if _, err := engine.Apply(t.Context(), session, queueOptions(t, first)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := engine.Rollback(t.Context(), session, queueOptions(t, first)); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	result, err := engine.Apply(t.Context(), session, queueOptions(t, rewritten(first)))
	if err != nil {
		t.Fatalf("re-applying an edited migration the database no longer carries: %v", err)
	}

	if len(result.Runs) != 1 {
		t.Fatalf("the run applied %d migrations, want the edited one", len(result.Runs))
	}

	runs := recordedRuns(t, session)
	if got := runs[len(runs)-1].Checksum.Source; got != checksum.OfSource([]byte(stagedBody(rewritten(first)))) {
		t.Errorf("the new row records %v, want the edited file's digest", got)
	}

	t.Log("Invariant #6 protects applied migrations. One the database no longer carries is free")
	t.Log("to change, which is why the fold reports pending rather than drifted for it.")
}

// narrowed returns the options a queue test uses with one selector applied to it.
func narrowed(t *testing.T, scope engine.Scope, items ...staged) engine.Options {
	t.Helper()

	opts := queueOptions(t, items...)
	opts.Scope = scope

	return opts
}

func TestRollingBackTheLastBatchLeavesTheOneBeforeItApplied(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)

			session := queueSession(t, dsn, target)
			observer := queueSession(t, dsn, target)

			first := createAccounts(t)
			second := addNickname(t)

			if _, err := engine.Apply(t.Context(), session, queueOptions(t, first)); err != nil {
				t.Fatalf("applying the first batch: %v", err)
			}

			afterFirst := describeSchema(t, observer, target)

			if _, err := engine.Apply(t.Context(), session,
				queueOptions(t, first, second)); err != nil {
				t.Fatalf("applying the second batch: %v", err)
			}

			if describeSchema(t, observer, target) == afterFirst {
				t.Fatal("the second batch changed nothing another session can see, so the " +
					"assertion below would pass for the wrong reason")
			}

			result, err := engine.Rollback(t.Context(), session,
				narrowed(t, engine.LastBatch(), first, second))
			if err != nil {
				t.Fatalf("Rollback: %v", err)
			}

			if len(result.Runs) != 1 || result.Runs[0].Migration.Version != second.version {
				t.Fatalf("the rollback undid %d migrations, want the second batch alone",
					len(result.Runs))
			}

			if after := describeSchema(t, observer, target); after != afterFirst {
				t.Errorf("the schema did not return to what the first batch left:\n%s",
					diffLines(afterFirst, after))
			}

			t.Log("This is migr down with no arguments: one invocation is the unit an operator")
			t.Log("undoes, and the migration before it in the same repository stays applied.")
		})
	}
}

func TestASecondRollbackOfTheLastBatchTakesTheBatchBeforeIt(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)
	observer := queueSession(t, dsn, target)

	first := createAccounts(t)
	second := addNickname(t)

	if err := session.History().Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	empty := describeSchema(t, observer, target)

	if _, err := engine.Apply(t.Context(), session, queueOptions(t, first)); err != nil {
		t.Fatalf("applying the first batch: %v", err)
	}

	if _, err := engine.Apply(t.Context(), session, queueOptions(t, first, second)); err != nil {
		t.Fatalf("applying the second batch: %v", err)
	}

	for i := range 2 {
		result, err := engine.Rollback(t.Context(), session,
			narrowed(t, engine.LastBatch(), first, second))
		if err != nil {
			t.Fatalf("rollback %d: %v", i+1, err)
		}

		if len(result.Runs) != 1 {
			t.Fatalf("rollback %d undid %d migrations, want one batch's worth", i+1,
				len(result.Runs))
		}
	}

	if after := describeSchema(t, observer, target); after != empty {
		t.Errorf("two rollbacks did not return the schema to empty:\n%s", diffLines(empty, after))
	}

	t.Log("Latest is direction-blind, so after the first rollback the highest batch in the table")
	t.Log("is that rollback's own. Reading the batch from Latest would report nothing to undo")
	t.Log("here, and the first batch would be unreachable forever.")
}

func TestSteppingOneUpAndOneDownRoundTripsTheSchemaDigest(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)
	observer := queueSession(t, dsn, target)

	first := createAccounts(t)
	second := addNickname(t)

	if _, err := engine.Apply(t.Context(), session, queueOptions(t, first)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	afterFirst := describeSchema(t, observer, target)

	stepped, err := engine.Apply(t.Context(), session,
		narrowed(t, engine.Steps(1), first, second))
	if err != nil {
		t.Fatalf("stepping up: %v", err)
	}

	if len(stepped.Runs) != 1 || stepped.Runs[0].Ordinal != 1 {
		t.Fatalf("--step 1 ran %d migrations at ordinal %d, want one numbered from one",
			len(stepped.Runs), stepped.Runs[0].Ordinal)
	}

	back, err := engine.Rollback(t.Context(), session,
		narrowed(t, engine.Steps(1), first, second))
	if err != nil {
		t.Fatalf("stepping down: %v", err)
	}

	if len(back.Runs) != 1 {
		t.Fatalf("--step 1 undid %d migrations, want one", len(back.Runs))
	}

	if after := describeSchema(t, observer, target); after != afterFirst {
		t.Errorf("one step up and one step down did not round-trip:\n%s",
			diffLines(afterFirst, after))
	}
}

func TestNarrowingToAVersionLandsTheSchemaOnTheStateThatVersionDescribes(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)
	observer := queueSession(t, dsn, target)

	first := createAccounts(t)
	second := addNickname(t)

	if _, err := engine.Apply(t.Context(), session,
		narrowed(t, engine.To(first.version), first, second)); err != nil {
		t.Fatalf("applying up to the first: %v", err)
	}

	atFirst := describeSchema(t, observer, target)

	if strings.Contains(atFirst, "nickname") {
		t.Fatalf("--to the first version applied the second as well:\n%s", atFirst)
	}

	if _, err := engine.Apply(t.Context(), session, queueOptions(t, first, second)); err != nil {
		t.Fatalf("applying the rest: %v", err)
	}

	if _, err := engine.Rollback(t.Context(), session,
		narrowed(t, engine.To(first.version), first, second)); err != nil {
		t.Fatalf("rolling back to the first: %v", err)
	}

	if after := describeSchema(t, observer, target); after != atFirst {
		t.Errorf("--to did not land on the state that version describes:\n%s",
			diffLines(atFirst, after))
	}

	t.Log("Both runs name the same version and both leave the database at it -- applying")
	t.Log("through it going up, undoing everything newer going down.")
}

func TestANarrowedRollbackRecordsAFreshBatchNumberedFromOne(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := queueSession(t, dsn, target)

	first := createAccounts(t)
	second := addNickname(t)

	if _, err := engine.Apply(t.Context(), session, queueOptions(t, first, second)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	result, err := engine.Rollback(t.Context(), session,
		narrowed(t, engine.Steps(1), first, second))
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if result.Batch != 2 {
		t.Errorf("the narrowed rollback recorded under batch %d, want one past the apply",
			result.Batch)
	}

	runs := recordedRuns(t, session)

	last := runs[len(runs)-1]
	if last.Batch != 2 || last.Ordinal != 1 || last.Direction != ir.Down {
		t.Errorf("the down row is (batch %d, ordinal %d, %s), want (2, 1, down)",
			last.Batch, last.Ordinal, last.Direction)
	}
}
