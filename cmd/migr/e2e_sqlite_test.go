package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/sqlite"
	"github.com/emrebdr/migr/internal/driver/sqlite/sqlitetest"
	"github.com/emrebdr/migr/internal/history"
)

// The whole stack against a real database, untagged, because this engine needs no daemon.
//
// ADR 0050: a tag names a requirement, and SQLite's is a t.TempDir(). So this is the first
// end-to-end CLI round trip inside make check and on both CI legs -- PostgreSQL's equivalent is
// reachable only through the Docker job.
//
// Each case gets a database and a history table of its own. M7-T8 found out why: a mutated migration
// is applied *and recorded*, so a shared table has the next case meeting checksum drift for a reason
// that has nothing to do with it.

// sqliteHistoryTable keeps these runs out of the table the driver's own tests use.
const sqliteHistoryTable = "migr_e2e_sqlite"

// sqliteMigrations cover a table with an index and SQLite's native DROP COLUMN, which is the most
// expensive operation this engine has -- it rewrites the whole table content (ADR 0052).
//
// They are .sql, so every one of them reaches the database as ir.Raw. That is the limit of what this
// project can prove and the reason the Go project below exists: a rebuild needs a real operation, and
// no .sql migration ever produces one.
var sqliteMigrations = []struct{ name, body string }{
	{
		name: "20260929T120000_add_e2e_users.sql",
		body: "-- migr:up\nCREATE TABLE e2e_users (id integer PRIMARY KEY, email text NOT NULL, " +
			"note text);\nCREATE INDEX idx_e2e_users_email ON e2e_users (email);\n\n" +
			"-- migr:down\nDROP TABLE e2e_users;\n",
	},
	{
		name: "20260929T120100_drop_e2e_note.sql",
		body: "-- migr:up\nALTER TABLE e2e_users DROP COLUMN note;\n\n" +
			"-- migr:down\nALTER TABLE e2e_users ADD COLUMN note text;\n",
	},
}

// sqliteProject writes a migr.yaml over its own database file, and returns the config's path.
func sqliteProject(t *testing.T) (config, database string) {
	t.Helper()

	dir := t.TempDir()
	config = filepath.Join(dir, "migr.yaml")
	database = filepath.Join(dir, "app.db")

	body := fmt.Sprintf(`version: 1
migrations:
  dir: ./migrations
  language: sql
databases:
  default:
    driver: sqlite
    dsn: %s
history:
  table: %s
lock:
  timeout: 5s
`, database, sqliteHistoryTable)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "migrations"), 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, m := range sqliteMigrations {
		path := filepath.Join(dir, "migrations", m.name)
		if err := os.WriteFile(path, []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}

	return config, database
}

// sqliteOpened dials the database file, closing the session when the test is done.
func sqliteOpened(t *testing.T, database, table string) driver.Conn {
	t.Helper()

	d, err := sqlite.New(driver.Options{HistoryTable: table})
	if err != nil {
		t.Fatalf("building the driver: %v", err)
	}

	ctx := context.WithoutCancel(t.Context())

	conn, err := d.Open(ctx, database)
	if err != nil {
		t.Fatalf("opening %s: %v", database, err)
	}

	t.Cleanup(func() {
		if err := conn.Close(ctx); err != nil {
			t.Errorf("closing the session: %v", err)
		}
	})

	return conn
}

// sqliteDigest renders what the database holds, excluding this suite's history table.
func sqliteDigest(t *testing.T, database string) string {
	t.Helper()

	return sqliteDigestOf(t, database, sqliteHistoryTable)
}

// sqliteDigestOf renders what a database holds, excluding one history table and its index.
func sqliteDigestOf(t *testing.T, database, table string) string {
	t.Helper()

	conn := sqliteOpened(t, database, table)

	rendered, err := sqlitetest.Digest(sqlitetest.Over(context.WithoutCancel(t.Context()), conn,
		table))
	if err != nil {
		t.Fatalf("describing the database: %v", err)
	}

	return rendered
}

