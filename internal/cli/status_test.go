package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// lineFor returns the status line for one migration, so a test asserts a row rather than a substring.
func lineFor(t *testing.T, stdout, name string) string {
	t.Helper()

	for line := range strings.Lines(stdout) {
		if strings.Contains(line, name) {
			return strings.TrimSpace(line)
		}
	}

	t.Fatalf("no status line mentions %q:\n%s", name, stdout)

	return ""
}

// decodeStatus reads the document --json promised, failing the test rather than the command.
func decodeStatus(t *testing.T, stdout string) statusReport {
	t.Helper()

	var report statusReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("migr status --json emitted %v:\n%s", err, stdout)
	}

	return report
}

// rowFor returns the decoded row for one version, so a test reads a field rather than a rendering.
func rowFor(t *testing.T, report statusReport, version string) statusRow {
	t.Helper()

	for _, row := range report.Statuses {
		if row.Version == version {
			return row
		}
	}

	t.Fatalf("no status row carries version %s: %+v", version, report.Statuses)

	return statusRow{}
}

func TestStatusTakesNoLockAndCreatesNothing(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "status"); err != nil {
		t.Fatalf("status: %v", err)
	}

	if !slices.Equal(f.calls, []string{"open", "list", "close"}) {
		t.Errorf("status asked the session for %q, want it to open, list, and give it back", f.calls)
		t.Log("A status answers what is, not what will run. Taking the migration lock would make a")
		t.Log("read-only report block a deploy, and give it an exit code 3 of its own (ADR 0027, A).")
	}

	if f.table {
		t.Errorf("status created the history table")
	}
}

func TestStatusShowsEveryStateItCanResolve(t *testing.T) {
	dir, _, drivers := everyState(t)

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	for _, want := range []struct{ name, state string }{
		{fileAt(0, "add_users"), "applied"},
		{fileAt(1, "add_teams"), "drifted"},
		{versionAt(2), "missing"},
		{fileAt(3, "add_orgs"), "dirty"},
		{fileAt(4, "add_repos"), "pending"},
	} {
		if line := lineFor(t, stdout, want.name); !strings.HasPrefix(line, want.state) {
			t.Errorf("%s reads %q, want the state %q", want.name, line, want.state)
		}
	}

	if !strings.Contains(stdout, "1 applied, 1 pending, 1 missing, 1 drifted, 1 dirty") {
		t.Errorf("the count line does not name all five states:\n%s", stdout)
		t.Log("The counts walk history.States(), so a sixth state is counted the day it exists")
		t.Log("rather than the day someone remembers to add it here (M13-T14).")
	}
}

func TestStatusPrintsTheOrderMigrUpWouldRunThem(t *testing.T) {
	dir, _, drivers := everyState(t)

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "status")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}

	report := decodeStatus(t, stdout)

	got := make([]string, 0, len(report.Statuses))
	for _, row := range report.Statuses {
		got = append(got, row.Version)
	}

	want := []string{versionAt(0), versionAt(1), versionAt(3), versionAt(4), versionAt(2)}

	if !slices.Equal(got, want) {
		t.Errorf("status reported %q, want the file order with the recorded-only version appended", got)
		t.Log("The order is migration.Order's, which is the order migr up would run them; re-sorting")
		t.Log("here would show an order migr will not follow and discard the dependency overlay.")
		t.Log("A version no file describes has no position to merge into, so it goes last.")
	}
}

func TestStatusReportsDriftAndDirtWithoutFailing(t *testing.T) {
	dir, _, drivers := everyState(t)

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Log("The five states are this command's output, not its failure. migr up already exits 4")
		t.Log("on drift and 1 on dirty; a report that printed correctly did not fail (ADR 0027, E).")
		t.Fatalf("status exited %v having reported successfully", err)
	}

	if drifted := lineFor(t, stdout, fileAt(1, "add_teams")); !strings.Contains(
		drifted, "edited since it was applied") {
		t.Errorf("the drifted row does not say what drifted means: %q", drifted)
	}

	if dirty := lineFor(t, stdout, fileAt(3, "add_orgs")); !strings.Contains(
		dirty, "stopped after statement 2 of the up run") {
		t.Errorf("the dirty row does not say how far the run got: %q", dirty)
		t.Log("last_statement is where M9-T6 resumes from, and this is the first command to show it.")
	}
}

