//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// historyTable is deliberately not migr_migrations: a store that ignored Options.HistoryTable and
// hard-coded the default would pass every assertion in this file if both halves agreed on it.
const historyTable = "runs_of_migrations"

// ran is the instant these fixtures record, aligned to a microsecond because PostgreSQL stores
// timestamptz to that resolution and Go holds nanoseconds. forbidigo bans time.Now here anyway.
var ran = time.Date(2026, time.July, 28, 15, 30, 0, 123456000, time.UTC)

// historyIn opens a session pointed at one schema, with a history store bound to historyTable.
func historyIn(t *testing.T, dsn, schema string) *pgConn {
	t.Helper()

	conn := open(t, dsn, driver.Options{Schema: schema, HistoryTable: historyTable})

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", conn)
	}

	return session
}

// ensured gives one test an empty history table of its own, and a session pointed at it.
func ensured(t *testing.T, dsn string) (*pgConn, string) {
	t.Helper()

	target := scratchSchema(t, dsn)
	session := historyIn(t, dsn, target)

	if err := session.History().Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	return session, target
}

// distinctNumbers is why no fixture below carries a batch, ordinal, or statement index of one.
//
// IRVersion is always 1, so an ordinal of 1 would let an ordinal/ir_version transposition
// round-trip perfectly -- and a fifteen-placeholder insert read back through the same ordered
// column list cannot see a symmetric swap at all. Only reading columns by name can.
const distinctNumbers = "every number in a fixture is distinct, and none of them is one"

// run builds a record whose numbers obey distinctNumbers.
func run(t *testing.T, batch int64, ordinal int32, version string) history.Record {
	t.Helper()

	sum, err := checksum.Of([]byte("-- migration "+version), ir.Operations{ir.DropTable{Table: "t"}})
	if err != nil {
		t.Fatalf("checksum.Of: %v", err)
	}

	return history.Record{
		Batch:     batch,
		Ordinal:   ordinal,
		Version:   version,
		Name:      "add_" + version,
		Direction: ir.Up,
		Checksum:  sum,
		AppliedAt: ran,
		Duration:  1234 * time.Millisecond,
		Driver:    Name,
		Runtime:   "golang",
	}
}

func TestTheHistoryTableIsCreatedAndSeenFromAnotherSession(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			session, target := ensured(t, dsn)

			observer := historyIn(t, dsn, target)
			checkCreatedTable(t, observer, target)

			if err := session.History().Ensure(t.Context()); err != nil {
				t.Errorf("a second Ensure did not report the table it had just created: %v", err)
			}

			if err := observer.History().Ensure(t.Context()); err != nil {
				t.Errorf("another session did not see the created table: %v", err)
			}
		})
	}
}

// checkCreatedTable holds what Ensure created to what the neutral schema declares.
func checkCreatedTable(t *testing.T, session *pgConn, schema string) {
	t.Helper()

	digest := describeSchema(t, session, schema)

	for _, column := range declaredColumns(historyTable) {
		if !strings.Contains(digest, historyTable+"."+column+" ") {
			t.Errorf("the created table has no column %q:\n%s", column, digest)
		}
	}

	if !strings.Contains(digest, "PRIMARY KEY (batch, ordinal)") {
		t.Errorf("the created table has no (batch, ordinal) primary key:\n%s", digest)
		t.Log("Asserting only that Ensure returned nil would pass for CREATE TABLE runs (x int).")
	}

	if !strings.Contains(digest, historyTable+"_version_idx") {
		t.Errorf("the created table has no version index:\n%s", digest)
		t.Log("The index and the table are created in one transaction precisely so this cannot")
		t.Log("happen: without it, a failure between them leaves a table every later Ensure")
		t.Log("reports as present, and the index is never created at all.")
	}
}

func TestARunSurvivesTheRoundTripToAnotherSession(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			session, target := ensured(t, dsn)

			want := run(t, 42, 9, "20260728T120000")
			if err := session.History().Record(t.Context(), want); err != nil {
				t.Fatalf("Record: %v", err)
			}

			observer := historyIn(t, dsn, target)

			records, err := observer.History().List(t.Context())
			if err != nil {
				t.Fatalf("List: %v", err)
			}

			if len(records) != 1 {
				t.Fatalf("another session read %d runs, want the one that was recorded", len(records))
			}

			checkSameRun(t, records[0], want.Stamp())
			checkColumnsByName(t, observer, want)
		})
	}
}

