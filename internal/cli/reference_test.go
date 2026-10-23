package cli

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/schema"
)

// M10-T3's done-when through the command: a column added to a table nothing creates, and a foreign
// key to a column nothing adds, each caught before anything runs. The check needs the database, so
// every case here passes --connect -- neither of its two bases is sound alone.

func TestValidateConnectCatchesAReferenceNoProjectionSatisfies(t *testing.T) {
	dir, f, drivers := goProject(t, "add_scripted")
	f.live = scriptedSchema(t)

	stdout, _, err := runScripted(t, dir, actStrays, drivers, "validate", "--connect")
	if err == nil {
		t.Fatalf("migr validate passed a migration naming a table nothing creates:\n%s", stdout)
	}

	if got := ExitCode(err); got != ExitValidation {
		t.Errorf("migr validate exited %d, want %d: %v", got, ExitValidation, err)
		t.Log("The argument is wrong rather than the world unready: the reference is")
		t.Log("refused from the project's own beginning too, so it is the migration at fault.")
	}

	for _, want := range []string{"reference", strayTable, "no such table", "fk_stray", "ghost"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not name %q:\n%s", want, stdout)
		}
	}
}

// Not merely the word: the report's own closing sentence says what the references were projected
// from whether it found any or not, so what this looks for is a problem line opening with the label.

// reportsReference says whether a report holds a finding from this check.
func reportsReference(stdout string) bool {
	return strings.Contains(stdout, "\n    reference ")
}

func TestValidateNamesBothProjectionsItJudgedReferencesAgainst(t *testing.T) {
	dir, f, drivers := goProject(t, "add_scripted")
	f.live = holdingStray(t)

	stdout, _, _ := runScripted(t, dir, actStrays, drivers, "validate", "--connect")

	for _, want := range []string{"the project's own beginning", "the database as it is",
		"every one of them refused"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, stdout)
		}
	}
}

func TestValidateOfflineSaysTheReferencesWereNotCheckedEither(t *testing.T) {
	dir, _, drivers := goProject(t, "add_scripted")

	stdout, _, err := runScripted(t, dir, actStrays, drivers, "validate")
	if err != nil {
		t.Fatalf("an offline run failed over references it could not judge: %v\n%s", err, stdout)
	}

	if reportsReference(stdout) {
		t.Errorf("an offline run judged references anyway:\n%s", stdout)
	}

	for _, want := range []string{noDatabase, "the references the migrations make was " +
		"checked", "--connect"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, stdout)
		}
	}

	t.Log("A run with one basis has none, so it judges no references and says so -- which is the")
	t.Log("shape capability already has. Reporting from the project's own beginning alone would")
	t.Log("have every migration migr diff writes fail this command.")
}

func TestValidateConnectIsSilentOnAProjectThatBuildsOnWhatTheDatabaseHolds(t *testing.T) {
	dir, f, drivers := goProject(t, "add_scripted")
	f.live = holdingStray(t)

	stdout, _, _ := runScripted(t, dir, actStrays, drivers, "validate", "--connect")

	if reportsReference(stdout) {
		t.Errorf("the live schema did not satisfy the reference:\n%s", stdout)
		t.Log("migr adopted into an existing database is the ordinary case, so the live schema is")
		t.Log("one of the two bases rather than a cross-check on the project's own.")
	}
}

// holdingStray is a database that already holds what actStrays names, which no migration creates.
func holdingStray(t *testing.T) *schema.Snapshot {
	t.Helper()

	held := scriptedSchema(t)
	id := held.Tables[0].Columns[0]

	ghost := id
	ghost.Name = "ghost"

	held.Tables[0].Columns = append(held.Tables[0].Columns, ghost)
	held.Tables = append(held.Tables, schema.Table{Name: strayTable, Columns: []schema.Column{id}})

	return held
}
