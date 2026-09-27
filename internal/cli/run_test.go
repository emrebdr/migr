package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
)

// applied returns the versions a fake database holds a completed run for, in the order they ran.
func applied(f *fake) []string {
	var versions []string

	for _, rec := range f.runs {
		if rec.Outcome == history.Applied && rec.Direction == "up" {
			versions = append(versions, rec.Version)
		}
	}

	return versions
}

// reversed returns the versions a fake database holds a completed rollback for, in the order they ran.
func reversed(f *fake) []string {
	var versions []string

	for _, rec := range f.runs {
		if rec.Outcome == history.Applied && rec.Direction == "down" {
			versions = append(versions, rec.Version)
		}
	}

	return versions
}

func TestUpAppliesEveryPendingMigrationAndSaysWhatItDid(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "up")
	if err != nil {
		t.Fatalf("up: %v", err)
	}

	want := []string{
		"CREATE TABLE users (id bigint);",
		"CREATE INDEX idx_users ON users (id);",
		"CREATE TABLE teams (id bigint);",
	}

	if !slices.Equal(f.statements, want) {
		t.Errorf("the database ran %q, want %q", f.statements, want)
		t.Log("A .sql migration becomes one raw operation per statement, so the two statements in")
		t.Log("the first file are two the engine counts, times, and could resume between.")
	}

	if got := applied(f); !slices.Equal(got, []string{versionAt(0), versionAt(1)}) {
		t.Errorf("the history holds %q, want both migrations in version order", got)
	}

	for _, want := range []string{
		fileAt(0, "add_users"), "2 statements",
		fileAt(1, "add_teams"), "1 statement",
		"batch 1", "applied 2 migrations",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the summary does not mention %q:\n%s", want, stdout)
		}
	}
}

func TestEachRunIsRecordedInsideTheTransactionThatRanIt(t *testing.T) {
	dir, f, drivers := project(t, twoTables()[:1]...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	want := []string{"open", "lock", "ensure", "list", "latest", "begin", "record", "commit",
		"unlock", "close"}

	if !slices.Equal(f.calls, want) {
		t.Errorf("the session was asked for %q, want %q", f.calls, want)
		t.Log("The lock is taken before the transaction begins, the row is written inside the")
		t.Log("transaction that ran the statements, and the release runs after the rollback would")
		t.Log("have — every one of those orderings is a constraint rather than a preference.")
	}
}

func TestDownWithNoFlagsRollsBackTheLastBatch(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	f.statements = nil

	stdout, _, err := runWith(t, dir, noon, drivers, "down")
	if err != nil {
		t.Fatalf("down: %v", err)
	}

	if !slices.Equal(f.statements, []string{"DROP TABLE teams;", "DROP TABLE users;"}) {
		t.Errorf("the rollback ran %q, want the whole batch newest first", f.statements)
	}

	if got := reversed(f); !slices.Equal(got, []string{versionAt(1), versionAt(0)}) {
		t.Errorf("the history holds %q, want both undone newest first", got)
	}

	if !strings.Contains(stdout, "rolled back 2 migrations") {
		t.Errorf("the summary does not report the whole batch:\n%s", stdout)
		t.Log("A bare `migr down` rolls back the last batch, not the last single migration. Latest")
		t.Log("is direction-blind, so the batch a rollback acts on cannot come from it (ADR 0023).")
	}
}

func TestStepNarrowsInBothDirections(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	if got := applied(f); !slices.Equal(got, []string{versionAt(0)}) {
		t.Fatalf("up --step 1 applied %q, want only the first pending migration", got)
	}

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if _, _, err := runWith(t, dir, noon, drivers, "down", "--step", "1"); err != nil {
		t.Fatalf("down --step 1: %v", err)
	}

	if got := reversed(f); !slices.Equal(got, []string{versionAt(1)}) {
		t.Errorf("down --step 1 undid %q, want only the last applied migration", got)
	}

	t.Log("--step 2 means the first two pending going up and the last two applied going down, so")
	t.Log("the two directions are not one function with a flag (ADR 0023).")
}

func TestToLeavesTheDatabaseAtThatVersionInBothDirections(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up", "--to", versionAt(0)); err != nil {
		t.Fatalf("up --to: %v", err)
	}

	if got := applied(f); !slices.Equal(got, []string{versionAt(0)}) {
		t.Fatalf("up --to the first version applied %q, want through it inclusive", got)
	}

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if _, _, err := runWith(t, dir, noon, drivers, "down", "--to", versionAt(0)); err != nil {
		t.Fatalf("down --to: %v", err)
	}

	if got := reversed(f); !slices.Equal(got, []string{versionAt(1)}) {
		t.Errorf("down --to the first version undid %q, want everything newer than it", got)
	}

	t.Log("--to names the state the database should end in, so both up --to X and down --to X")
	t.Log("leave it at X: inclusive going up, exclusive going down (ADR 0023).")
}

