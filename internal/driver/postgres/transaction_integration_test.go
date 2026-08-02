//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
)

// undefinedTable is the SQLSTATE PostgreSQL raises for a statement naming a table it does not have.
const undefinedTable = "42P01"

// scratchPrefix opens the name of every schema a test in this package gets to itself.
const scratchPrefix = "migr_test_"

// scratchSchema gives one test a schema of its own, named after it, and drops it afterwards.
func scratchSchema(t *testing.T, dsn string) string {
	t.Helper()

	name := scratchName(t.Name())

	admin := openSession(t, dsn)

	if _, err := admin.conn.Exec(t.Context(), "DROP SCHEMA IF EXISTS "+quote(name)+" CASCADE"); err != nil {
		t.Fatalf("clearing schema %q: %v", name, err)
	}

	if _, err := admin.conn.Exec(t.Context(), "CREATE SCHEMA "+quote(name)); err != nil {
		t.Fatalf("creating schema %q: %v", name, err)
	}

	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		if _, err := admin.conn.Exec(ctx, "DROP SCHEMA "+quote(name)+" CASCADE"); err != nil {
			t.Errorf("dropping schema %q: %v", name, err)
		}
	})

	return name
}

// fingerprintWidth is what a scratch name reserves for the mark that keeps two cut names apart.
const fingerprintWidth = len("_00000000")

// scratchRoom is how much of a test's own name a schema identifier has left for it.
//
// PostgreSQL cuts an identifier over NAMEDATALEN - 1 bytes and reports it with a notice rather
// than an error, so two tests whose names agree for that long would quietly share one schema --
// and this package names its tests in sentences. Cutting here instead, and ending every name with
// a fingerprint of the whole of it, keeps the schema readable in a failure and keeps it distinct.
const scratchRoom = maxIdentifier - len(scratchPrefix) - fingerprintWidth

// scratchName renders a test's name as a schema identifier, cut to fit and kept distinct.
func scratchName(test string) string {
	readable := strings.NewReplacer("/", "_", " ", "_").Replace(strings.ToLower(test))
	if len(readable) > scratchRoom {
		readable = readable[:scratchRoom]
	}

	return scratchPrefix + readable + "_" + fingerprint(test)
}

// fingerprint distinguishes two test names that survive the cut with the same bytes.
func fingerprint(test string) string {
	sum := fnv.New32a()
	sum.Write([]byte(test))

	return fmt.Sprintf("%08x", sum.Sum32())
}

func TestAScratchNameFitsAndStaysDistinct(t *testing.T) {
	shared := "TestSomethingThisPackageWouldPlausiblyCallATestOfItsOwnBehavior"

	first := scratchName(shared + "UnderOneCondition")
	second := scratchName(shared + "UnderAnother")

	for _, name := range []string{first, second} {
		if len(name) > maxIdentifier {
			t.Errorf("scratch schema %q is %d bytes, over the %d PostgreSQL keeps",
				name, len(name), maxIdentifier)
		}
	}

	if first == second {
		t.Errorf("two tests would share the scratch schema %q", first)
		t.Log("Both names are cut at the same place, so only the fingerprint of the whole name")
		t.Log("keeps them apart. Sharing one schema means one test drops the other's tables.")
	}
}

// sessionIn opens a session pointed at one schema, so unqualified DDL lands only there.
func sessionIn(t *testing.T, dsn, schema string) *pgConn {
	t.Helper()

	conn := open(t, dsn, driver.Options{Schema: schema})

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", conn)
	}

	return session
}

// compileAll turns a migration's operations into the statements an executor would be handed.
func compileAll(t *testing.T, ops ir.Operations) []driver.Statement {
	t.Helper()

	var stmts []driver.Statement

	for _, op := range ops {
		if err := op.Validate(); err != nil {
			t.Fatalf("%s: the fixture is not a well-formed operation: %v", op.Kind(), err)
		}

		compiled, err := compile(op)
		if err != nil {
			t.Fatalf("%s: %v", op.Kind(), err)
		}

		stmts = append(stmts, compiled...)
	}

	return stmts
}