// checkSameRun holds a record read back to the one written, field by field.
func checkSameRun(t *testing.T, got, want history.Record) {
	t.Helper()

	if !got.AppliedAt.Equal(want.AppliedAt) {
		t.Errorf("applied_at read back as %v, want %v", got.AppliedAt, want.AppliedAt)
	}

	got.AppliedAt, want.AppliedAt = time.Time{}, time.Time{}
	want.Outcome = history.Applied

	if got != want {
		t.Errorf("the run read back as\n  %+v\nwant\n  %+v", got, want)
	}
}

// checkColumnsByName reads three columns by name, which a symmetric round trip cannot check.
func checkColumnsByName(t *testing.T, session *pgConn, want history.Record) {
	t.Helper()

	var driverName, runtime string
	var ordinal, lastStatement int32
	var blank bool

	err := session.conn.QueryRow(t.Context(),
		`SELECT "driver", "runtime", "ordinal", "last_statement", "error" IS NULL FROM `+
			quote(historyTable)).
		Scan(&driverName, &runtime, &ordinal, &lastStatement, &blank)
	if err != nil {
		t.Fatalf("reading the row by column name: %v", err)
	}

	if driverName != want.Driver || runtime != want.Runtime || ordinal != want.Ordinal {
		t.Errorf("the row holds driver=%q runtime=%q ordinal=%d, want %q %q %d",
			driverName, runtime, ordinal, want.Driver, want.Runtime, want.Ordinal)
		t.Log("Reading back through the same ordered column list the insert bound cannot see a")
		t.Log("transposition: both halves would agree on the wrong order. Only naming columns can,")
		t.Logf("and only when %s.", distinctNumbers)
	}

	if lastStatement != 0 {
		t.Errorf("an applied run recorded last_statement %d, want 0", lastStatement)
	}

	if !blank {
		t.Error("an applied run wrote an empty string where the column allows NULL")
		t.Log("error is the one nullable column. Writing '' makes that declaration decorative and")
		t.Log("makes a later `WHERE error IS NULL` find none of the runs that succeeded.")
	}
}