func TestStepAndToTogetherAreRefused(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	_, _, err := runWith(t, dir, noon, drivers, "up", "--step", "1", "--to", versionAt(0))
	if err == nil {
		t.Fatal("up accepted two selectors that name different sets")
	}

	if got := ExitCode(err); got != ExitValidation {
		t.Errorf("ExitCode = %d, want %d", got, ExitValidation)
	}
}

func TestADryRunExecutesNothingAndCreatesNoHistoryTable(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "up", "--dry-run")
	if err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}

	if len(f.statements) != 0 {
		t.Errorf("a dry run executed %q", f.statements)
	}

	if len(f.runs) != 0 {
		t.Errorf("a dry run recorded %d history rows", len(f.runs))
	}

	if f.asked("ensure") {
		t.Errorf("a dry run created the migration history table: %q", f.calls)
		t.Log("Ensure is a write, and a command that says it will touch nothing must not make it.")
		t.Log("That is why a preview reads the history where an apply ensures it (ADR 0026).")
	}

	if !f.asked("lock") || !f.asked("unlock") {
		t.Errorf("a dry run did not take the migration lock: %q", f.calls)
		t.Log("Reconciling outside the lock answers a question another run is still changing.")
	}

	for _, want := range []string{"dry run", fileAt(0, "add_users"), "2 statements",
		"nothing was executed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the dry run does not report %q:\n%s", want, stdout)
		}
	}
}

func TestADryRunAgainstADatabaseWithNoHistoryTableSaysSo(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "up", "--dry-run")
	if err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}

	if !strings.Contains(stdout, "no migration history table") {
		t.Errorf("the dry run does not say the table is absent:\n%s", stdout)
	}

	if !strings.Contains(stdout, "would apply 2 migrations") {
		t.Errorf("the dry run does not list what a first apply would run:\n%s", stdout)
		t.Log("An apply reads an absent history table as a bug, because it just ensured one. A")
		t.Log("preview never ensured one, so absent is an answer: every migration is pending.")
	}
}

func TestADryRunAfterAnApplyReportsOnlyWhatIsLeft(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	before := len(f.calls)

	stdout, _, err := runWith(t, dir, noon, drivers, "up", "--dry-run")
	if err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}

	if !strings.Contains(stdout, "would apply 1 migration") {
		t.Errorf("the dry run does not report the one migration left:\n%s", stdout)
	}

	if !strings.Contains(stdout, "batch 2") {
		t.Errorf("the dry run does not name the batch it would record under:\n%s", stdout)
	}

	if slices.Contains(f.calls[before:], "ensure") {
		t.Errorf("the dry run ensured a table that already existed: %q", f.calls[before:])
	}
}

func TestTheSummaryShowsWhatCommittedBeforeTheFailure(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.refuse = "CREATE TABLE teams (id bigint);"

	stdout, _, err := runWith(t, dir, noon, drivers, "up")
	if err == nil {
		t.Fatal("up reported success for a refused statement")
	}

	if !strings.Contains(stdout, fileAt(0, "add_users")) {
		t.Errorf("the summary does not show the migration that committed:\n%s", stdout)
		t.Log("Result is populated alongside the error, which is the whole reason it is a value")
		t.Log("rather than a field on one — an operator has to know what already ran.")
	}

	if strings.Contains(stdout, fileAt(1, "add_teams")) {
		t.Errorf("the summary claims the migration that failed as applied:\n%s", stdout)
	}

	if !strings.Contains(err.Error(), fileAt(1, "add_teams")) {
		t.Errorf("the failure does not name the migration it happened in: %v", err)
	}

	if got := applied(f); !slices.Equal(got, []string{versionAt(0)}) {
		t.Errorf("the history holds %q, want only the migration that committed", got)
	}
}

