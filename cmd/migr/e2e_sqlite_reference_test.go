package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// M10-T3's done-when against a real catalog: a column added to a table nothing creates, and a
// foreign key to a column nothing adds, each caught by migr validate --connect before anything runs.
//
// Untagged, for the reason the rest of this engine's suites are: SQLite needs a t.TempDir() and the
// Go toolchain is present by definition when go test is what is running (M6-T6).
//
// It is this engine rather than PostgreSQL because the check reads Conn.Introspect, which every
// driver answers the same way -- so what a second engine would add is a second catalog
// rather than a second answer, and the snapshot conformance suite already holds all five to one.

// strayingMigration adds a column to a table no migration in this project creates.
const strayingMigration = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

type Migration20260929T150000 struct{}

func (Migration20260929T150000) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("absent_table").AddColumn("note", migr.String(64)).Null().Apply(ctx)
}

func init() { migr.Register(Migration20260929T150000{}) }
`

// ghostKeyMigration keys a table it creates to a column the referenced table does not hold.
const ghostKeyMigration = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

type Migration20260929T150100 struct{}

func (Migration20260929T150100) Up(ctx context.Context, s *migr.Schema) error {
	if err := s.CreateTable("tickets").
		Column("id", migr.Int64()).
		Column("owner", migr.Int64()).
		PrimaryKey("id").
		Apply(ctx); err != nil {
		return err
	}

	return s.Table("tickets").
		AddForeignKey("fk_tickets_owner", []string{"owner"}, "rebuilt_accounts",
			[]string{"ghost"}).
		Apply(ctx)
}

func init() { migr.Register(Migration20260929T150100{}) }
`

// stageReferencing adds one migration to this suite's project, beside the ones it already stages.
func stageReferencing(t *testing.T, config, name, body string) {
	t.Helper()

	path := filepath.Join(filepath.Dir(config), "migrations", name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("staging %s: %v", name, err)
	}
}

func TestValidateCatchesAColumnAddedToATableNothingCreates(t *testing.T) {
	config, _ := sqliteGoProject(t)

	stageReferencing(t, config, "20260929T150000_stray.go", strayingMigration)

	out, err := migrReport(t, config, "validate", "--connect")
	if err == nil {
		t.Fatalf("migr validate --connect passed a column added to a table nothing creates:\n%s",
			out)
	}

	for _, want := range []string{"reference", "absent_table", "no such table"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not name %q:\n%s", want, out)
		}
	}
}

func TestValidateCatchesAForeignKeyToAColumnNothingAdds(t *testing.T) {
	config, _ := sqliteGoProject(t)

	stageReferencing(t, config, "20260929T150100_ghost_key.go", ghostKeyMigration)

	out, err := migrReport(t, config, "validate", "--connect")
	if err == nil {
		t.Fatalf("migr validate --connect passed a key to a column nothing adds:\n%s", out)
	}

	for _, want := range []string{"reference", "add_foreign_key", "rebuilt_accounts.ghost"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not name %q:\n%s", want, out)
		}
	}

	t.Log("The referenced table is one an earlier migration of this project creates, so the key's")
	t.Log("own table and column are fine and only the far end is wrong -- which is the half a")
	t.Log("compiler cannot see, every driver compiling a key by name alone.")
}

func TestValidateIsSilentOnTheProjectsOwnMigrationsWhetherAppliedOrNot(t *testing.T) {
	config, _ := sqliteGoProject(t)

	if err := migr(t, config, "validate", "--connect"); err != nil {
		t.Fatalf("a correct project was refused before it ran: %v", err)
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if err := migr(t, config, "validate", "--connect"); err != nil {
		t.Fatalf("a correct project was refused after it ran: %v", err)
	}

	t.Log("Both ends of the live-catalog problem, measured against one real catalog. Before the run the live")
	t.Log("schema holds none of what the migrations name; after it, it holds all of it and every")
	t.Log("create_table conflicts. Reporting only what both projections refuse is what survives both.")
}

func TestValidateOfflineJudgesNoReferencesAndSaysSo(t *testing.T) {
	config, _ := sqliteGoProject(t)

	stageReferencing(t, config, "20260929T150000_stray.go", strayingMigration)

	out := migrOut(t, config, "validate")

	if strings.Contains(out, "\n    reference ") {
		t.Errorf("an offline run judged references against the project alone:\n%s", out)
	}

	if !strings.Contains(out, "the references the migrations make was checked") {
		t.Errorf("the report does not say the references went unjudged:\n%s", out)
	}
}
