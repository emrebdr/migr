//go:build integration

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/rule"
	"github.com/emrebdr/migr/internal/schema"
)

// goHistoryTable keeps this suite's runs out of the tables the .sql suite and the driver use.
const goHistoryTable = "migr_e2e_go_migrations"

// goModule is the module the Go migrations are built inside, written rather than kept in the tree.
const goModule = `module example.com/e2e

go 1.26

require github.com/emrebdr/migr/sdk/go v0.0.0

replace github.com/emrebdr/migr/sdk/go => `

// goMigrations are the two this suite applies: one authoring its undo, one leaving it to be derived.
var goMigrations = []struct{ name, body string }{
	{
		name: "20260727T160000_add_e2e_go_accounts.go",
		body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddAccounts{}) }

// AddAccounts creates the table this suite's round trip is measured over.
type AddAccounts struct{}

// Up creates the table and then indexes it, since a new table states only its key inline.
func (m AddAccounts) Up(ctx context.Context, s *migr.Schema) error {
	if err := s.CreateTable("e2e_go_accounts").
		Column("id", migr.Int64()).
		Column("email", migr.String(255)).
		PrimaryKey("id").
		Apply(ctx); err != nil {
		return err
	}

	return s.Table("e2e_go_accounts").
		AddIndex("idx_e2e_go_accounts_email", "email").
		Apply(ctx)
}

// Down drops both, recording what the table held so the drop inverts exactly.
func (m AddAccounts) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("e2e_go_accounts").
		DropIndex("idx_e2e_go_accounts_email").Was(migr.Columns("email")).
		Drop().Was(migr.TableDef{
		Columns: []migr.Column{
			{Name: "id", Type: migr.Int64()},
			{Name: "email", Type: migr.String(255)},
		},
		PrimaryKey: []string{"id"},
	}).Apply(ctx)
}
`,
	},
	{
		name: "20260727T160100_add_e2e_go_balance.go",
		body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddBalance{}) }

// AddBalance adds one column and authors no undo, so the engine derives the inverse itself.
type AddBalance struct{}

// Up adds the column. There is deliberately no Down: invariant #10's derivation is the point.
func (m AddBalance) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("e2e_go_accounts").
		AddColumn("balance", migr.Decimal(12, 2)).Null().
		Apply(ctx)
}
`,
	},
}

// goProject writes a Go project migr can run, and returns the path of its migr.yaml.
func goProject(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	config := filepath.Join(dir, "migr.yaml")

	body := fmt.Sprintf(`version: 1
migrations:
  dir: ./migrations
  language: go
databases:
  default:
    driver: postgres
    dsn: %s
history:
  table: %s
`, dsn(), goHistoryTable)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writeGoModule(t, dir)
	writeGoMigrations(t, filepath.Join(dir, "migrations"))

	return config
}