// groundwork is a migration that succeeds, establishing the schema the failing one starts from.
func groundwork(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.CreateTable{
			Table:      "accounts",
			Columns:    []ir.Column{{Name: "id", Type: probeType(t, "int64")}},
			PrimaryKey: []string{"id"},
		},
	}
}

// halfway is a migration whose first two statements succeed and whose third names no table.
func halfway(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.AddColumn{Table: "accounts", Column: ir.Column{
			Name:     "nickname",
			Type:     probeType(t, "string(64)"),
			Nullable: true,
		}},
		ir.AddIndex{Table: "accounts", Name: "idx_accounts_nickname", Columns: []string{"nickname"}},
		ir.AddColumn{Table: "no_such_table", Column: ir.Column{
			Name:     "orphan",
			Type:     probeType(t, "int32"),
			Nullable: true,
		}},
	}
}

func TestAFailedMigrationLeavesTheSchemaExactlyAsItWas(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)

			session := sessionIn(t, dsn, target)
			observer := sessionIn(t, dsn, target)

			empty := describeSchema(t, observer, target)

			if err := engine.Apply(t.Context(), session, compileAll(t, groundwork(t))); err != nil {
				t.Fatalf("the groundwork migration failed: %v", err)
			}

			before := describeSchema(t, observer, target)
			if before == empty {
				t.Fatal("a migration that succeeded left the digest unchanged, so this probe cannot " +
					"see the kind of change the failing migration below attempts, and every " +
					"assertion after this point would pass for the wrong reason")
			}

			err := engine.Apply(t.Context(), session, compileAll(t, halfway(t)))
			if err == nil {
				t.Fatal("the deliberately failing migration reported success")
			}

			checkServerRejected(t, err)

			after := describeSchema(t, observer, target)
			if after != before {
				t.Errorf("the schema changed across a failed migration.\nbefore:\n%s\nafter:\n%s",
					before, after)
			}

			t.Log("The two digests are read from a second session, because \"the schema is unchanged\"")
			t.Log("is a claim about what every other connection sees, not about what the migrating")
			t.Log("session can still see inside a transaction it has not ended.")
			t.Logf("The digest is this package's own, not internal/schema's snapshot: that model does "+
				"not exist yet, and %s replaces this with the neutral comparison.", snapshotSupersededBy)
		})
	}
}

// checkServerRejected asserts the failure came from the server rather than from this process.
func checkServerRejected(t *testing.T, err error) {
	t.Helper()

	var failure *engine.StatementError
	if !errors.As(err, &failure) {
		t.Fatalf("the migration failed with %v, which names no statement", err)
	}

	if failure.Position != 3 {
		t.Errorf("the failure reports statement %d, want the third", failure.Position)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("the migration failed with %v, which is not a server error -- a compile refusal "+
			"or any client-side failure would leave the server never having entered an aborted "+
			"transaction, so nothing about rollback would have been exercised", err)
	}

	if pgErr.Code != undefinedTable {
		t.Errorf("the server rejected the statement with SQLSTATE %s, want %s",
			pgErr.Code, undefinedTable)
	}
}

func TestTheStatementsBeforeTheFailureRanAndWereDiscarded(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := sessionIn(t, dsn, target)
	observer := sessionIn(t, dsn, target)

	if err := engine.Apply(t.Context(), session, compileAll(t, groundwork(t))); err != nil {
		t.Fatalf("the groundwork migration failed: %v", err)
	}

	before := describeSchema(t, observer, target)
	var midway string

	err := session.Transact(t.Context(), func(ctx context.Context) error {
		for _, stmt := range compileAll(t, halfway(t)[:2]) {
			if err := session.Exec(ctx, stmt); err != nil {
				return err
			}
		}

		midway = describeSchema(t, session, target)

		return errors.New("this migration decided to fail")
	})

	if err == nil {
		t.Fatal("Transact committed work whose closure returned an error")
	}

	if midway == before {
		t.Error("the statements before the failure changed nothing, so this test would report a " +
			"rollback for a migration that was really a no-op")
	}

	if after := describeSchema(t, observer, target); after != before {
		t.Errorf("the discarded statements survived.\nbefore:\n%s\nafter:\n%s", before, after)
	}

	t.Log("This is what separates a rollback from a migration that never did anything. The middle")
	t.Log("digest is read on the migrating session, which is the only one that can see uncommitted")
	t.Log("work; the last is read on another, which is the only one whose answer is the claim.")
}