func TestAStoreOverwritesTheFieldsItsMethodOwns(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session, _ := ensured(t, dsn)

	rec := run(t, 7, 3, "20260728T130000")
	rec.Outcome = history.Dirty
	rec.Error = "a caller filled this in and should not have"
	rec.LastStatement = 5

	if err := session.History().Record(t.Context(), rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	stored, _, err := session.History().Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	if stored.Outcome != history.Applied || stored.Error != "" || stored.LastStatement != 0 {
		t.Errorf("Record kept the caller's outcome %q, error %q, and last statement %d",
			stored.Outcome, stored.Error, stored.LastStatement)
		t.Log("Record writes Applied and nothing else can. Outcome, Error, and LastStatement are")
		t.Log("read-side fields the store owns on write, so a caller cannot record a failure")
		t.Log("marked applied -- which is a doc sentence until something asserts it.")
	}
}

func TestABuildStampsItselfOntoEveryRunItRecords(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session, _ := ensured(t, dsn)

	rec := run(t, 5, 4, "20260728T140000")
	rec.EngineVersion = ""
	rec.IRVersion = 0

	if err := session.History().Record(t.Context(), rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	stored, _, err := session.History().Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	if stored.EngineVersion == "" || stored.IRVersion == 0 {
		t.Errorf("the run recorded engine_version %q and ir_version %d",
			stored.EngineVersion, stored.IRVersion)
		t.Log("Stamp has a value receiver returning a Record, so writing rec.Stamp() as a bare")
		t.Log("statement compiles, does nothing, and no linter in this repository catches it.")
		t.Log("This is the assertion that does.")
	}
}

func TestAllThreeOutcomesAreRecordedAndReadBack(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session, _ := ensured(t, dsn)
	store := session.History()

	failure := errors.New("the server refused a statement")

	if err := store.Record(t.Context(), run(t, 3, 2, "20260728T150000")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if err := store.MarkFailed(t.Context(), run(t, 3, 4, "20260728T160000"), failure); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	if err := store.MarkDirty(t.Context(), run(t, 3, 6, "20260728T170000"), 7, failure); err != nil {
		t.Fatalf("MarkDirty: %v", err)
	}

	records, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 3 {
		t.Fatalf("read %d runs, want 3", len(records))
	}

	want := []history.Outcome{history.Applied, history.Failed, history.Dirty}
	for i, outcome := range want {
		if records[i].Outcome != outcome {
			t.Errorf("run %d ended %q, want %q", i+1, records[i].Outcome, outcome)
		}
	}

	if records[1].Error != failure.Error() || records[1].LastStatement != 0 {
		t.Errorf("the failed run recorded error %q and last statement %d",
			records[1].Error, records[1].LastStatement)
	}

	if records[2].LastStatement != 7 {
		t.Errorf("the dirty run stopped at statement %d, want 7", records[2].LastStatement)
		t.Log("last_statement is zero on every applied and failed run, so the dirty path is the")
		t.Log("only one that proves the column is written at all. M9-T6 resumes from this index.")
	}
}

func TestTheLogIsOrderedByTheKeyAndNotByTheClockOrByInsertion(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session, _ := ensured(t, dsn)
	store := session.History()

	later := run(t, 2, 1, "20260728T190000")
	later.AppliedAt = ran.Add(-time.Hour)

	earlier := run(t, 1, 1, "20260728T180000")
	earlier.AppliedAt = ran.Add(time.Hour)

	if err := store.Record(t.Context(), later); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if err := store.Record(t.Context(), earlier); err != nil {
		t.Fatalf("Record: %v", err)
	}

	records, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 2 || records[0].Batch != 1 || records[1].Batch != 2 {
		t.Fatalf("the log reads back in the order it was inserted, not the order the runs happened")
	}

	latest, found, err := store.Latest(t.Context())
	if err != nil || !found {
		t.Fatalf("Latest: %v (found %v)", err, found)
	}

	if latest.Batch != 2 {
		t.Errorf("the most recent run is batch %d, want 2", latest.Batch)
		t.Log("Batch 2 was applied an hour before batch 1, so a reader keyed on applied_at picks")
		t.Log("the wrong one. Two runs can also share a millisecond, and down reverses the last")
		t.Log("batch -- so a tie there rolls two migrations back in the wrong order (ADR 0013).")
	}

	t.Log("The rows are inserted out of order on purpose: on a small fresh table PostgreSQL")
	t.Log("returns heap order, so an ORDER BY dropped entirely would be invisible otherwise.")
}

func TestAnEmptyHistoryIsNotTheSameAsNoHistoryTable(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session, _ := ensured(t, dsn)

	rec, found, err := session.History().Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest on an empty table: %v", err)
	}

	if found || rec != (history.Record{}) {
		t.Errorf("an empty history reported a run: %+v", rec)
	}

	records, err := session.History().List(t.Context())
	if err != nil || len(records) != 0 {
		t.Fatalf("List on an empty table returned %d runs and %v", len(records), err)
	}
}

func TestReadingAHistoryTableNothingCreatedIsAnError(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)
	session := historyIn(t, dsn, target)

	if _, err := session.History().List(t.Context()); !errors.Is(err, history.ErrNoTable) {
		t.Errorf("List against a missing table returned %v, want history.ErrNoTable", err)
	}

	if _, _, err := session.History().Latest(t.Context()); !errors.Is(err, history.ErrNoTable) {
		t.Errorf("Latest against a missing table returned %v, want history.ErrNoTable", err)
	}

	if err := session.History().Record(t.Context(), run(t, 1, 1, "20260728T200000")); err == nil {
		t.Error("a run was recorded into a table nothing created")
	}

	t.Log("Nothing validates history.table, so a typo names a table that does not exist. Read as")
	t.Log("\"no runs yet\", a fully migrated production database looks virgin and the next up")
	t.Log("re-runs every migration it had already applied.")
}

func TestARunCannotBeRecordedTwiceUnderOneKey(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session, _ := ensured(t, dsn)

	rec := run(t, 8, 2, "20260728T210000")
	if err := session.History().Record(t.Context(), rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	err := session.History().Record(t.Context(), run(t, 8, 2, "20260728T220000"))
	if !errors.Is(err, ErrAlreadyRecorded) {
		t.Fatalf("recording a second run at batch 8 ordinal 2 returned %v, want ErrAlreadyRecorded", err)
	}

	t.Log("The log is append-only, so a key collision would replace the evidence of a run rather")
	t.Log("than add one. M4-T5 allocates the batch; this is what tells it when it got that wrong.")
}

func TestARowThisBuildCannotDecodeIsReportedAsCorruptRatherThanAsDrift(t *testing.T) {
	cases := []struct {
		field  string
		column string
		value  string
	}{
		{field: "direction", column: "direction", value: "sideways"},
		{field: "outcome", column: "outcome", value: "maybe"},
		{field: "checksum", column: "checksum", value: "not-a-checksum"},
	}

	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			dsn := dsnFor(servers[0].env, servers[0].dsn)
			session, _ := ensured(t, dsn)

			if err := session.History().Record(t.Context(), run(t, 6, 5, "20260728T230000")); err != nil {
				t.Fatalf("Record: %v", err)
			}

			_, err := session.conn.Exec(t.Context(),
				`UPDATE `+quote(historyTable)+` SET `+quote(c.column)+` = $1`, c.value)
			if err != nil {
				t.Fatalf("corrupting the row: %v", err)
			}

			_, err = session.History().List(t.Context())

			var corrupt *history.CorruptError
			if !errors.As(err, &corrupt) {
				t.Fatalf("List returned %v, which is not a *history.CorruptError", err)
			}

			if corrupt.Field != c.field {
				t.Errorf("the corrupt row blames %q, want %q", corrupt.Field, c.field)
			}

			if corrupt.Batch != 6 || corrupt.Ordinal != 5 {
				t.Errorf("the corrupt row is reported at batch %d ordinal %d, want 6 and 5",
					corrupt.Batch, corrupt.Ordinal)
				t.Log("Batch and ordinal are scanned before anything that can fail to decode, so")
				t.Log("the failure can name the row an operator has to go and repair.")
			}

			if errors.Is(err, checksum.ErrMalformed) != (c.field == "checksum") {
				t.Errorf("the cause under the corrupt row is wrong: %v", err)
			}
		})
	}

	t.Log("A corrupt row is not drift. Drift is exit code 4 and means someone edited an applied")
	t.Log("migration; an unreadable row means this table needs manual repair (ADR 0013).")
}

