package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/schema"
)

// contradicting stages a Go project whose one migration creates a table this database already holds.
func contradicting(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := goProject(t, "add_scripted")
	f.live = scriptedSchema(t)

	return dir, f, drivers
}

// scriptedSchema is the database the scripted migration's operations are judged against.
func scriptedSchema(t *testing.T) *schema.Snapshot {
	t.Helper()

	logical, err := schema.Parse("int64")
	if err != nil {
		t.Fatalf("schema.Parse: %v", err)
	}

	typed, err := schema.ExactType(logical, "bigint")
	if err != nil {
		t.Fatalf("ExactType: %v", err)
	}

	return &schema.Snapshot{Tables: []schema.Table{{
		Name:    scriptedTable,
		Columns: []schema.Column{{Name: "id", Type: typed}},
	}}}
}

func TestUpRefusesAMigrationThisDatabasesSchemaContradicts(t *testing.T) {
	dir, f, drivers := contradicting(t)

	_, stderr, err := runScripted(t, dir, actTypes, drivers, "up")

	if got := ExitCode(err); got != ExitRuntime {
		t.Fatalf("migr up exited %d, want %d: %v", got, ExitRuntime, err)
	}

	if !errors.Is(err, engine.ErrUnrestorable) {
		t.Fatalf("migr up returned %v, want the refusal", err)
	}

	for _, want := range []string{"already holds a table", scriptedTable} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the failure does not say %q:\n%s", want, stderr)
		}
	}

	if len(f.statements) != 0 {
		t.Errorf("the run sent %v, and a refused migration runs nothing", f.statements)
	}

	if len(f.runs) != 0 {
		t.Errorf("the run recorded %d rows for a migration it refused", len(f.runs))
	}

	t.Log("Exit 1 rather than 2, which is ADR 0029's rule: the same command line succeeds against")
	t.Log("a database that does not contradict the migration, so the world is what is not ready.")
}

func TestARefusedMigrationIsOneJSONDocumentOnStderr(t *testing.T) {
	dir, _, drivers := contradicting(t)

	stdout, stderr, err := runScripted(t, dir, actTypes, drivers, "up", "--json")
	if !errors.Is(err, engine.ErrUnrestorable) {
		t.Fatalf("migr up --json returned %v, want the refusal", err)
	}

	if strings.TrimSpace(stdout) != "" {
		t.Errorf("a refused run printed a result document:\n%s", stdout)
	}

	var found struct {
		Code         int    `json:"code"`
		Error        string `json:"error"`
		Migration    string `json:"migration"`
		Operation    string `json:"operation"`
		Unrestorable *struct {
			Version    string `json:"version"`
			Direction  string `json:"direction"`
			Operations []struct {
				Operation int32  `json:"operation"`
				Kind      string `json:"kind"`
				Subject   string `json:"subject"`
				Sense     string `json:"sense"`
				Detail    string `json:"detail"`
			} `json:"operations"`
		} `json:"unrestorable"`
	}

	if err := json.Unmarshal([]byte(stderr), &found); err != nil {
		t.Fatalf("the failure is not one JSON document: %v\n%s", err, stderr)
	}

	if found.Code != ExitRuntime || found.Unrestorable == nil {
		t.Fatalf("the document reports code %d and %v", found.Code, found.Unrestorable)
	}

	if found.Operation != "create_table" || found.Unrestorable.Direction != "up" {
		t.Errorf("the document names operation %q in the %q half",
			found.Operation, found.Unrestorable.Direction)
	}

	if len(found.Unrestorable.Operations) != 1 {
		t.Fatalf("the document holds %d disagreements", len(found.Unrestorable.Operations))
	}

	one := found.Unrestorable.Operations[0]
	if one.Sense != string(engine.SenseStated) || one.Subject != scriptedTable {
		t.Errorf("the disagreement reads %+v", one)
	}

	if strings.Contains(found.Error, "\n") {
		t.Errorf("the error field carries the whole paragraph: %q", found.Error)
	}
}

func TestPlanMarksWhatUpWillRefuseAndExitsZero(t *testing.T) {
	dir, _, drivers := contradicting(t)

	stdout, _, err := runScripted(t, dir, actTypes, drivers, "plan")
	if err != nil {
		t.Fatalf("migr plan returned %v, and a preview refuses nothing", err)
	}

	for _, want := range []string{"~", "already holds a table", "marked ~ above"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the plan does not say %q:\n%s", want, stdout)
		}
	}

	t.Log("Invariant #7's rule applied without exception: migr plan says what migr up will refuse")
	t.Log("and exits 0, as migr status reports drift at 0 while migr up exits 4.")
}

func TestPlanJSONCarriesTheDisagreementsAsFields(t *testing.T) {
	dir, _, drivers := contradicting(t)

	stdout, _, err := runScripted(t, dir, actTypes, drivers, "plan", "--json")
	if err != nil {
		t.Fatalf("migr plan --json: %v", err)
	}

	var found struct {
		Migrations []struct {
			Unrestorable []struct {
				Kind  string `json:"kind"`
				Sense string `json:"sense"`
			} `json:"unrestorable"`
		} `json:"migrations"`
	}

	if err := json.Unmarshal([]byte(stdout), &found); err != nil {
		t.Fatalf("the plan is not one JSON document: %v\n%s", err, stdout)
	}

	if len(found.Migrations) != 1 || len(found.Migrations[0].Unrestorable) != 1 {
		t.Fatalf("the document holds %+v", found.Migrations)
	}

	if found.Migrations[0].Unrestorable[0].Kind != "create_table" {
		t.Errorf("the disagreement reads %+v", found.Migrations[0].Unrestorable[0])
	}
}

func TestValidateSaysNothingAboutWhatOnlyARunCanJudge(t *testing.T) {
	dir, f, drivers := contradicting(t)

	stdout, _, _ := runScripted(t, dir, actTypes, drivers, "validate", "--connect")

	if strings.Contains(stdout, "already holds a table") {
		t.Errorf("migr validate judged a migration against the live schema:\n%s", stdout)
	}

	if f.asked("introspect") {
		t.Error("migr validate read the schema, which ADR 0064's C2 is the reason it must not")
		t.Log("A check reading the live catalog calls a correct migration broken whenever the")
		t.Log("migration that creates its table has not run yet, and the projection that would")
		t.Log("fix that has no task -- so this is the run's gate and not a validate check.")
	}
}

func TestAMigrationNothingContradictsRunsUntouched(t *testing.T) {
	dir, f, drivers := goProject(t, "add_scripted")

	if _, _, err := runScripted(t, dir, actTypes, drivers, "up"); err != nil {
		t.Fatalf("migr up: %v", err)
	}

	if len(f.runs) != 1 {
		t.Fatalf("the run recorded %d rows", len(f.runs))
	}

	if !f.asked("introspect") {
		t.Errorf("the session was asked for %v, and the gate must read the schema", f.calls)
	}
}