func TestTheSessionIsStillUsableAfterAFailedMigration(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := sessionIn(t, dsn, target)

	if err := engine.Apply(t.Context(), session, compileAll(t, groundwork(t))); err != nil {
		t.Fatalf("the groundwork migration failed: %v", err)
	}

	if err := engine.Apply(t.Context(), session, compileAll(t, halfway(t))); err == nil {
		t.Fatal("the deliberately failing migration reported success")
	}

	if err := engine.Apply(t.Context(), session, compileAll(t, ir.Operations{
		ir.CreateTable{Table: "after", Columns: []ir.Column{{Name: "id", Type: probeType(t, "int64")}}},
	})); err != nil {
		t.Fatalf("the session did not survive the failed migration: %v", err)
	}

	t.Log("The rollback ends the aborted transaction, so the session is idle again rather than")
	t.Log("refusing every statement with 25P02. M4-T5 runs a queue on one session and needs this.")
}

// unmade is never made, so writing to it faults the way an ordinary bug in a migration would.
//
// The fault has to come from a real one: forbidigo forbids calling panic anywhere outside cmd/ and
// internal/cli/, test files included, and rightly -- a rollback path that only survives a panic
// this file raised on purpose would prove less than one that survives the kind that actually
// happens.
var unmade map[string]string

// faultPartway runs a migration that faults after its first statement, returning what the fault was.
func faultPartway(t *testing.T, session *pgConn) (fault any) {
	t.Helper()

	defer func() { fault = recover() }()

	_ = session.Transact(t.Context(), func(ctx context.Context) error {
		for _, stmt := range compileAll(t, groundwork(t)) {
			if err := session.Exec(ctx, stmt); err != nil {
				t.Fatalf("Exec: %v", err)
			}
		}

		unmade["accounts"] = "created just above"

		return nil
	})

	return fault
}

func TestAMigrationThatFaultsPartwayIsDiscardedRatherThanCommitted(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := sessionIn(t, dsn, target)
	observer := sessionIn(t, dsn, target)

	before := describeSchema(t, observer, target)

	if fault := faultPartway(t, session); fault == nil {
		t.Fatal("the work returned instead of faulting, so the rollback path this test is about " +
			"was never taken and the assertions below would pass for the wrong reason")
	}

	if after := describeSchema(t, observer, target); after != before {
		t.Errorf("a faulted migration was committed.\nbefore:\n%s\nafter:\n%s", before, after)
		t.Log("A closure that never returns leaves the deferred end with no error to see, so the")
		t.Log("commit decision cannot rest on the error alone -- it rests on whether the work")
		t.Log("returned at all. Otherwise a panic mid-migration commits everything before it.")
	}

	if err := engine.Apply(t.Context(), session, compileAll(t, groundwork(t))); err != nil {
		t.Errorf("the session did not survive the fault: %v", err)
	}
}

func TestACommitTheServerDiscardsIsReportedAsTheFailureItIs(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session := openSession(t, dsn)

	err := session.Transact(t.Context(), func(ctx context.Context) error {
		_, failure := session.conn.Exec(ctx, "ALTER TABLE no_such_table ADD COLUMN x integer")
		if failure == nil {
			t.Fatal("the deliberate failure succeeded")
		}

		return nil
	})

	if !errors.Is(err, ErrCommitDiscarded) {
		t.Log("PostgreSQL answers COMMIT on an aborted transaction with the command tag ROLLBACK.")
		t.Log("Taken for success, a migration whose work swallowed an error reports as applied")
		t.Log("while the server kept none of it -- which is the worst outcome this task can have.")
		t.Fatalf("committing an aborted transaction returned %v, want ErrCommitDiscarded", err)
	}
}

