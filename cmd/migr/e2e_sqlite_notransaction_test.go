package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/schema"
)

// vacuumName is a migration that runs VACUUM between two statements, which SQLite refuses in a transaction.
const vacuumName = "20260929T120300_vacuum_e2e.sql"

// vacuumMigration declares its VACUUM, so the migration runs in three parts.
const vacuumMigration = "-- migr:up\nCREATE TABLE e2e_vacuumed (id integer PRIMARY KEY, tag text);\n" +
	"-- migr:no-transaction\nVACUUM;\nCREATE INDEX idx_e2e_vacuumed_tag ON e2e_vacuumed (tag);\n\n" +
	"-- migr:down\nDROP TABLE e2e_vacuumed;\n"

// vacuumProject stages the round-trip project with one more migration whose body a test chooses.
func vacuumProject(t *testing.T, body string) (config, database string) {
	t.Helper()

	config, database = sqliteProject(t)

	path := filepath.Join(filepath.Dir(config), "migrations", vacuumName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("staging %s: %v", vacuumName, err)
	}

	return config, database
}

func TestAMigrationSplitAroundAVacuumAppliesAndRollsBackAgainstSQLite(t *testing.T) {
	config, database := vacuumProject(t, vacuumMigration)
	before := sqliteDigest(t, database)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up returned %v, want the VACUUM run outside any transaction", err)
	}

	var rows []history.Outcome

	for _, rec := range sqliteRecorded(t, database) {
		if strings.HasPrefix(vacuumName, rec.Version) {
			rows = append(rows, rec.Outcome)
		}
	}

	want := []history.Outcome{history.Dirty, history.Dirty, history.Applied}
	if len(rows) != len(want) || rows[0] != want[0] || rows[1] != want[1] || rows[2] != want[2] {
		t.Errorf("the split migration's rows are %v, want %v", rows, want)
	}

	if err := migr(t, config, "down", "--step", "3"); err != nil {
		t.Fatalf("down: %v", err)
	}

	if got := sqliteDigest(t, database); got != before {
		t.Errorf("the rollback did not restore the starting schema:\n%s", schema.Difference(before, got))
	}

	t.Log("SQLite refuses VACUUM inside a transaction, so this is the untagged proof of the split:")
	t.Log("the table commits first, VACUUM runs bare, and the index commits with the applied row.")
}

func TestWithoutTheMarkerVacuumIsRefusedAndNothingIsLeftBehind(t *testing.T) {
	config, database := vacuumProject(t, strings.Replace(vacuumMigration, "-- migr:no-transaction\n", "", 1))

	if err := migr(t, config, "up"); err == nil {
		t.Fatal("up succeeded with a VACUUM inside the migration's transaction")
	}

	if strings.Contains(sqliteDigest(t, database), "e2e_vacuumed") {
		t.Error("the refused migration left its table behind, so it did not run as one transaction")
	}

	t.Log("The control: undeclared, the whole migration is one transaction, SQLite refuses the")
	t.Log("VACUUM, and the rollback takes the table with it -- the marker is what made the other pass.")
}