// writeGoModule writes the go.mod pointing at this repository's SDK, which a user would go get.
func writeGoModule(t *testing.T, dir string) {
	t.Helper()

	sdk, err := filepath.Abs(filepath.Join("..", "..", "sdk", "go"))
	if err != nil {
		t.Fatalf("resolving the SDK: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goModule+sdk+"\n"), 0o600); err != nil {
		t.Fatalf("staging the project module: %v", err)
	}
}

// writeGoMigrations stages every migration this suite runs, in the directory migr.yaml names.
func writeGoMigrations(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, m := range goMigrations {
		if err := os.WriteFile(filepath.Join(dir, m.name), []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}
}

func TestAGoMigrationAppliesAndRollsBackAgainstPostgreSQL(t *testing.T) {
	config := goProject(t)

	if err := migr(t, config, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	before := digest(t, goHistoryTable)

	t.Cleanup(func() {
		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	applied := digest(t, goHistoryTable)
	if applied == before {
		t.Fatal("up changed nothing about the schema, so there is no round trip to measure")
	}

	for _, want := range []string{"e2e_go_accounts", "idx_e2e_go_accounts_email", "balance"} {
		if !strings.Contains(applied, want) {
			t.Errorf("the schema does not hold %q after up:\n%s", want, applied)
		}
	}

	if err := migr(t, config, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	t.Log("Both directions crossed the wire as IR from a Go source: the runner was generated,")
	t.Log("built, and started by the CLI, and every statement here was compiled by the driver.")

	if got := digest(t, goHistoryTable); got != before {
		t.Errorf("the schema did not return to what it was:\n%s", schema.Difference(before, got))
	}
}

func TestADerivedRollbackDropsWhatTheGoSDKNeverWroteAnUndoFor(t *testing.T) {
	config := goProject(t)

	if err := migr(t, config, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if !strings.Contains(digest(t, goHistoryTable), "balance") {
		t.Fatal("the column the derived rollback is meant to drop was never added")
	}

	if err := migr(t, config, "down", "--step", "1"); err != nil {
		t.Fatalf("down --step 1: %v", err)
	}

	t.Log("A .sql migration can never reach here: ir.Raw has no inverse, so invariant #10's")
	t.Log("derivation had never run against a real server until a Go source could author no Down.")

	rolled := digest(t, goHistoryTable)
	if strings.Contains(rolled, "balance") {
		t.Errorf("the derived rollback left the column behind:\n%s", rolled)
	}

	if !strings.Contains(rolled, "e2e_go_accounts") {
		t.Errorf("the derived rollback took the table with it:\n%s", rolled)
	}
}

func TestASecondUpSucceedsAfterAGoRollback(t *testing.T) {
	config := goProject(t)

	if err := migr(t, config, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	t.Log("A rollback that recorded success without dropping anything fails on the second up and")
	t.Log("nowhere else: the history says the migration is undone either way.")

	for _, direction := range []string{"up", "down", "up"} {
		if err := migr(t, config, direction); err != nil {
			t.Fatalf("%s: %v", direction, err)
		}
	}
}

func TestStatusAndPlanReportAGoProject(t *testing.T) {
	config := goProject(t)

	if err := migr(t, config, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	planned := migrOut(t, config, "plan")
	for _, want := range []string{"create_table", "add_index", "add_column", "native"} {
		if !strings.Contains(planned, want) {
			t.Errorf("migr plan does not print %q for a Go project:\n%s", want, planned)
		}
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	status := migrOut(t, config, "status")
	for _, m := range goMigrations {
		if got := stateOf(t, status, m.name); got != "applied" {
			t.Errorf("%s reads %q after up, want applied:\n%s", m.name, got, status)
		}
	}
}

// assertWarnsOfTheDerivedDown holds the one lint rule this project's own fixtures trip (M10-T5).
func assertWarnsOfTheDerivedDown(t *testing.T, out string) {
	t.Helper()

	for _, want := range []string{"1 warning", string(rule.MissingDown), "20260727T160100"} {
		if !strings.Contains(out, want) {
			t.Errorf("validate does not warn that a migration authors no down (%q):\n%s", want, out)
			t.Log("The second fixture omits its Down so invariant #10's derivation runs against a")
			t.Log("real database. missing-down is what says so, and at warn rather than error")
			t.Log("precisely because that omission is deliberate here.")
		}
	}
}

func TestValidateChecksAGoProjectWithoutOpeningADatabase(t *testing.T) {
	config := goProject(t)

	out := migrOut(t, config, "validate")

	t.Log("ADR 0028's promise survives a language that needs a runtime: the process starts, the")
	t.Log("module is built, and every database callback it could make refuses (ADR 0040).")

	if !strings.Contains(out, "no problems found") {
		t.Fatalf("validate found something in migrations the stack then runs:\n%s", out)
	}

	assertWarnsOfTheDerivedDown(t, out)

	if err := migr(t, config, "up"); err != nil {
		t.Errorf("what validate passed, up refused: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})
}