// sqliteRecorded returns every run the history holds, through the store contract alone.
func sqliteRecorded(t *testing.T, database string) []history.Record {
	t.Helper()

	conn := sqliteOpened(t, database, sqliteHistoryTable)

	log, err := conn.History().List(context.WithoutCancel(t.Context()))
	if err != nil {
		t.Fatalf("reading the history: %v", err)
	}

	return log
}

// sqliteApplied counts the completed runs in one direction, which is what a round trip is measured in.
func sqliteApplied(runs []history.Record, direction string) int {
	var count int

	for _, rec := range runs {
		if string(rec.Direction) == direction && rec.Outcome == history.Applied {
			count++
		}
	}

	return count
}

func TestTheWholeStackAppliesAndRollsBackAgainstSQLite(t *testing.T) {
	config, database := sqliteProject(t)

	before := sqliteDigest(t, database)

	if err := migr(t, config, "up", "--dry-run"); err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}

	if got := sqliteDigest(t, database); got != before {
		t.Fatalf("a dry run changed the schema:\n%s\n---\n%s", before, got)
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	applied := sqliteDigest(t, database)
	if applied == before {
		t.Fatal("up left the schema as it found it")
	}

	if got := sqliteApplied(sqliteRecorded(t, database), "up"); got != len(sqliteMigrations) {
		t.Fatalf("the history holds %d applied runs, want %d", got, len(sqliteMigrations))
	}

	if err := migr(t, config, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	if got := sqliteDigest(t, database); got != before {
		t.Errorf("the rollback did not restore the starting schema:\nbefore\n%s\nafter\n%s",
			before, got)
	}

	if got := sqliteApplied(sqliteRecorded(t, database), "down"); got != len(sqliteMigrations) {
		t.Errorf("the history holds %d rollbacks, want %d", got, len(sqliteMigrations))
	}

	t.Log("Byte-identical, and DROP COLUMN is what makes that worth asserting: SQLite rewrites the")
	t.Log("whole table content for one, and rewrites the recorded CREATE TABLE with it. Measured:")
	t.Log("dropping a column and adding it back leaves the definition exactly as it was.")
}

func TestTheRoundTripRunsTwiceOverAndTheSecondUpIsNotANoOpBySkipping(t *testing.T) {
	config, database := sqliteProject(t)

	for pass := range 2 {
		if err := migr(t, config, "up"); err != nil {
			t.Fatalf("up, pass %d: %v", pass, err)
		}

		if err := migr(t, config, "down"); err != nil {
			t.Fatalf("down, pass %d: %v", pass, err)
		}
	}

	log := sqliteRecorded(t, database)

	if got := sqliteApplied(log, "up"); got != 2*len(sqliteMigrations) {
		t.Errorf("the history holds %d applied runs over two passes, want %d", got,
			2*len(sqliteMigrations))
	}

	t.Log("A rollback that reported success without undoing anything would fail on the second up,")
	t.Log("and nowhere else: the history says the migrations are gone either way.")
}

func TestASecondUpWithNothingPendingChangesNothing(t *testing.T) {
	config, database := sqliteProject(t)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	applied := sqliteDigest(t, database)
	runs := len(sqliteRecorded(t, database))

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("the second up: %v", err)
	}

	if got := sqliteDigest(t, database); got != applied {
		t.Errorf("a second up changed the schema")
	}

	if got := len(sqliteRecorded(t, database)); got != runs {
		t.Errorf("a second up recorded %d runs, want none", got-runs)
	}
}

