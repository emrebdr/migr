package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three data operations through the whole stack, untagged because this engine needs no daemon.
//
// What a row holds afterwards is the driver's own suite to assert -- reading one from here would be
// SQL composed above a driver package (invariant #3). What only this suite can show is that a Go
// migration's builder reaches the engine as irVersion 2 operations, that migr plan says which rows
// each writes, and that migr validate and migr up both accept them against a real catalog.

// peopleMigration creates the two tables the data migration below writes into.
const peopleMigration = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddPeople{}) }

// AddPeople creates a table with room for derived columns, and the archive it is copied into.
type AddPeople struct{}

// Up creates both tables.
func (AddPeople) Up(ctx context.Context, s *migr.Schema) error {
	if err := s.CreateTable("people").
		Column("id", migr.Int64()).
		Column("email", migr.String(255)).
		Column("nickname", migr.String(64)).Null().
		Column("email_key", migr.String(255)).Null().
		PrimaryKey("id").
		Apply(ctx); err != nil {
		return err
	}

	return s.CreateTable("people_archive").
		Column("id", migr.Int64()).
		Column("email", migr.String(255)).
		PrimaryKey("id").
		Apply(ctx)
}
`

// shapingMigration writes rows through all three data operations, which no inverse can undo.
const shapingMigration = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(ShapePeople{}) }

// ShapePeople fills, rewrites and copies.
type ShapePeople struct{}

// Up writes the rows.
func (ShapePeople) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").
		Backfill("nickname", migr.String(64), migr.Literal("anonymous")).
		Transform("email_key", migr.String(255), "email", migr.TransformLower).
		CopyInto("people_archive",
			migr.CopyColumn{From: "id", To: "id"}, migr.CopyColumn{From: "email", To: "email"}).
		Apply(ctx)
}

// Irreversible says why a rollback stops here.
func (ShapePeople) Irreversible() string {
	return "the rows these operations replaced are recorded nowhere"
}
`

// dataProject is a Go project on SQLite holding the two migrations above and nothing else.
func dataProject(t *testing.T) string {
	t.Helper()

	config, _ := sqliteGoProject(t)
	dir := filepath.Join(filepath.Dir(config), "migrations")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the staged migrations: %v", err)
	}

	for _, entry := range entries {
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			t.Fatalf("clearing %s: %v", entry.Name(), err)
		}
	}

	for name, body := range map[string]string{
		"20260929T170000_add_people.go":   peopleMigration,
		"20260929T170100_shape_people.go": shapingMigration,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", name, err)
		}
	}

	return config
}

func TestTheDataOperationsReachARealDatabaseThroughEveryCommand(t *testing.T) {
	config := dataProject(t)

	if err := migr(t, config, "up", "--step", "1"); err != nil {
		t.Fatalf("applying the tables: %v", err)
	}

	planned := migrOut(t, config, "plan")
	for _, want := range []string{
		`rows: fills the NULLs of "people.nickname" with "anonymous", and no other row`,
		`UPDATE "people" SET "nickname" = 'anonymous' WHERE "people"."nickname" IS NULL;`,
		`rows: rewrites "people.email_key" in every row, from "email" through lower`,
		`rows: copies every row of "people" into "people_archive", 2 columns`,
		`INSERT INTO "people_archive" ("id", "email")`,
	} {
		if !strings.Contains(planned, want) {
			t.Errorf("migr plan does not show %q:\n%s", want, planned)
		}
	}

	refused, err := migrReport(t, config, "validate", "--connect")
	if err == nil || !strings.Contains(refused, "allow_destructive: [data_transform]") {
		t.Errorf("validate did not report the unpermitted data_transform by kind:\n%s", refused)
		t.Log("data_transform overwrites every value of the column it writes, so it is permitted")
		t.Log("by name as every other destructive kind is -- and a project names it in migr.yaml.")
	}

	if out, err := migrReport(t, config, "validate", "--connect", "--allow-destructive"); err != nil {
		t.Errorf("migr validate --connect refused the data migration once permitted:\n%s", out)
		t.Log("Every name the three operations use is one the migration before them created, so")
		t.Log("the reference check has nothing to find.")
	}

	if err := migr(t, config, "up", "--allow-destructive"); err != nil {
		t.Fatalf("applying the data migration: %v", err)
	}

	if out := migrFailure(t, config, "down"); !strings.Contains(out, "recorded nowhere") {
		t.Errorf("a rollback over the data migration did not halt with the author's reason:\n%s",
			out)
	}
}