func TestAMigrationThatAuthorsNoDownRefusesTheWholeRollback(t *testing.T) {
	dir, f, drivers := project(t,
		stagedMigration{name: "add_users", up: "CREATE TABLE users (id bigint);", down: "DROP TABLE users;"},
		stagedMigration{name: "add_teams", up: "CREATE TABLE teams (id bigint);"},
	)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	f.statements = nil

	_, _, err := runWith(t, dir, noon, drivers, "down")
	if err == nil {
		t.Fatal("down reported success for a migration nothing can reverse")
	}

	if len(f.statements) != 0 {
		t.Errorf("the rollback ran %q before refusing", f.statements)
		t.Log("An operation with no inverse halts the whole rollback before its first statement,")
		t.Log("because a rollback runs newest first and one that stopped partway would leave")
		t.Log("migrations whose successors are already undone (invariant #10).")
	}

	if !strings.Contains(err.Error(), fileAt(1, "add_teams")) {
		t.Errorf("the refusal does not name the migration that cannot be reversed: %v", err)
	}

	if got := ExitCode(err); got != ExitRuntime {
		t.Errorf("ExitCode = %d, want %d", got, ExitRuntime)
	}
}

func TestUpJSONIsValidAndOnStdout(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, stderr, err := runWith(t, dir, noon, drivers, "up", "--json")
	if err != nil {
		t.Fatalf("up --json: %v", err)
	}

	var result struct {
		Database  string `json:"database"`
		Direction string `json:"direction"`
		Batch     int64  `json:"batch"`
		Runs      []struct {
			Ordinal    int32   `json:"ordinal"`
			Version    string  `json:"version"`
			File       string  `json:"file"`
			Statements int32   `json:"statements"`
			DurationMS float64 `json:"duration_ms"`
		} `json:"runs"`
	}

	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\ngot: %q", err, stdout)
	}

	if result.Database != "default" || result.Direction != "up" || result.Batch != 1 {
		t.Errorf("up reported %+v, want the database, direction, and batch it ran under", result)
	}

	if len(result.Runs) != 2 || result.Runs[0].Statements != 2 || result.Runs[0].DurationMS <= 0 {
		t.Errorf("up reported runs %+v, want both, with their statement counts and timings",
			result.Runs)
	}

	if stderr != "" {
		t.Errorf("nothing should reach stderr at default verbosity, got %q", stderr)
	}
}

// staging is how each language needing a runtime gets a project, so the loop below covers them all.
var staging = map[config.Language]func(*testing.T, ...string) (string, *fake, *driver.Registry){
	config.LanguageGo:         goProject,
	config.LanguageTypeScript: tsProject,
}

func TestEveryLanguageMigrRegistersReachesItsRuntime(t *testing.T) {
	t.Log("Both SDK languages were refused by name while their adapters already existed -- go")
	t.Log("between M6-T8 and M6-T10, typescript between M7-T6 and M7-T8. Nothing is waiting now,")
	t.Log("so the gate that named a task is gone and this is what took its place.")

	for _, lang := range scripted {
		stage, staged := staging[lang]
		if !staged {
			t.Fatalf("%s needs a runtime and this test stages no project for it", lang)
		}

		dir, f, drivers := stage(t, "add_scripted")

		if _, _, err := runScripted(t, dir, actEmits, drivers, "up"); err != nil {
			t.Errorf("up on a %s project: %v", lang, err)

			continue
		}

		if !slices.Contains(f.statements, "CREATE TABLE scripted (id bigint)") {
			t.Errorf("a %s project ran %v, and the runtime emitted a create_table", lang, f.statements)
		}
	}
}

func TestADirectoryHoldingTwoLanguagesRunsBothInTimestampOrder(t *testing.T) {
	dir, f, drivers := goProject(t, "add_scripted")
	write(t, filepath.Join(dir, scaffoldDir, versionAt(1)+"_add_other.sql"),
		"-- migr:up\nCREATE TABLE other (id bigint);\n\n-- migr:down\nDROP TABLE other;\n")

	if _, _, err := runScripted(t, dir, actEmits, drivers, "up"); err != nil {
		t.Fatalf("up over a directory holding two languages: %v", err)
	}

	t.Log("Until M7-T10 this was refused, and before that refusal up applied every file its one")
	t.Log("runtime recognized and failed on the first it did not -- half applying the project and")
	t.Log("blaming the migration. Each file is now routed by its own extension, and the .sql half")
	t.Log("needs no process at all where the .go half needs a session.")

	want := []string{"CREATE TABLE scripted (id bigint)", "CREATE TABLE other (id bigint);"}
	if !slices.Equal(f.statements, want) {
		t.Errorf("up ran %v, want %v in that order", f.statements, want)
	}

	languages := map[string]string{versionAt(0): "go", versionAt(1): "sql"}

	for _, rec := range f.runs {
		if rec.Runtime != languages[rec.Version] {
			t.Errorf("%s recorded runtime %q, and its own file is a %s one", rec.Version,
				rec.Runtime, languages[rec.Version])
			t.Log("The row is per migration and the run is not: a mixed run stamping one language")
			t.Log("on every row is what engine.Options.Runtime being a string used to guarantee.")
		}
	}
}