func TestARowFromANewerBuildIsStillReadable(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session, _ := ensured(t, dsn)

	if err := session.History().Record(t.Context(), run(t, 2, 2, "20260728T235000")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	_, err := session.conn.Exec(t.Context(), `UPDATE `+quote(historyTable)+
		` SET "ir_version" = 99, "engine_version" = 'from-the-future', "last_statement" = 4`)
	if err != nil {
		t.Fatalf("aging the row: %v", err)
	}

	if _, err := session.conn.Exec(t.Context(),
		`ALTER TABLE `+quote(historyTable)+` ADD COLUMN "added_later" text`); err != nil {
		t.Fatalf("extending the table: %v", err)
	}

	records, err := session.History().List(t.Context())
	if err != nil {
		t.Fatalf("a row a newer build could have written is unreadable: %v", err)
	}

	if len(records) != 1 || records[0].IRVersion != 99 {
		t.Fatalf("the row read back as %+v", records)
	}

	if err := session.History().Ensure(t.Context()); err != nil {
		t.Errorf("Ensure refused a table a newer build had extended: %v", err)
	}

	t.Log("Record.Validate is a write-side precondition and is deliberately not run on read: it")
	t.Log("refuses last_statement on a non-dirty run, so applying it here would make a row this")
	t.Log("build merely does not understand unreadable -- and migr history is the command an")
	t.Log("operator runs to inspect exactly that table.")
}

func TestARunThatIsNotAWholeRunIsRefusedBeforeItIsWritten(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	session, _ := ensured(t, dsn)

	incomplete := run(t, 9, 8, "20260729T090000")
	incomplete.Name = ""

	err := session.History().Record(t.Context(), incomplete)
	if !errors.Is(err, history.ErrIncomplete) {
		t.Fatalf("recording a nameless run returned %v, want history.ErrIncomplete", err)
	}

	records, listErr := session.History().List(t.Context())
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}

	if len(records) != 0 {
		t.Errorf("the refused run was written anyway: %+v", records)
	}

	t.Log("Validate runs before the insert, so a record missing what a run must carry never")
	t.Log("reaches the table. A blank engine_version is the case that matters most: it means the")
	t.Log("write path stopped stamping, and the column would record nothing forever after.")
}

