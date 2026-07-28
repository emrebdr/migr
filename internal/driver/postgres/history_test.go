package postgres

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
)

// longName builds a table name of a given length, for the identifier limits.
func longName(n int) string {
	return strings.Repeat("h", n)
}

func TestAHistoryTableNameTooLongForItsIndexIsRefused(t *testing.T) {
	const derived = len("_version_idx")

	cases := []struct {
		name    string
		table   string
		refused bool
	}{
		{name: "the default", table: driver.DefaultHistoryTable, refused: false},
		{name: "the longest usable", table: longName(maxIdentifier - derived), refused: false},
		{name: "one over", table: longName(maxIdentifier - derived + 1), refused: true},
		{name: "under 63 but over the index limit", table: longName(maxIdentifier - 1), refused: true},
		{name: "over an identifier itself", table: longName(maxIdentifier + 1), refused: true},
		{name: "holding a NUL", table: "migr\x00migrations", refused: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(driver.Options{HistoryTable: c.table})

			if c.refused && err == nil {
				t.Fatalf("New accepted a %d-byte history table PostgreSQL cannot carry", len(c.table))
			}

			if !c.refused && err != nil {
				t.Fatalf("New refused a usable history table of %d bytes: %v", len(c.table), err)
			}
		})
	}

	t.Logf("The usable length is %d, not %d: history.Schema names the index after the table, and",
		maxIdentifier-derived, maxIdentifier)
	t.Log("PostgreSQL truncates an identifier with a notice rather than an error -- so a longer")
	t.Log("name creates the table and silently loses the index, or collides with another one.")
}

func TestRefusingATableNameNamesTheDerivedIndex(t *testing.T) {
	_, err := New(driver.Options{HistoryTable: longName(maxIdentifier - 1)})
	if err == nil {
		t.Fatal("New accepted a table name too long for its index")
	}

	if !errors.Is(err, ErrHistory) {
		t.Errorf("New failed with %v, which does not report ErrHistory", err)
	}

	if !strings.Contains(err.Error(), "index is named after the table") {
		t.Errorf("the refusal does not say why the limit is shorter than an identifier's: %v", err)
		t.Log("Without it the operator reads a complaint about a field called \"name\" and has no")
		t.Log("way to connect it to the history.table they set.")
	}
}

func TestTheInsertBindsEveryColumnTheRecordCarries(t *testing.T) {
	store := pgHistory{table: "migr_migrations"}

	fields := reflect.TypeOf(history.Record{}).NumField()
	if len(historyColumns) != fields {
		t.Fatalf("this store binds %d columns and a record carries %d fields",
			len(historyColumns), fields)
	}

	stmt := store.insertStatement()

	for i, column := range historyColumns {
		placeholder := "$" + strconv.Itoa(i+1)
		if !strings.Contains(stmt, placeholder) {
			t.Errorf("the insert has no %s, so column %q is never bound", placeholder, column)
		}

		if !strings.Contains(stmt, quote(column)) {
			t.Errorf("the insert names no column %q", column)
		}
	}

	if strings.Contains(stmt, "$"+strconv.Itoa(len(historyColumns)+1)) {
		t.Error("the insert binds more placeholders than there are columns")
	}

	if !strings.Contains(stmt, `INSERT INTO "migr_migrations"`) {
		t.Errorf("the insert does not write to the configured table:\n%s", stmt)
	}
}

func TestTheReadersOrderByTheKeyAndNotByTheClock(t *testing.T) {
	store := pgHistory{table: "migr_migrations"}

	listing := store.selectStatement(runOrder)
	latest := store.selectStatement(latestOrder)

	for _, stmt := range []string{listing, latest} {
		if strings.Contains(stmt, "applied_at\" DESC") || strings.Contains(stmt, "BY \"applied_at") {
			t.Errorf("a reader orders by the clock:\n%s", stmt)
			t.Log("Two runs can share a millisecond, and down reverses the last batch -- so a tie")
			t.Log("there rolls two migrations back in the wrong order. ADR 0013 rejected applied_at")
			t.Log("as the key for exactly this.")
		}

		if !strings.Contains(stmt, "ORDER BY") {
			t.Errorf("a reader states no order at all:\n%s", stmt)
		}
	}

	if !strings.Contains(latest, "LIMIT 1") {
		t.Errorf("Latest reads more than one row:\n%s", latest)
	}

	if strings.Contains(listing, "LIMIT") {
		t.Errorf("List does not read the whole log:\n%s", listing)
	}

	if strings.Contains(listing, "*") {
		t.Errorf("a reader selects every column rather than the ones it scans:\n%s", listing)
		t.Log("SELECT * cannot survive a newer migr adding a sixteenth column: the scan targets")
		t.Log("would shift silently rather than the read simply ignoring what it does not know.")
	}
}

