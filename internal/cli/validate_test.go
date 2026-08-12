package cli

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/source/sql"
	"github.com/emrebdr/migr/internal/validate"
)

// decodeValidation reads the document --json promised, failing the test rather than the command.
func decodeValidation(t *testing.T, stdout string) validation {
	t.Helper()

	var result validation
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("migr validate --json emitted %v:\n%s", err, stdout)
	}

	return result
}

// filesFaulted returns the migration each reported problem belongs to, in the order reported.
func filesFaulted(result validation) []string {
	found := make([]string, 0, len(result.Problems))
	for _, problem := range result.Problems {
		found = append(found, problem.File)
	}

	return found
}

// declaring returns a migration that does something one-way and says so in the file.
func declaring(name, up, reason string) stagedMigration {
	return stagedMigration{name: name, up: up, declared: reason}
}

func TestValidateOpensNoDatabaseAtAll(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "validate"); err != nil {
		t.Fatalf("validate: %v", err)
	}

	if len(f.calls) != 0 {
		t.Errorf("validate asked the session for %q, want nothing at all", f.calls)
		t.Log("Compiling needs a driver and no connection, so this is the one command CI can run")
		t.Log("on a pull request with no database and no secret.")
	}
}

func TestValidateReportsEveryBrokenMigrationRatherThanTheFirst(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	for _, n := range []int{0, 1} {
		write(t, filepath.Join(dir, scaffoldDir, fileAt(n, twoTables()[n].name)),
			"CREATE TABLE t (id bigint);\n")
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "validate")
	if err == nil {
		t.Fatal("validate passed a project whose migrations have no markers")
	}

	want := []string{fileAt(0, "add_users"), fileAt(1, "add_teams")}

	if got := filesFaulted(decodeValidation(t, stdout)); !slices.Equal(got, want) {
		t.Errorf("validate reported %q, want a problem in each", got)
	}
}

func TestValidateReportsOneProblemForAMigrationItCannotRead(t *testing.T) {
	dir, _, drivers := project(t, twoTables()[:1]...)

	write(t, filepath.Join(dir, scaffoldDir, fileAt(0, "add_users")), "CREATE TABLE t (id bigint);\n")

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "validate")
	if err == nil {
		t.Fatal("validate passed a migration with no markers")
	}

	problems := decodeValidation(t, stdout).Problems

	if len(problems) != 1 || problems[0].Check != validate.CheckFormat {
		t.Errorf("validate reported %+v, want the one check that could run", problems)
		t.Log("Compiling a file nothing could read, and reversing it, are unanswerable rather")
		t.Log("than wrong: three complaints about one defect is how a report stops being read.")
	}
}

func TestValidateReportsAMigrationNothingCanUndo(t *testing.T) {
	dir, _, drivers := project(t, stagedMigration{
		name: "drop_sessions",
		up:   "DROP TABLE legacy_sessions;",
	})

	stdout, _, err := runWith(t, dir, noon, drivers, "validate")

	if ExitCode(err) != ExitValidation {
		t.Fatalf("validate exited %d over a migration nothing can undo, want %d",
			ExitCode(err), ExitValidation)
	}

	for _, want := range []string{"reverse", "down operation 1 (raw)", sql.MarkerIrreversible} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not mention %q:\n%s", want, stdout)
		}
	}
}

func TestValidatePassesAMigrationThatDeclaresItCannotBeUndone(t *testing.T) {
	dir, _, drivers := project(t,
		declaring("drop_sessions", "DROP TABLE legacy_sessions;", "the rows are gone; use a backup"))

	stdout, _, err := runWith(t, dir, noon, drivers, "validate")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, stdout)
	}

	if !strings.Contains(stdout, "no problems found") {
		t.Errorf("validate did not pass a migration whose author declared it one-way:\n%s", stdout)
	}

	if !strings.Contains(stdout, noDatabase) {
		t.Errorf("validate did not say it opened nothing:\n%s", stdout)
	}
}