func TestASecondTransactionOnALiveSessionIsRefused(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session := openSession(t, dsn)

	err := session.Transact(t.Context(), func(ctx context.Context) error {
		return session.Transact(ctx, func(context.Context) error { return nil })
	})

	if !errors.Is(err, ErrTransactionOpen) {
		t.Fatalf("a nested Transact returned %v, want ErrTransactionOpen", err)
	}
}

func TestATransactionOpenedBehindTheDriversBackIsRefused(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session := openSession(t, dsn)

	tx, err := session.conn.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	defer func() {
		if err := tx.Rollback(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Rollback: %v", err)
		}
	}()

	transactErr := session.Transact(t.Context(), func(context.Context) error {
		t.Error("the work ran inside a transaction this driver did not open")

		return nil
	})

	if !errors.Is(transactErr, ErrTransactionOpen) {
		t.Fatalf("Transact returned %v, want ErrTransactionOpen", transactErr)
	}

	t.Log("PostgreSQL answers a second BEGIN with a warning and does nothing, so without this")
	t.Log("guard the driver's commit would commit the outer transaction -- the exact accident")
	t.Log("that would let a test helper's rolled-back transaction be made permanent instead.")
}

func TestTheMigrationLockOutlivesTheTransactionItBrackets(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	session := sessionIn(t, dsn, target)
	observer := sessionIn(t, dsn, target)

	unlock, err := session.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	held := func(when string) {
		if countHeld(t, observer, session.key) != 1 {
			t.Errorf("the migration lock was not held %s", when)
		}
	}

	held("after the acquire")

	if err := engine.Apply(t.Context(), session, compileAll(t, groundwork(t))); err != nil {
		t.Fatalf("the groundwork migration failed: %v", err)
	}

	held("after a committed migration")

	if err := engine.Apply(t.Context(), session, compileAll(t, halfway(t))); err == nil {
		t.Fatal("the deliberately failing migration reported success")
	}

	held("after a rolled-back migration")

	if err := unlock(t.Context()); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	t.Log("A session advisory lock is documented as untied to transactions, and this repository")
	t.Log("holds that to the same standard as M3-T4's lock_timeout finding: measured, not read.")
	t.Log("If a rollback released it, every migration after the first failure in a run would")
	t.Log("proceed unprotected.")
}

// countHeld reports how many granted advisory locks a key has, seen from another session.
func countHeld(t *testing.T, observer *pgConn, key lockKey) int {
	t.Helper()

	var held int

	err := observer.conn.QueryRow(t.Context(),
		`SELECT count(*) FROM pg_locks
		  WHERE locktype = 'advisory' AND classid = $1 AND objid = $2 AND granted`,
		key.class, key.obj).Scan(&held)
	if err != nil {
		t.Fatalf("reading pg_locks: %v", err)
	}

	return held
}

func TestReleasingTheLockInsideAnAbortedTransactionNamesTheRealCause(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session := openSession(t, dsn)

	unlock, err := session.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	var releaseErr error

	transactErr := session.Transact(t.Context(), func(ctx context.Context) error {
		_, failure := session.conn.Exec(ctx, "ALTER TABLE no_such_table ADD COLUMN x integer")
		if failure == nil {
			t.Fatal("the deliberate failure succeeded")
		}

		releaseErr = unlock(ctx)

		return failure
	})

	if transactErr == nil {
		t.Fatal("Transact committed a transaction the server had aborted")
	}

	if !errors.Is(releaseErr, ErrLockRelease) {
		t.Fatalf("releasing inside an aborted transaction returned %v, want ErrLockRelease", releaseErr)
	}

	if !strings.Contains(releaseErr.Error(), "aborted transaction") {
		t.Errorf("the failure does not name the aborted transaction: %v", releaseErr)
		t.Log("PostgreSQL answers every command in an aborted transaction with 25P02, so without")
		t.Log("the check this reads as \"releasing the migration lock failed\" -- a report about the")
		t.Log("lock, for a problem that is not about the lock.")
	}

	if err := unlock(context.WithoutCancel(t.Context())); err != nil {
		t.Errorf("releasing after the rollback: %v", err)
	}

	t.Log("Locking before beginning, and deferring in that order, makes the rollback run first and")
	t.Log("this path unreachable. The check is for the day someone releases early instead.")
}