func TestABlankErrorIsRecordedAsNothingRatherThanAsAnEmptyString(t *testing.T) {
	if nullable("") != nil {
		t.Error("a run that completed would write an empty string where the column allows NULL")
		t.Log("error is the one nullable column of the fifteen. Writing '' makes that declaration")
		t.Log("decorative and makes `WHERE error IS NULL` find none of the runs that succeeded.")
	}

	kept := nullable("it failed")
	if kept == nil || *kept != "it failed" {
		t.Error("a run that failed did not record why")
	}

	if text(nil) != "" {
		t.Error("a NULL error did not read back as the empty string a record carries")
	}
}

func TestADurationIsRecordedInTheUnitTheColumnNames(t *testing.T) {
	cases := []struct {
		name     string
		duration time.Duration
		stored   int64
	}{
		{name: "whole milliseconds", duration: 1200 * time.Millisecond, stored: 1200},
		{name: "sub-millisecond truncates down", duration: 1500 * time.Microsecond, stored: 1},
		{name: "under a millisecond stores zero", duration: 900 * time.Microsecond, stored: 0},
		{name: "zero", duration: 0, stored: 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.duration.Milliseconds(); got != c.stored {
				t.Errorf("%v is recorded as %d, want %d", c.duration, got, c.stored)
			}

			restored := time.Duration(c.stored) * time.Millisecond
			if restored.Milliseconds() != c.stored {
				t.Errorf("%d milliseconds read back as %v", c.stored, restored)
			}
		})
	}

	t.Log("duration_ms is what the column is called, so a run measured in microseconds does not")
	t.Log("round-trip. Stating it here is what stops a later reader treating it as a bug.")
}

// serverError builds the failure PostgreSQL raises for one SQLSTATE, for the classifiers.
func serverError(code string) error {
	return &pgconn.PgError{Code: code, Message: "from a test"}
}

func TestAMissingHistoryTableIsNeverReadAsADatabaseWithNoRuns(t *testing.T) {
	store := pgHistory{table: "migr_migrations"}

	err := store.classifyRead(serverError(undefinedTableCode))
	if !errors.Is(err, ErrNoHistoryTable) {
		t.Fatalf("reading a missing table returned %v, want ErrNoHistoryTable", err)
	}

	if !strings.Contains(err.Error(), "migr_migrations") {
		t.Errorf("the failure does not name the table it looked for: %v", err)
	}

	t.Log("This is the worst outcome this store can produce and it is one errors.Is away: a")
	t.Log("history.table typo naming a table nobody created would otherwise decode as zero rows,")
	t.Log("a fully migrated production database would read as virgin, and the next up would")
	t.Log("re-run every migration it had already applied.")
}

func TestTheSameSQLSTATEMeansDifferentThingsInDifferentPlaces(t *testing.T) {
	store := pgHistory{table: "migr_migrations", session: &pgConn{}}
	rec := history.Record{Version: "20260728T120000", Batch: 4, Ordinal: 2}

	raced := store.classifyCreate(serverError(uniqueViolation))
	if !errors.Is(raced, ErrHistoryRace) {
		t.Errorf("a unique violation while creating returned %v, want ErrHistoryRace", raced)
	}

	if errors.Is(raced, ErrAlreadyRecorded) {
		t.Error("a lost create race was reported as a duplicate run")
	}

	written := store.classifyWrite(rec, serverError(uniqueViolation))
	if !errors.Is(written, ErrAlreadyRecorded) {
		t.Errorf("a unique violation while recording returned %v, want ErrAlreadyRecorded", written)
	}

	if !strings.Contains(written.Error(), "batch 4 ordinal 2") {
		t.Errorf("the duplicate does not name the run it collided with: %v", written)
	}

	t.Log("A concurrent CREATE TABLE does not raise 42P07 when the winner commits mid-flight: the")
	t.Log("loser blocks on a pg_catalog unique index and raises 23505, measured on both servers.")
	t.Log("One SQLSTATE table for the whole store would report a duplicate history row that does")
	t.Log("not exist. The two never share a statement, so per-call-site classification is exact.")
}