func TestAJavaScriptMigrationRunsInATypeScriptProject(t *testing.T) {
	dir, f, drivers := tsProject(t, "add_scripted")
	write(t, filepath.Join(dir, scaffoldDir, versionAt(1)+"_add_plain.js"), "exports.up = 0;\n")

	if _, _, err := runScripted(t, dir, actEmits, drivers, "up"); err != nil {
		t.Fatalf("up over a project holding a .js migration: %v", err)
	}

	t.Log("migr-runtime-node loads .ts, .js and .mjs, and M7-T8's one-extension-per-language check")
	t.Log("refused the last two in a project that was already TypeScript. A language owns a set.")

	if len(f.runs) != 2 {
		t.Fatalf("up recorded %d migrations, want 2", len(f.runs))
	}

	for _, rec := range f.runs {
		if rec.Runtime != string(config.LanguageTypeScript) {
			t.Errorf("%s recorded runtime %q, and both files are TypeScript's", rec.Version, rec.Runtime)
		}
	}
}

func TestTwoRuntimeSessionsShareOnePinnedConnectionWithoutOverlapping(t *testing.T) {
	dir, f, drivers := goProject(t, "add_scripted")
	write(t, filepath.Join(dir, scaffoldDir, versionAt(1)+"_add_other.ts"), "export const up = 0;\n")

	if _, _, err := runScripted(t, dir, actAsks, drivers, "up"); err != nil {
		t.Fatalf("up over a project needing two runtimes: %v", err)
	}

	t.Log("Both sessions' callbacks reach the same pinned driver.Conn, which is safe only because")
	t.Log("the engine asks one migration at a time. The fake counts calls in flight rather than")
	t.Log("serializing them, so a run that ever asked two migrations at once fails here.")

	if f.overlapping() {
		t.Error("two calls were in flight on one session at once")
	}

	if !f.asked("lock") {
		t.Error("a run holding two runtime sessions still takes one migration lock")
	}
}

func TestEveryCommandThatRunsOrChecksMigrationsReadsTwoLanguages(t *testing.T) {
	for _, command := range []string{"up", "down", "plan", "validate", "status"} {
		dir, _, drivers := goProject(t, "add_scripted")
		write(t, filepath.Join(dir, scaffoldDir, versionAt(1)+"_add_other.sql"),
			"-- migr:up\nCREATE TABLE other (id bigint);\n\n-- migr:down\nDROP TABLE other;\n")

		if _, _, err := runScripted(t, dir, actEmits, drivers, command); err != nil {
			t.Errorf("migr %s over a mixed directory: %v", command, err)
		}
	}
}

func TestEveryCommandRefusesAMigrationNothingHereReads(t *testing.T) {
	t.Log("status and history are reports and take no lock (ADR 0027), but a file no runtime could")
	t.Log("ever execute is not one of the five states either -- discovery already fails both for a")
	t.Log("filename the grammar refuses, and this is the same class of malformed project.")

	for _, command := range []string{"up", "down", "plan", "validate", "status"} {
		dir, _, drivers := goProject(t, "add_scripted")
		write(t, filepath.Join(dir, scaffoldDir, versionAt(1)+"_add_other.py"), "def up(s):\n    pass\n")

		_, _, err := runScripted(t, dir, actEmits, drivers, command)
		if got := ExitCode(err); got != ExitValidation {
			t.Errorf("migr %s over an unroutable file exited %d, want %d (%v)", command, got,
				ExitValidation, err)
		}

		for _, want := range []string{"_add_other.py", ".py", ".go", ".sql", ".ts"} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("migr %s: the refusal does not name %q: %v", command, want, err)
			}
		}
	}
}

func TestARuntimeThatWillNotStartIsTheWorldNotBeingReady(t *testing.T) {
	dir, _, drivers := goProject(t, "add_users")

	_, _, err := runWith(t, dir, noon, drivers, "up")
	if err == nil {
		t.Fatal("up ran migrations through an adapter that starts nothing")
	}

	t.Log("ADR 0029: the argument is wrong is 2, the world is not ready is 1. A toolchain that")
	t.Log("is missing or a module that will not build is the second, not the first.")

	if got := ExitCode(err); got != ExitRuntime {
		t.Errorf("ExitCode = %d, want %d (%v)", got, ExitRuntime, err)
	}
}