func TestStatusNamesAMissingVersionRatherThanAFilenameItHasNone(t *testing.T) {
	dir, _, drivers := everyState(t)

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	line := lineFor(t, stdout, versionAt(2))

	if strings.Contains(line, "_") {
		t.Errorf("the missing row reads %q, want the version alone", line)
		t.Log("A status no file describes carries a zero Migration, whose Filename() is the string")
		t.Log(`"_" -- so the renderer has to ask before it calls it.`)
	}

	if !strings.Contains(line, "no migration file carries this version") {
		t.Errorf("the missing row does not say what missing means: %q", line)
	}
}

func TestStatusPrintsTheBatchMigrDownWouldActOn(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if line := lineFor(t, stdout, fileAt(1, "add_teams")); !strings.Contains(line, "batch 2") {
		t.Errorf("the second migration reads %q, want the batch its own run recorded", line)
		t.Log("The batch printed is Status.Recorded.Batch, the field LastBatch() reads, so the")
		t.Log("number an operator sees is the number migr down will act on (M4-T8, ADR 0023).")
	}

	if len(f.runs) != 2 {
		t.Errorf("the fixture recorded %d runs, want one per migration", len(f.runs))
	}
}

func TestStatusSaysWhichDirectionLeftAMigrationWhereItIs(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	for _, args := range [][]string{{"up"}, {"down"}} {
		if _, _, err := runWith(t, dir, noon, drivers, args...); err != nil {
			t.Fatalf("migr %s: %v", args[0], err)
		}
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	line := lineFor(t, stdout, fileAt(0, "add_users"))

	if !strings.HasPrefix(line, "pending") || !strings.Contains(line, "down in batch 2") {
		t.Errorf("a rolled-back migration reads %q, want pending and the rollback that left it so", line)
		t.Log("The anchor of a pending migration is not always absent: a rollback that completed")
		t.Log("leaves the fold uncarried and anchored on the down run. Printing its batch with no")
		t.Log("direction reads as though the migration had been applied in that batch.")
	}
}

func TestStatusOverADatabaseWithNoHistoryTablePrintsThatRatherThanAnEmptyTable(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if !strings.Contains(stdout, "no migration history table yet") {
		t.Errorf("status did not say the history table is absent:\n%s", stdout)
	}

	for _, state := range history.States() {
		if strings.Contains(stdout, state.String()) {
			t.Errorf("status resolved a state over a database with no history table:\n%s", stdout)
			t.Log("An absent table yields zero statuses, not N pendings. Read as pending, a fully")
			t.Log("migrated production database looks virgin -- the failure ADR 0019 exists for.")
		}
	}

	if !strings.Contains(stdout, "there are 2 migration files on disk") {
		t.Errorf("status did not count the files it found:\n%s", stdout)
	}
}

func TestStatusCountsOneMigrationFileInTheSingular(t *testing.T) {
	dir, _, drivers := project(t, twoTables()[:1]...)

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if !strings.Contains(stdout, "there is 1 migration file on disk") {
		t.Errorf("status counted one file as %q:\n%s", describeFiles(1), stdout)
		t.Log("The count is written into a sentence, so a helper that agrees with itself in the")
		t.Log("plural and not in the singular reads as a typo in the tool rather than in the text.")
	}
}

func TestStatusJSONCarriesTheRunThatDecidedEachState(t *testing.T) {
	dir, _, drivers := everyState(t)

	stdout, stderr, err := runWith(t, dir, noon, drivers, "--json", "status")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}

	if stderr != "" {
		t.Errorf("status --json wrote %q to stderr", stderr)
	}

	report := decodeStatus(t, stdout)

	if pending := rowFor(t, report, versionAt(4)); pending.Run != nil {
		t.Errorf("the pending row carries a run: %+v", pending.Run)
		t.Log("A pending migration has no anchor run, so there is nothing to render. A zero")
		t.Log("checksum also refuses to marshal, so a row that carried one would fail the whole")
		t.Log("document the moment one migration was pending.")
	}

	if applied := rowFor(t, report, versionAt(0)); applied.Run == nil || applied.Run.Batch != 1 {
		t.Errorf("the applied row carries %+v, want the run that applied it", applied.Run)
	}

	if dirty := rowFor(t, report, versionAt(3)); dirty.Run == nil || dirty.Run.LastStatement != 2 {
		t.Errorf("the dirty row carries %+v, want the statement it reached", dirty.Run)
	}
}