func TestACreateThatLostTheRaceNamesTheLockThatPreventsIt(t *testing.T) {
	store := pgHistory{table: "migr_migrations", session: &pgConn{}}

	unlocked := store.classifyCreate(serverError(duplicateTable))
	if !strings.Contains(unlocked.Error(), "migration lock") {
		t.Errorf("the race does not name the lock that prevents it: %v", unlocked)
		t.Log("The advisory key is derived from the schema and table names, so it needs no table")
		t.Log("to exist -- the lock is available on a database with no history at all.")
	}

	if strings.Contains(unlocked.Error(), "different key") {
		t.Errorf("a race without the lock reports the diagnosis for one under it: %v", unlocked)
		t.Log("Telling an operator who never took the lock that their two migr.yaml files disagree")
		t.Log("sends them to look at configuration when the fix is to take the lock.")
	}

	store.session.locked = true

	locked := store.classifyCreate(serverError(duplicateTable))
	if !strings.Contains(locked.Error(), "different key") {
		t.Errorf("a race under the lock reports the same diagnosis as one without it: %v", locked)
		t.Log("Two processes racing while both hold a lock means they locked on different keys,")
		t.Log("which is a configuration mismatch and an entirely different fix.")
	}
}

func TestAWriteAgainstAnUnusableTableSaysWhichWayItIsUnusable(t *testing.T) {
	store := pgHistory{table: "migr_migrations"}
	rec := history.Record{Version: "20260728T120000"}

	missing := store.classifyWrite(rec, serverError(undefinedTableCode))
	if !errors.Is(missing, ErrNoHistoryTable) {
		t.Errorf("writing to a missing table returned %v, want ErrNoHistoryTable", missing)
	}

	shaped := store.classifyWrite(rec, serverError(undefinedColumn))
	if !errors.Is(shaped, ErrHistoryShape) {
		t.Errorf("writing a column the table lacks returned %v, want ErrHistoryShape", shaped)
	}

	other := store.classifyWrite(rec, serverError("53300"))
	if errors.Is(other, ErrNoHistoryTable) || errors.Is(other, ErrHistoryShape) ||
		errors.Is(other, ErrAlreadyRecorded) {
		t.Errorf("an unrelated server failure was classified as one of ours: %v", other)
	}

	if !strings.Contains(other.Error(), "20260728T120000") {
		t.Errorf("the failure does not name the migration it was recording: %v", other)
	}
}

func TestMarkingARunThatDidNotCompleteNeedsTheFailure(t *testing.T) {
	store := pgHistory{table: "migr_migrations"}

	if err := store.checkCause("MarkFailed", nil); err == nil {
		t.Error("a run was marked failed with nothing to say why")
	} else if !strings.Contains(err.Error(), "MarkFailed") {
		t.Errorf("the refusal does not name the method that made it: %v", err)
		t.Log("Validate would otherwise blame the error field, which is true and names the wrong")
		t.Log("culprit: the caller passed a nil cause, they did not fill in a field wrongly.")
	}

	if err := store.checkCause("MarkDirty", errors.New("boom")); err != nil {
		t.Errorf("a run marked dirty with a cause was refused: %v", err)
	}
}

func TestTheDeclaredColumnsComeFromTheNeutralSchema(t *testing.T) {
	declared := declaredColumns("migr_migrations")

	if len(declared) != len(historyColumns) {
		t.Fatalf("the schema declares %d columns and this store binds %d",
			len(declared), len(historyColumns))
	}

	for i, name := range declared {
		if historyColumns[i] != name {
			t.Errorf("column %d: the schema declares %q and this store binds %q",
				i, name, historyColumns[i])
			t.Log("The two lists are written down separately on purpose, so a column added to the")
			t.Log("record has to be added twice and neither half can drift alone. They must agree.")
		}
	}
}

func TestATableMissingAColumnIsNamedAndExtrasAreNot(t *testing.T) {
	declared := declaredColumns("migr_migrations")

	present := make(map[string]bool, len(declared)+1)
	for _, name := range declared {
		present[name] = true
	}

	present["something_a_newer_migr_added"] = true

	if missing := missingColumns(declared, present); len(missing) != 0 {
		t.Errorf("a table with an extra column was reported as missing %v", missing)
		t.Log("Requiring exactly these columns rather than at least these would make a table a")
		t.Log("newer migr extended unreadable by this build, for no gain.")
	}

	delete(present, "checksum")
	delete(present, "last_statement")

	missing := missingColumns(declared, present)
	if len(missing) != 2 || missing[0] != "checksum" || missing[1] != "last_statement" {
		t.Errorf("the missing columns are %v, want checksum and last_statement in that order", missing)
	}
}