func TestALanguageThisBuildRegisteredNoAdapterForSaysSo(t *testing.T) {
	dir, _, drivers := goProject(t, "add_users")

	opts := &options{clock: ticking(noon), drivers: drivers, runtimes: runtimeRegistry(t)}

	_, _, err := runOptions(t, dir, opts, "up")
	if err == nil {
		t.Fatal("up ran migrations in a language this build has no adapter for")
	}

	t.Log("A build with the Go adapter compiled out and one whose SDK has not landed are")
	t.Log("different problems: the first is fixed by a different binary, the second by waiting.")

	if !strings.Contains(err.Error(), "no runtimes registered") {
		t.Errorf("the refusal does not say the registry is empty: %v", err)
	}

	if strings.Contains(err.Error(), "runtime adapter") {
		t.Errorf("the refusal claims an adapter this build does not have: %v", err)
	}
}

func TestAnUnknownDriverIsRefusedListingTheOnesThisBuildHas(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)
	write(t, filepath.Join(dir, config.DefaultFilename),
		strings.Replace(projectConfig, "driver: "+fakeName, "driver: oracle", 1))

	_, _, err := runWith(t, dir, noon, drivers, "up")
	if err == nil {
		t.Fatal("up opened a database with a driver this build does not have")
	}

	if !strings.Contains(err.Error(), fakeName) {
		t.Errorf("the refusal does not list the drivers this build does have: %v", err)
	}
}

func TestGivingUpOnTheMigrationLockExitsThree(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.lockErr = driver.ErrLockTimeout

	_, _, err := runWith(t, dir, noon, drivers, "up")
	if err == nil {
		t.Fatal("up ran without the migration lock")
	}

	if got := ExitCode(err); got != ExitLock {
		t.Errorf("ExitCode = %d, want %d", got, ExitLock)
	}

	if !f.asked("close") {
		t.Errorf("a run that never took the lock left its connection open: %q", f.calls)
	}
}

func TestARunAgainstAProjectWithNoMigrationsSaysThereIsNothingToDo(t *testing.T) {
	dir, f, drivers := project(t)

	stdout, _, err := runWith(t, dir, noon, drivers, "up")
	if err != nil {
		t.Fatalf("up: %v", err)
	}

	if !strings.Contains(stdout, "nothing to do") {
		t.Errorf("up does not say there was nothing to do:\n%s", stdout)
	}

	if len(f.statements) != 0 {
		t.Errorf("up ran %q against an empty migrations directory", f.statements)
	}
}

func TestAPreviewAndAnApplyDisagreeAboutNothing(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up", "--dry-run"); err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if len(f.statements) != 3 {
		t.Errorf("the apply ran %q, want what the dry run said it would", f.statements)
	}

	t.Log("The preview and the apply run the same reconcile, the same classify, the same emit and")
	t.Log("the same compile; only the last step differs. There is no second implementation to")
	t.Log("drift out of sync, which is what makes a dry run worth reading.")
}

func TestADryRunOfADirectionThatCannotRunIsStillRefused(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	_, _, err := runWith(t, dir, noon, drivers, "up", "--dry-run", "--step", "1", "--to", versionAt(0))
	if err == nil {
		t.Fatal("a dry run accepted two selectors")
	}

	if !errors.Is(err, errBothSelectors) || ExitCode(err) != ExitValidation {
		t.Errorf("the refusal is not the one a real run would get: %v", err)
	}
}

func TestARunThatDidNothingBeforeFailingPrintsNoSummary(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.refuse = "CREATE TABLE users (id bigint);"

	stdout, _, err := runWith(t, dir, noon, drivers, "up")
	if err == nil {
		t.Fatal("up reported success for a refused statement")
	}

	if stdout != "" {
		t.Errorf("a run that applied nothing printed a summary anyway:\n%s", stdout)
		t.Log("\"nothing to do\" above a refusal reads as two answers to one question, and the")
		t.Log("first one is wrong. The summary exists to show what committed; with nothing")
		t.Log("committed there is nothing it can honestly say that the error does not.")
	}
}