func TestStatusReportsWhatIsAppliedAndWhatIsStillPending(t *testing.T) {
	config, _ := sqliteProject(t)

	if err := migr(t, config, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	partial := migrOut(t, config, "status")

	if got := stateOf(t, partial, sqliteMigrations[0].name); got != "applied" {
		t.Errorf("%s reads as %q after one step, want applied", sqliteMigrations[0].name, got)
	}

	if got := stateOf(t, partial, sqliteMigrations[1].name); got != "pending" {
		t.Errorf("%s reads as %q after one step, want pending", sqliteMigrations[1].name, got)
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	for _, m := range sqliteMigrations {
		if got := stateOf(t, migrOut(t, config, "status"), m.name); got != "applied" {
			t.Errorf("%s reads as %q after up, want applied", m.name, got)
		}
	}
}

func TestHistoryReportsBothDirectionsOfTheRoundTrip(t *testing.T) {
	config, _ := sqliteProject(t)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if err := migr(t, config, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	runs := countRuns(migrOut(t, config, "history"))
	for _, m := range sqliteMigrations {
		name := strings.TrimSuffix(m.name, ".sql")

		for _, direction := range directions {
			if got := runs[name+" "+direction]; got != 1 {
				t.Errorf("history reports %d %s runs of %s, want 1", got, direction, name)
			}
		}
	}

	t.Log("One up and one down each, because both migrations applied in one batch and down unwinds")
	t.Log("a batch. Two batches would leave the earlier one applied, which is what --step makes.")
}

func TestADatabaseWithNoHistoryTableIsPrintedAsItselfRatherThanAsEveryMigrationPending(t *testing.T) {
	config, _ := sqliteProject(t)

	out := migrOut(t, config, "status")

	if !strings.Contains(out, "no migration history table yet") {
		t.Errorf("status did not say the history table is absent:\n%s", out)
	}

	t.Log("ADR 0019: read as \"no runs yet\", a fully migrated production database looks virgin.")
	t.Log("status creates nothing, so it is the command that meets history.ErrNoTable for real.")
}

func TestStatusCreatesNothingAtAll(t *testing.T) {
	config, database := sqliteProject(t)

	before := sqliteDigest(t, database)

	if _, err := os.Stat(database); err != nil {
		t.Fatalf("the database file: %v", err)
	}

	migrOut(t, config, "status")

	if got := sqliteDigest(t, database); got != before {
		t.Errorf("status changed the schema:\n%s\n---\n%s", before, got)
	}

	conn := sqliteOpened(t, database, sqliteHistoryTable)
	if _, err := conn.History().List(context.WithoutCancel(t.Context())); err == nil {
		t.Error("status created the history table")
	}
}

func TestAMutatedMigrationBodyIsCaughtAsDriftRatherThanReapplied(t *testing.T) {
	config, database := sqliteProject(t)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	applied := sqliteDigest(t, database)

	mutate(t, config, sqliteMigrations[0].name,
		"CREATE INDEX idx_e2e_users_email ON e2e_users (email);",
		"CREATE INDEX idx_e2e_users_email ON e2e_users (email, id);")

	out := migrFailure(t, config, "up")

	if !strings.Contains(out, "checksum") && !strings.Contains(out, "drift") {
		t.Errorf("the failure does not name the drift:\n%s", out)
	}

	if got := sqliteDigest(t, database); got != applied {
		t.Errorf("a drifted run changed the schema:\n%s\n---\n%s", applied, got)
	}
}

// mutate rewrites one migration in place, which is what an applied migration must not survive.
func mutate(t *testing.T, config, name, from, to string) {
	t.Helper()

	path := filepath.Join(filepath.Dir(config), "migrations", name)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}

	if !strings.Contains(string(body), from) {
		t.Fatalf("%s does not contain %q", name, from)
	}

	mutated := strings.Replace(string(body), from, to, 1)
	if err := os.WriteFile(path, []byte(mutated), 0o600); err != nil {
		t.Fatalf("rewriting %s: %v", name, err)
	}
}

func TestValidatePassesWhatTheWholeStackThenRunsAgainstSQLite(t *testing.T) {
	config, _ := sqliteProject(t)

	out := migrOut(t, config, "validate")

	if !strings.Contains(out, "no problems found") {
		t.Errorf("validate found a problem in migrations the stack then applies:\n%s", out)
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up after a clean validate: %v", err)
	}
}