func TestStatusJSONReportsBothDigestsOfADriftedMigrationAndNoOthers(t *testing.T) {
	dir, _, drivers := everyState(t)

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "status")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}

	report := decodeStatus(t, stdout)
	drifted := rowFor(t, report, versionAt(1))

	if drifted.RecordedSource == "" || drifted.CurrentSource == "" {
		t.Fatalf("the drifted row carries %+v, want both source digests", drifted)
	}

	if drifted.RecordedSource == drifted.CurrentSource {
		t.Errorf("the drifted row reports one digest twice: %s", drifted.CurrentSource)
		t.Log("Drift is decided on the source half alone, and the two values are the message's")
		t.Log("whole job. Nothing compares them again -- the row reads State (ADR 0009, M4-T7).")
	}

	if applied := rowFor(t, report, versionAt(0)); applied.RecordedSource != "" {
		t.Errorf("the applied row carries a drift digest: %s", applied.RecordedSource)
	}
}

func TestStatusReportsAProjectWhoseLanguageNothingCanRunYet(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	swapLanguage(t, dir, "go")

	if _, _, err := runWith(t, dir, noon, drivers, "status"); err != nil {
		t.Log("A status compiles nothing and reads no migration body, so the source that cannot")
		t.Log("run Go migrations until M6-T10 is one it never needed to build.")
		t.Fatalf("status over a Go project: %v", err)
	}

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err == nil {
		t.Errorf("up over a Go project succeeded, though nothing can run those migrations yet")
	}
}

func TestStatusCountsEveryStateThisBuildKnows(t *testing.T) {
	counts := countStates(nil)

	if len(counts) != len(history.States()) {
		t.Errorf("the counts hold %d states, want one entry per state a migration may resolve to",
			len(counts))
	}

	for _, state := range history.States() {
		if _, ok := counts[state.String()]; !ok {
			t.Errorf("--json reports no count for %s, so a script cannot ask whether it is zero", state)
		}
	}
}

// everyState stages a project holding one migration in each of the five states reconciliation resolves.
func everyState(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, fiveMigrations()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up", "--step", "3"); err != nil {
		t.Fatalf("staging three applied migrations: %v", err)
	}

	rewrite(t, dir, fileAt(1, "add_teams"), "CREATE TABLE teams (id bigint, name text);")
	remove(t, dir, fileAt(2, "add_repos_old"))

	f.record(t, history.Record{
		Batch:         2,
		Ordinal:       1,
		Version:       versionAt(3),
		Name:          "add_orgs",
		Direction:     ir.Up,
		Outcome:       history.Dirty,
		Checksum:      checksum.Checksum{Source: checksum.OfSource([]byte("orgs")), IR: checksum.OfSource(nil)},
		Duration:      tick,
		LastStatement: 2,
		Error:         "the server refused this statement",
	})

	return dir, f, drivers
}

// fiveMigrations is the project every-state runs against: three that will run, one dirty, one new.
func fiveMigrations() []stagedMigration {
	return []stagedMigration{
		{name: "add_users", up: "CREATE TABLE users (id bigint);", down: "DROP TABLE users;"},
		{name: "add_teams", up: "CREATE TABLE teams (id bigint);", down: "DROP TABLE teams;"},
		{name: "add_repos_old", up: "CREATE TABLE repos_old (id bigint);", down: "DROP TABLE repos_old;"},
		{name: "add_orgs", up: "CREATE TABLE orgs (id bigint);", down: "DROP TABLE orgs;"},
		{name: "add_repos", up: "CREATE TABLE repos (id bigint);", down: "DROP TABLE repos;"},
	}
}

// rewrite replaces a migration's body after it has run, which is what drift is.
func rewrite(t *testing.T, dir, name, up string) {
	t.Helper()

	write(t, filepath.Join(dir, scaffoldDir, name),
		stagedMigration{up: up, down: "DROP TABLE teams;"}.body())
}

// remove deletes a migration whose runs the database still carries, which is what missing is.
func remove(t *testing.T, dir, name string) {
	t.Helper()

	if err := os.Remove(filepath.Join(dir, scaffoldDir, name)); err != nil {
		t.Fatalf("removing %s: %v", name, err)
	}
}

// swapLanguage repoints a staged project at a language whose runtime does not exist yet.
func swapLanguage(t *testing.T, dir, lang string) {
	t.Helper()

	path := filepath.Join(dir, config.DefaultFilename)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the staged config: %v", err)
	}

	write(t, path, strings.Replace(string(body), "language: sql", "language: "+lang, 1))
}