func TestADriftedMigrationExitsFourAndRunsNothing(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	write(t, filepath.Join(dir, scaffoldDir, fileAt(0, "add_users")),
		twoTables()[0].body()+"\n-- edited after it was applied\n")

	f.statements = nil

	stdout, _, err := runWith(t, dir, noon, drivers, "up")
	if err == nil {
		t.Fatal("up ran against a database carrying an edited migration")
	}

	if got := ExitCode(err); got != ExitDrift {
		t.Errorf("ExitCode = %d, want %d", got, ExitDrift)
	}

	if len(f.statements) != 0 || stdout != "" {
		t.Errorf("drift ran %q and printed %q, want neither", f.statements, stdout)
		t.Log("Drift refuses the whole queue before anything runs, including the migrations that")
		t.Log("have nothing to do with the one that was edited (M4-T7).")
	}
}

func TestDownRollsBackOnlyTheLastBatchWhenSeveralExist(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	f.statements = nil

	if _, _, err := runWith(t, dir, noon, drivers, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	if !slices.Equal(f.statements, []string{"DROP TABLE teams;"}) {
		t.Errorf("the rollback ran %q, want only the batch the second up recorded", f.statements)
		t.Log("Two invocations mean two batches, and a bare `migr down` reverses the last one.")
		t.Log("Passing All() here would undo every applied migration instead, which agrees with")
		t.Log("LastBatch only while the whole project fits in one batch (ADR 0023).")
	}
}

// refusingWriter is a stdout that cannot be written to, which a closed pipe or a full disk is.
type refusingWriter struct{}

// Write always fails, so a command that ignores what writing its result reported is visible.
func (refusingWriter) Write([]byte) (int, error) {
	return 0, errors.New("stdout is gone")
}

func TestARunWhoseSummaryCannotBeWrittenFails(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)
	t.Chdir(dir)

	cmd := rootCmd(&options{clock: ticking(noon), drivers: drivers})
	cmd.SetArgs([]string{"up"})
	cmd.SetOut(refusingWriter{})
	cmd.SetErr(&bytes.Buffer{})

	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Error("up reported success having failed to tell anyone what it did")
		t.Log("The migrations are applied and the operator was told nothing. Exit code 0 there")
		t.Log("says the summary they never saw was empty, which is the one reading that is wrong.")
	}
}

func TestAMigrationARuntimeDidNotLoadIsAFailureNamingTheFile(t *testing.T) {
	dir, f, drivers := goProject(t, "add_scripted")

	_, _, err := runScripted(t, dir, actLoadsNothing, drivers, "up")
	if err == nil {
		t.Fatal("up ran a project whose runtime loaded nothing")
	}

	t.Log("Nothing in this package called Session.Discovered before M7-T10, which is why a file the")
	t.Log("runtime could not load came back from the SDK as unknown_migration -- true and useless.")
	t.Log("A loader may skip a file it does not own only because this holds both sides to the route.")

	for _, want := range []string{"_add_scripted.go", "go runtime did not load it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	if len(f.statements) != 0 {
		t.Errorf("up ran %v before refusing", f.statements)
	}
}

func TestAMigrationTwoRuntimesClaimIsAFailureToo(t *testing.T) {
	dir, _, drivers := goProject(t, "add_scripted")

	_, _, err := runScripted(t, dir, actLoadsExtra, drivers, "up")
	if err == nil {
		t.Fatal("up ran a project whose runtime claimed a migration nothing routed to it")
	}

	t.Log("The reconciliation is two-directional on purpose: a runtime loading what was routed")
	t.Log("elsewhere is how one migration would be run twice, once by each language.")

	for _, want := range []string{strayVersion, "routed it elsewhere"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

func TestEverySessionARunStartedIsShutDownAndNotMerelyKilled(t *testing.T) {
	dir, _, drivers := goProject(t, "add_scripted")
	write(t, filepath.Join(dir, scaffoldDir, versionAt(1)+"_add_other.ts"), "export const up = 0;\n")

	if _, _, err := runScripted(t, dir, actEmits, drivers, "up"); err != nil {
		t.Fatalf("up over a project needing two runtimes: %v", err)
	}

	t.Log("Each scripted runtime writes a marker when it answers shutdown, so a closer that ended")
	t.Log("only the last session leaves one process to be killed by the process group instead --")
	t.Log("which is invisible to every other assertion here (ADR 0034, ADR 0048).")

	for _, lang := range scripted {
		marker := filepath.Join(dir, shutdownMarker+string(lang))

		if _, err := os.Stat(marker); err != nil {
			t.Errorf("the %s runtime was never shut down: %v", lang, err)
		}
	}
}