func TestARollbackOfADeclaredMigrationRefusesWithItsAuthorsReason(t *testing.T) {
	reason := "the rows are gone; use a backup"
	dir, _, drivers := project(t, declaring("drop_sessions", "DROP TABLE legacy_sessions;", reason))

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	_, _, err := runWith(t, dir, noon, drivers, "down")
	if err == nil {
		t.Fatal("down succeeded over a migration declared irreversible")
	}

	if !strings.Contains(err.Error(), reason) {
		t.Errorf("down refused with %v, want the reason the author gave", err)
		t.Log("A migration that validates clean must not then fail a rollback with a message")
		t.Log("about ir.Raw being opaque: the author already answered that question.")
	}

	if !strings.Contains(err.Error(), fileAt(0, "drop_sessions")) {
		t.Errorf("the refusal does not name the migration: %v", err)
	}

	if strings.Contains(err.Error(), "asking what this migration undoes") {
		t.Errorf("the refusal reads as a failure to read the file: %v", err)
		t.Log("The file answered that question; the answer was that there is no undo. Wrapping a")
		t.Log("declaration as a read failure tells an operator to go look at a file that is fine.")
	}
}

func TestValidateReportsAnOperationTheDriverRefuses(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.unsupported = "CREATE TABLE teams (id bigint);"

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "validate")
	if err == nil {
		t.Fatal("validate passed an operation the driver refuses")
	}

	problems := decodeValidation(t, stdout).Problems

	if len(problems) != 1 || problems[0].Check != validate.CheckCompile {
		t.Fatalf("validate reported %+v, want the refusal", problems)
	}

	if problems[0].Operation != 1 || problems[0].Direction != "up" {
		t.Errorf("the refusal sits at %s operation %d, want the operation it refused",
			problems[0].Direction, problems[0].Operation)
	}
}

func TestValidatePassesACleanProject(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "validate")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, stdout)
	}

	if !strings.Contains(stdout, "no problems found") || !strings.Contains(stdout, "2 migrations") {
		t.Errorf("validate did not pass a project whose migrations both author a down:\n%s", stdout)
	}
}

func TestValidateStillNeedsAConfigItCanRead(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	write(t, filepath.Join(dir, config.DefaultFilename), "version: 1\n")

	_, _, err := runWith(t, dir, noon, drivers, "validate")

	if ExitCode(err) != ExitValidation {
		t.Errorf("validate exited %d over a config with no databases", ExitCode(err))
		t.Log("Opening no database is not the same as needing no configuration: which engine to")
		t.Log("compile for comes from migr.yaml, and there is no answer without one.")
	}
}

func TestValidateJSONIsValidAndCarriesNothingOnStderr(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, stderr, err := runWith(t, dir, noon, drivers, "--json", "validate")
	if err != nil {
		t.Fatalf("validate --json: %v", err)
	}

	if stderr != "" {
		t.Errorf("validate --json wrote %q to stderr", stderr)
	}

	result := decodeValidation(t, stdout)

	if result.Driver != fakeName || result.Checked != 2 || len(result.Problems) != 0 {
		t.Errorf("validate --json reported %+v, want the engine it compiled for and no problems",
			result)
	}
}

func TestValidateSaysHowThisFormatAnswersAnIrreversibleMigration(t *testing.T) {
	dir, _, drivers := project(t, stagedMigration{name: "one_way", up: "DROP TABLE legacy;"})

	stdout, _, err := runWith(t, dir, noon, drivers, "validate")
	if err == nil {
		t.Fatal("validate passed a migration nothing can undo")
	}

	if !strings.Contains(stdout, sql.MarkerDown) || !strings.Contains(stdout, sql.MarkerIrreversible) {
		t.Errorf("the report does not say how a .sql migration answers this:\n%s", stdout)
		t.Log("ir.Raw's reason says to write the undo `in Down`, which is SDK language a .sql")
		t.Log("author cannot act on. The command knows the project's language; the check does not.")
	}
}

func TestValidateRefusesAProjectItCannotOrder(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	write(t, filepath.Join(dir, scaffoldDir, "not-a-migration.sql"), "-- migr:up\nSELECT 1;\n")

	_, _, err := runWith(t, dir, noon, drivers, "validate")

	if ExitCode(err) != ExitValidation {
		t.Errorf("validate exited %d over a file that is not a migration", ExitCode(err))
		t.Log("Discovery is what enumerates the set every later check walks, so a file nothing")
		t.Log("can name stops the run rather than being reported alongside the rest.")
	}
}

func TestValidateFailsWithAnErrorItsExitCodeCanBeReadFrom(t *testing.T) {
	dir, _, drivers := project(t, stagedMigration{name: "one_way", up: "DROP TABLE legacy;"})

	_, _, err := runWith(t, dir, noon, drivers, "validate")

	if !errors.Is(err, errValidationFailed) {
		t.Errorf("validate failed with %v, want the sentinel a caller can recognize", err)
	}
}
