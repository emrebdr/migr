package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two emulated operations in one compilation, end to end, which is what M11-T7 is.
//
// ADR 0022 compiled every operation before running any of them and ADR 0053 compiles an emulated
// one against the database, so the second expansion in one compilation was rendered from a catalog
// the first had already changed. Measured on this engine before the fix: a migration whose up is
// alter_column ... NOT NULL then add_primary_key came out as "tag" varchar(64) with no NOT NULL at
// all, because the key's rebuild re-rendered the table from the pre-alter catalog.
//
// Neither shape is reachable with one emulated operation per migration and one emulated migration
// per run, which is why the suites beside this one never met it. All four cases here are about an
// operation reading what the operation before it left, and nothing in them is about SQLite.
//
// Untagged like the rest of this engine's suites: a t.TempDir() and the Go toolchain are the whole
// requirement (ADR 0050, M6-T6).

// expandHistoryTable keeps this suite's runs out of every other table here.
const expandHistoryTable = "migr_e2e_sqlite_expand"

// expandPreamble is the module header every migration in this suite shares.
const expandPreamble = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)
`

// expandBase creates the table the cases below change, with a nullable column and no key.
const expandBase = expandPreamble + `
func init() { migr.Register(AddCrates{}) }

// AddCrates creates the table every case here rebuilds.
type AddCrates struct{}

// Up creates it with a nullable column, which is what the two emulated operations then tighten.
func (m AddCrates) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("crates").
		Column("id", migr.Int64()).
		Column("tag", migr.String(64)).Null().
		Apply(ctx)
}

// Down drops it, recording what it held so the drop inverts exactly.
func (m AddCrates) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("crates").Drop().Was(migr.TableDef{
		Columns: []migr.Column{
			{Name: "id", Type: migr.Int64()},
			{Name: "tag", Type: migr.String(64), Nullable: true},
		},
	}).Apply(ctx)
}
`

// expandBothInOne is the done-when's first half: two emulated operations in one migration.
const expandBothInOne = expandPreamble + `
func init() { migr.Register(KeyCrates{}) }

// KeyCrates tightens a column and then keys it, which this engine rebuilds the table for twice.
type KeyCrates struct{}

// Up states the nullability the key implies and then adds the key, in that order.
func (m KeyCrates) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("crates").
		AlterColumn("tag").NotNull().WasNullable(true).
		AddPrimaryKey("pk_crates", "tag").
		Apply(ctx)
}
`

// expandTightenOnly is the first of two emulated migrations a single rollback undoes.
const expandTightenOnly = expandPreamble + `
func init() { migr.Register(TightenCrates{}) }

// TightenCrates makes the column NOT NULL, which this engine rebuilds the table for.
type TightenCrates struct{}

// Up tightens it, and the inverse is derived from the state this records.
func (m TightenCrates) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("crates").
		AlterColumn("tag").NotNull().WasNullable(true).
		Apply(ctx)
}
`

// expandKeyOnly is the second, so one migr down holds two rebuilds in one compilation.
const expandKeyOnly = expandPreamble + `
func init() { migr.Register(KeyCrates{}) }

// KeyCrates adds the primary key, which this engine also rebuilds the table for.
type KeyCrates struct{}

// Up adds the key over the column the migration before it tightened.
func (m KeyCrates) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("crates").
		AddPrimaryKey("pk_crates", "tag").
		Apply(ctx)
}
`

// expandCreateThenKey is the shape migr diff writes for any new table with a reference (M11-T3).
const expandCreateThenKey = expandPreamble + `
func init() { migr.Register(AddLabels{}) }

// AddLabels creates a table and gives it a foreign key, which is the list a diff writes for one.
type AddLabels struct{}

// Up creates the table first, so the key's own rebuild reads a catalog that holds it.
func (m AddLabels) Up(ctx context.Context, s *migr.Schema) error {
	if err := s.CreateTable("labels").
		Column("id", migr.Int64()).
		Column("crate", migr.String(64)).
		PrimaryKey("id").
		Apply(ctx); err != nil {
		return err
	}

	return s.Table("labels").
		AddForeignKey("fk_labels_crate", []string{"crate"}, "crates", []string{"tag"}).
		Apply(ctx)
}
`

// expandProject writes a Go project over its own database, with the migrations a case chooses.
func expandProject(t *testing.T, rest ...string) (config, database string) {
	t.Helper()

	dir := t.TempDir()
	config = filepath.Join(dir, "migr.yaml")
	database = filepath.Join(dir, "app.db")

	body := fmt.Sprintf(`version: 1
migrations:
  dir: ./migrations
  language: go
databases:
  default:
    driver: sqlite
    dsn: %s
history:
  table: %s
lock:
  timeout: 5s
