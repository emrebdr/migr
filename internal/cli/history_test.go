package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/version"
)

// decodeLog reads the document --json promised, failing the test rather than the command.
func decodeLog(t *testing.T, stdout string) executionLog {
	t.Helper()

	var log executionLog
	if err := json.Unmarshal([]byte(stdout), &log); err != nil {
		t.Fatalf("migr history --json emitted %v:\n%s", err, stdout)
	}

	return log
}

// runsOf returns each printed run as its batch, ordinal, and direction, which is the log's own order.
func runsOf(log executionLog) []string {
	printed := make([]string, 0, len(log.Runs))

	for _, run := range log.Runs {
		printed = append(printed, run.Version+" "+string(run.Direction))
	}

	return printed
}

func TestHistoryPrintsEveryRunInTheOrderTheyHappened(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	for _, args := range [][]string{{"up"}, {"down"}, {"up"}} {
		if _, _, err := runWith(t, dir, noon, drivers, args...); err != nil {
			t.Fatalf("migr %s: %v", args[0], err)
		}
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "history")
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	want := []string{
		versionAt(0) + " up", versionAt(1) + " up",
		versionAt(1) + " down", versionAt(0) + " down",
		versionAt(0) + " up", versionAt(1) + " up",
	}

	if got := runsOf(decodeLog(t, stdout)); !slices.Equal(got, want) {
		t.Errorf("history printed %q, want %q", got, want)
		t.Log("The table is a log, not a state: every run appends a row, and a rollback does not")
		t.Log("erase the evidence that it happened. The order is (batch, ordinal), which is the")
		t.Log("order the runs happened and the one a rollback reverses.")
	}
}

func TestHistoryTakesNoLockAndDiscoversNothing(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	f.calls = nil

	if _, _, err := runWith(t, dir, noon, drivers, "history"); err != nil {
		t.Fatalf("history: %v", err)
	}

	if !slices.Equal(f.calls, []string{"open", "list", "close"}) {
		t.Errorf("history asked the session for %q, want it to open, list, and give it back", f.calls)
	}
}

func TestHistoryNeedsNoMigrationsDirectoryAtAll(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if err := os.RemoveAll(filepath.Join(dir, scaffoldDir)); err != nil {
		t.Fatalf("removing the migrations directory: %v", err)
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "history")
	if err != nil {
		t.Log("The log is the database's, and an operator running history to diagnose a broken")
		t.Log("migrations directory is the case where refusing to print it is worst of all.")
		t.Fatalf("history without a migrations directory: %v", err)
	}

	if !strings.Contains(stdout, versionAt(0)) {
		t.Errorf("history printed no run for a migration whose file is gone:\n%s", stdout)
	}

	if _, _, err := runWith(t, dir, noon, drivers, "status"); err == nil {
		t.Errorf("status succeeded with no migrations directory, though it reconciles against one")
	}
}

func TestHistoryNamesAMigrationTheOnlyWayARowCan(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "history")
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	if strings.Contains(stdout, ".sql") {
		t.Errorf("history rendered a filename:\n%s", stdout)
		t.Log("A history row carries a version and a name and no extension, deliberately: a run")
		t.Log("may outlive the file it ran from. migr status renders filenames; this cannot.")
	}

	if !strings.Contains(stdout, versionAt(0)+"_add_users") {
		t.Errorf("history did not name the migration by what its row carries:\n%s", stdout)
	}
}

func TestHistoryOverADatabaseWithNoHistoryTableSaysSo(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "history")
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	if !strings.Contains(stdout, "no migration history table yet") {
		t.Errorf("history did not say the table is absent:\n%s", stdout)
	}

	if log := decodeLog(t, mustJSON(t, dir, drivers)); log.Recorded || len(log.Runs) != 0 {
		t.Errorf("history reported %+v over a database with no history table", log)
	}
}

func TestHistoryWritesOutWhyARunDidNotComplete(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	f.record(t, history.Record{
		Batch: 1, Ordinal: 1, Version: versionAt(0), Name: "add_users",
		Direction: ir.Up, Outcome: history.Failed, Duration: tick,
		Checksum: checksum.Checksum{Source: checksum.OfSource([]byte("users"))},
		Error:    "the server was not listening",
	})

	f.record(t, history.Record{
		Batch: 2, Ordinal: 1, Version: versionAt(1), Name: "add_teams",
		Direction: ir.Down, Outcome: history.Dirty, Duration: tick,
		Checksum:      checksum.Checksum{Source: checksum.OfSource([]byte("teams"))},
		LastStatement: 3, Error: `statement 4: DROP TABLE teams;: relation "teams" does not exist`,
	})

	stdout, _, err := runWith(t, dir, noon, drivers, "history")
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	for _, want := range []string{
		"failed: the server was not listening",
		`stopped after statement 3 -- statement 4: DROP TABLE teams;: ` +
			`relation "teams" does not exist`,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("history does not write out %q:\n%s", want, stdout)
			t.Log("A run that did not complete is what an operator opened this command for, and a")
			t.Log("server error is a sentence rather than a column.")
		}
	}
}

func TestHistoryPrintsWhatEachRunCost(t *testing.T) {
	dir, _, drivers := project(t, twoTables()[:1]...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "history")
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	if !strings.Contains(stdout, tick.String()) {
		t.Errorf("history does not print what the run cost, which the clock fixed at %s:\n%s",
			tick, stdout)
		t.Log("The took column reports what the row holds, and a row holds whole milliseconds --")
		t.Log("so a run faster than one reads back as 0s, and one that took a quarter second")
		t.Log("must not.")
	}

	if run := decodeLog(t, mustJSON(t, dir, drivers)).Runs[0]; run.Duration == 0 {
		t.Errorf("--json reports a run that took no time at all")
	}
}

func TestHistoryJSONCarriesTheWholeRow(t *testing.T) {
	dir, _, drivers := project(t, twoTables()[:1]...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	stdout, stderr, err := runWith(t, dir, noon, drivers, "--json", "history")
	if err != nil {
		t.Fatalf("history --json: %v", err)
	}

	if stderr != "" {
		t.Errorf("history --json wrote %q to stderr", stderr)
	}

	log := decodeLog(t, stdout)

	if len(log.Runs) != 1 {
		t.Fatalf("history reported %d runs, want the one that ran", len(log.Runs))
	}

	run := log.Runs[0]

	for _, field := range []struct{ name, got string }{
		{"driver", run.Driver},
		{"runtime", run.Runtime},
		{"engine_version", run.EngineVersion},
		{"checksum", run.Checksum},
	} {
		if field.got == "" {
			t.Errorf("--json reports no %s, though the row records one", field.name)
		}
	}

	if run.IRVersion != ir.Version || run.EngineVersion != version.Version {
		t.Errorf("--json reports the row was written by %s/ir%d, want this build's own provenance",
			run.EngineVersion, run.IRVersion)
		t.Log("This is the command an operator runs to inspect exactly that table, so --json")
		t.Log("carries the whole row where the table carries the columns a person reads.")
	}
}

// mustJSON runs history --json in a staged project, failing the test rather than the command.
func mustJSON(t *testing.T, dir string, drivers *driver.Registry) string {
	t.Helper()

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "history")
	if err != nil {
		t.Fatalf("history --json: %v", err)
	}

	return stdout
}