func TestAHalfCreatedHistoryTableIsNotLeftBehind(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)
	session := historyIn(t, dsn, target)

	if _, err := session.conn.Exec(t.Context(),
		`CREATE TABLE `+quote(historyTable+"_version_idx")+` (x integer)`); err != nil {
		t.Fatalf("occupying the index name: %v", err)
	}

	if err := session.History().Ensure(t.Context()); err == nil {
		t.Fatal("Ensure created the index over a name another relation already held")
	}

	kind, found, err := pgHistory{session: session, table: historyTable}.relkind(t.Context())
	if err != nil {
		t.Fatalf("looking for the table: %v", err)
	}

	if found {
		t.Errorf("the table survived a create whose index failed, as a %q", kind)
		t.Log("Every later Ensure would then report it present and the index would never be")
		t.Log("created at all -- a silent, permanent half-creation. The two statements run as one")
		t.Log("unit precisely so a retry is idempotent rather than merely a second attempt.")
	}
}

func TestEnsureRefusesANameHeldBySomethingThatIsNotATable(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)
	session := historyIn(t, dsn, target)

	if _, err := session.conn.Exec(t.Context(),
		`CREATE VIEW `+quote(historyTable)+` AS SELECT 1 AS x`); err != nil {
		t.Fatalf("creating the view: %v", err)
	}

	err := session.History().Ensure(t.Context())
	if !errors.Is(err, ErrHistoryNotATable) {
		t.Fatalf("Ensure on a view returned %v, want ErrHistoryNotATable", err)
	}
}

func TestEnsureRefusesATableMissingAColumnThisBuildWrites(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)
	session := historyIn(t, dsn, target)

	if _, err := session.conn.Exec(t.Context(),
		`CREATE TABLE `+quote(historyTable)+` ("batch" bigint, "ordinal" integer)`); err != nil {
		t.Fatalf("creating the table: %v", err)
	}

	err := session.History().Ensure(t.Context())
	if !errors.Is(err, ErrHistoryShape) {
		t.Fatalf("Ensure on a table missing columns returned %v, want ErrHistoryShape", err)
	}

	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("the refusal does not name a column that is missing: %v", err)
		t.Log("Without this the first write of a real migration fails with a bare 42703, at the")
		t.Log("worst possible moment, about a table Ensure had already reported as fine.")
	}
}

func TestAMixedCaseHistoryTableIsFoundOnceItExists(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)

	conn := open(t, dsn, driver.Options{Schema: target, HistoryTable: "MigrRuns"})

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", conn)
	}

	if err := session.History().Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if err := session.History().Ensure(t.Context()); err != nil {
		t.Fatalf("a second Ensure did not find the table the first created: %v", err)
	}

	t.Log("to_regclass folds an unquoted name to lower case, so without quote_ident this table")
	t.Log("would report absent forever and Ensure would try to create it on every single run.")
}

func TestTheHistoryTableIsCreatedWholeOrNotAtAll(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	target := scratchSchema(t, dsn)
	session := historyIn(t, dsn, target)
	observer := historyIn(t, dsn, target)

	err := session.Transact(t.Context(), func(ctx context.Context) error {
		if err := session.History().Ensure(ctx); err != nil {
			return err
		}

		return errors.New("this migration decided to fail after ensuring the history table")
	})

	if err == nil {
		t.Fatal("Transact committed work whose closure returned an error")
	}

	if err := observer.History().Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure inside a rolled-back transaction left something behind: %v", err)
	}

	records, err := observer.History().List(t.Context())
	if err != nil || len(records) != 0 {
		t.Fatalf("the recreated table is not empty: %d runs, %v", len(records), err)
	}

	t.Log("Ensure runs inside the caller's transaction when there is one rather than opening a")
	t.Log("second, so a rollback discards it whole. That is also why it is a setup step taken")
	t.Log("before the migration transaction opens, and under the migration lock.")
}