`, database, expandHistoryTable)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writeSQLiteGoModule(t, dir)

	migrations := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migrations, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	write(t, filepath.Join(migrations, "20261001T120000_add_crates.go"), expandBase)

	for i, body := range rest {
		write(t, filepath.Join(migrations,
			fmt.Sprintf("20261001T1201%02d_change_crates_%d.go", i, i)), body)
	}

	return config, database
}

func TestAMigrationHoldingTwoEmulatedOperationsAppliesBoth(t *testing.T) {
	config, database := expandProject(t, expandBothInOne)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	checkTightenedAndKeyed(t, database)

	t.Log("The second rebuild re-renders the whole table from the catalog, so it either reads the")
	t.Log("NOT NULL the first one wrote or it drops it. Measured before this task, with both")
	t.Log("expansions compiled up front, the column came back nullable and the key was over it.")
}

func TestARollbackOfTwoEmulatedMigrationsInOneRunUndoesBoth(t *testing.T) {
	config, database := expandProject(t, expandTightenOnly, expandKeyOnly)

	if err := migr(t, config, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	before := sqliteSnapshotOf(t, database, expandHistoryTable)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	checkTightenedAndKeyed(t, database)

	if err := migr(t, config, "down", "--step", "2"); err != nil {
		t.Fatalf("down --step 2: %v", err)
	}

	after := sqliteDigestOf(t, database, expandHistoryTable)

	if strings.Contains(after, "pk_crates") {
		t.Errorf("the primary key survived a rollback that dropped it:\n%s", after)
	}

	if strings.Contains(after, `"tag" varchar(64) NOT NULL`) {
		t.Errorf("the rollback left the nullability the first migration tightened:\n%s", after)
	}

	if restored := sqliteSnapshotOf(t, database, expandHistoryTable); !restored.Equal(*before) {
		t.Errorf("the rollback did not restore the schema:\n%s", after)
	}

	t.Log("Both inverses are compiled before either runs (ADR 0022, which irreversibility needs),")
	t.Log("so this is the same defect across migrations rather than within one: the older inverse's")
	t.Log("rebuild re-created what the newer one had just removed, and the key survived its own")
	t.Log("rollback. What changed is when each one is rendered, not when it is decided.")
}

// checkTightenedAndKeyed holds the table to carrying both emulated operations' results at once.
func checkTightenedAndKeyed(t *testing.T, database string) {
	t.Helper()

	rendered := sqliteDigestOf(t, database, expandHistoryTable)

	for _, want := range []string{`"tag" varchar(64) NOT NULL`, `PRIMARY KEY ("tag")`} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the schema does not hold %q, so one rebuild undid the other:\n%s",
				want, rendered)
		}
	}
}

func TestATableCreatedAndThenKeyedInOneRunCompilesAtAll(t *testing.T) {
	config, database := expandProject(t, expandBothInOne, expandCreateThenKey)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if got := sqliteDigestOf(t, database, expandHistoryTable); !strings.Contains(got,
		`FOREIGN KEY ("crate") REFERENCES "crates" ("tag")`) {
		t.Fatalf("the foreign key is not in the schema:\n%s", got)
	}

	if err := migr(t, config, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	t.Log("This is the list migr diff writes for a new table with a reference (M11-T3, ADR 0070),")
	t.Log("and before this task it could not compile on this engine at all: add_foreign_key is one")
	t.Log("of the seven rebuilds, so Expand read a catalog the create_table ahead of it in the same")
	t.Log("migration had not reached -- \"this database has no such table\". An apply emits one")
	t.Log("migration at a time (ADR 0021), so only one migration holding both reaches it, and that")
	t.Log("is exactly the file migr diff writes. M11-T5 was unusable on this engine until it landed.")
}

func TestAPlanMarksAnEmulatedOperationItCannotCompileExactly(t *testing.T) {
	config, _ := expandProject(t, expandBothInOne)

	if err := migr(t, config, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	out := migrOut(t, config, "plan")

	if !strings.Contains(out, `"tag" varchar(64) NOT NULL`) {
		t.Errorf("plan does not print the first operation's own statements:\n%s", out)
	}

	if strings.Contains(out, `PRIMARY KEY ("tag")`) {
		t.Errorf("plan printed statements for an operation it cannot compile exactly:\n%s", out)
	}

	for _, want := range []string{
		"→ compiled against the database when it runs",
		"only the first operation of the first migration can be compiled exactly",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan does not say %q:\n%s", want, out)
		}
	}

	t.Log("The first operation is exact, because a preview applies nothing and nothing ahead of it")
	t.Log("would have changed the catalog it reads. The second is not, and printing what it")
	t.Log("compiles to now would print SQL that is not the SQL that runs -- which is the rule migr")
	t.Log("plan already follows for a bound argument it counts rather than splices.")
}
