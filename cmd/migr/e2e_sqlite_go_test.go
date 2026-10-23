package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver/sqlite"
	"github.com/emrebdr/migr/internal/schema"
)

// A table rebuild, end to end, which only a real operation can reach.
//
// Every .sql migration compiles to ir.Raw, so the suite beside this one can never produce an
// emulated operation however it is written. A Go migration emits real IR, so add_check arrives as
// ir.AddCheck, the compiler answers driver.ErrNeedsSession, and the session expands it against the
// live database into the statements of SQLite's table rebuild.
//
// Untagged like the rest of this engine's tests: the Go toolchain is present by definition when
// go test is what is running (M6-T6), so building a runner needs nothing make check lacks.
//
// One thing a rebuild cannot restore is the wording of the table it replaced: it re-renders the
// CREATE TABLE from its own splitter, so the columns, the key and the constraints come back and the
// recorded text is the rebuild's rather than the author's. Until M11-T6 that cost this suite an
// exemption for every line naming the rebuilt table -- which left the one table the emulation
// touched as the one thing the round trip did not check. What is compared now is the neutral
// snapshot, where a re-wording is invisible, so the rebuilt table is held to the same equality as
// everything around it and the wording claim lives in the driver's own suite instead.

// sqliteGoHistoryTable keeps this suite's runs out of every other table here.
const sqliteGoHistoryTable = "migr_e2e_sqlite_go"

// sqliteGoModule is the module the migrations are built inside, pointing at this repository's SDK.
const sqliteGoModule = `module example.com/e2esqlite

go 1.26

require github.com/emrebdr/migr/sdk/go v0.0.0

replace github.com/emrebdr/migr/sdk/go => `

// sqliteGoMigrations create a table with an index and then constrain it, which is the rebuild.
var sqliteGoMigrations = []struct{ name, body string }{
	{
		name: "20260929T140000_add_rebuilt_accounts.go",
		body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddAccounts{}) }

// AddAccounts creates the table the rebuild below is measured over.
type AddAccounts struct{}

// Up creates the table and then indexes it, since a new table states only its key inline.
func (m AddAccounts) Up(ctx context.Context, s *migr.Schema) error {
	if err := s.CreateTable("rebuilt_accounts").
		Column("id", migr.Int64()).
		Column("email", migr.String(255)).
		PrimaryKey("id").
		Apply(ctx); err != nil {
		return err
	}

	return s.Table("rebuilt_accounts").
		AddIndex("idx_rebuilt_accounts_email", "email").
		Apply(ctx)
}

// Down drops both, recording what the table held so the drop inverts exactly.
func (m AddAccounts) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("rebuilt_accounts").
		DropIndex("idx_rebuilt_accounts_email").Was(migr.Columns("email")).
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
		name: "20260929T140100_check_rebuilt_email.go",
		body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(CheckEmail{}) }

// CheckEmail adds a check constraint, which SQLite's ALTER TABLE cannot do at all.
type CheckEmail struct{}

// Up adds the constraint, which this engine performs by rebuilding the table.
func (m CheckEmail) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("rebuilt_accounts").
		AddCheck("ck_rebuilt_email", "length(email) >= 3").
		Apply(ctx)
}

// Down drops it, naming what it was so the drop inverts exactly.
func (m CheckEmail) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("rebuilt_accounts").
		DropCheck("ck_rebuilt_email").Was(migr.CheckDef{Expression: "length(email) >= 3"}).
		Apply(ctx)
}
`,
	},
}

// sqliteGoProject writes a Go project over its own database file, and returns both paths.
func sqliteGoProject(t *testing.T) (config, database string) {
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
`, database, sqliteGoHistoryTable)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writeSQLiteGoModule(t, dir)
	writeSQLiteGoMigrations(t, filepath.Join(dir, "migrations"))

	return config, database
}

// writeSQLiteGoModule writes the go.mod pointing at this repository's SDK, which a user would go get.
func writeSQLiteGoModule(t *testing.T, dir string) {
	t.Helper()

	sdk, err := filepath.Abs(filepath.Join("..", "..", "sdk", "go"))
	if err != nil {
		t.Fatalf("resolving the SDK: %v", err)
	}

	path := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(path, []byte(sqliteGoModule+sdk+"\n"), 0o600); err != nil {
		t.Fatalf("staging the project module: %v", err)
	}
}

// writeSQLiteGoMigrations stages every migration this suite runs, where migr.yaml names them.
func writeSQLiteGoMigrations(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, m := range sqliteGoMigrations {
		if err := os.WriteFile(filepath.Join(dir, m.name), []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}
}

func TestPlanLabelsTheRebuildAndPrintsWhatARealDatabaseWouldRun(t *testing.T) {
	config, database := sqliteGoProject(t)

	if err := migr(t, config, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	out := migrOut(t, config, "plan")

	if !strings.Contains(out, "emulated: "+sqlite.RebuildStrategy) {
		t.Fatalf("plan does not label the rebuild:\n%s", out)
	}

	for _, fragment := range []string{
		"PRAGMA defer_foreign_keys",
		`CREATE TABLE "migr_rebuild_rebuilt_accounts"`,
		`DROP TABLE "rebuilt_accounts"`,
		`CREATE INDEX "idx_rebuilt_accounts_email"`,
		"ck_rebuilt_email",
	} {
		if !strings.Contains(out, fragment) {
			t.Errorf("plan does not print %q, so the rebuild was not expanded:\n%s", fragment, out)
		}
	}

	if got := sqliteDigestOf(t, database, sqliteGoHistoryTable); !strings.Contains(got,
		"rebuilt_accounts") {
		t.Fatalf("the table the plan describes is not there:\n%s", got)
	}

	t.Log("M8-T4 could prove this only through internal/cli's fake, because a preview takes the")
	t.Log("migration lock and that was M8-T5. This is the first plan in the repository showing an")
	t.Log("emulation a real database produced -- and the statements are the rebuild's own, read out")
	t.Log("of sqlite_schema rather than guessed at.")
}

func TestARebuildAppliesAndRollsBackWithoutLosingWhatItDidNotTouch(t *testing.T) {
	config, database := sqliteGoProject(t)

	if err := migr(t, config, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	before := sqliteDigestOf(t, database, sqliteGoHistoryTable)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	rebuilt := sqliteDigestOf(t, database, sqliteGoHistoryTable)
	if !strings.Contains(rebuilt, "ck_rebuilt_email") {
		t.Fatalf("the check is not in the schema after up:\n%s", rebuilt)
	}

	if err := migr(t, config, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	after := sqliteDigestOf(t, database, sqliteGoHistoryTable)

	if strings.Contains(after, "ck_rebuilt_email") {
		t.Errorf("the rollback left the check behind:\n%s", after)
	}

	if after != before {
		t.Errorf("the round trip did not restore the schema:\n%s", schema.Difference(before, after))
	}

	if !strings.Contains(after, "table       rebuilt_accounts") {
		t.Errorf("the comparison does not cover the rebuilt table, so it proves little:\n%s", after)
	}

	t.Log("The rebuilt table is not exempt from that equality any more. It used to be, because the")
	t.Log("comparison was SQLite's own recorded CREATE TABLE and a rebuild re-words it -- which left")
	t.Log("the one table the emulation touched as the one thing the round trip never checked.")
}

func TestARebuiltTableIsStableAcrossASecondRoundTrip(t *testing.T) {
	config, database := sqliteGoProject(t)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("the first up: %v", err)
	}

	if err := migr(t, config, "down"); err != nil {
		t.Fatalf("the first down: %v", err)
	}

	settled := sqliteDigestOf(t, database, sqliteGoHistoryTable)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("the second up: %v", err)
	}

	if err := migr(t, config, "down"); err != nil {
		t.Fatalf("the second down: %v", err)
	}

	if got := sqliteDigestOf(t, database, sqliteGoHistoryTable); got != settled {
		t.Errorf("a second round trip did not land where the first did:\n%s",
			schema.Difference(settled, got))
	}

	t.Log("Two round trips through the CLI land on the same schema, which is what says an emulated")
	t.Log("operation is idempotent across runs rather than merely reversible within one. The claim")
	t.Log("used to be about SQLite's recorded wording settling after the first rebuild -- that is a")
	t.Log("fact about this engine rather than about the stack, so it moved into the driver's own")
	t.Log("suite, where the recorded statement is available and a snapshot's is not.")
}

// notesMigration creates the table the dropping migration below takes a column from.
const notesMigration = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddNotes{}) }

// AddNotes creates a table with a column nothing else names, so dropping it needs no rebuild.
type AddNotes struct{}

// Up creates it.
func (m AddNotes) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("rebuilt_notes").
		Column("id", migr.Int64()).
		Column("body", migr.String(255)).
		PrimaryKey("id").
		Apply(ctx)
}

// Down drops it, recording what it held so the drop inverts exactly.
func (m AddNotes) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("rebuilt_notes").Drop().Was(migr.TableDef{
		Columns: []migr.Column{
			{Name: "id", Type: migr.Int64()},
			{Name: "body", Type: migr.String(255)},
		},
		PrimaryKey: []string{"id"},
	}).Apply(ctx)
}
`

// droppingMigration is a Go migration whose up destroys a column, which the gate refuses by kind.
const droppingMigration = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(DropBody{}) }

// DropBody removes a column, which is destruction migr can read rather than raw SQL it cannot.
type DropBody struct{}

// Up drops it, naming what it was so the drop inverts exactly.
func (m DropBody) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("rebuilt_notes").
		DropColumn("body").Was(migr.Column{Name: "body", Type: migr.String(255)}).
		Apply(ctx)
}

// Down puts it back, which is a rollback and is therefore not gated.
func (m DropBody) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("rebuilt_notes").
		AddColumn("body", migr.String(255)).
		Apply(ctx)
}
`

// stageDropping writes the two migrations a typed drop needs, and permits what the test says.
func stageDropping(t *testing.T, config, allowed string) {
	t.Helper()

	dir := filepath.Join(filepath.Dir(config), "migrations")

	for _, m := range []struct{ name, body string }{
		{"20260929T140200_add_rebuilt_notes.go", notesMigration},
		{"20260929T140300_drop_rebuilt_body.go", droppingMigration},
	} {
		if err := os.WriteFile(filepath.Join(dir, m.name), []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}

	if allowed == "" {
		return
	}

	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatalf("reading migr.yaml: %v", err)
	}

	amended := string(body) + "validation:\n  allow_destructive: [" + allowed + "]\n"
	if err := os.WriteFile(config, []byte(amended), 0o600); err != nil {
		t.Fatalf("staging the allowance: %v", err)
	}
}

func TestATypedDropIsRefusedUntilItsKindIsPermitted(t *testing.T) {
	config, database := sqliteGoProject(t)
	stageDropping(t, config, "")

	out := migrFailure(t, config, "up")

	for _, fragment := range []string{
		"drop_rebuilt_body.go",
		"this migration destroys data",
		"operation 1 (drop_column)",
		`"rebuilt_notes.body"`,
		"adding drop_column to validation.allow_destructive",
	} {
		if !strings.Contains(out, fragment) {
			t.Errorf("the refusal does not carry %q:\n%s", fragment, out)
		}
	}

	if got := sqliteDigestOf(t, database, sqliteGoHistoryTable); !strings.Contains(got,
		"rebuilt_notes.body") {
		t.Errorf("the refused migration changed the schema:\n%s", got)
	}

	if err := migr(t, config, "up", "--allow-destructive"); err != nil {
		t.Fatalf("up --allow-destructive: %v", err)
	}

	if got := sqliteDigestOf(t, database, sqliteGoHistoryTable); strings.Contains(got,
		"rebuilt_notes.body") {
		t.Errorf("the column is still there after a permitted drop:\n%s", got)
	}

	t.Log("A typed operation is destruction migr can read, so the refusal names the column rather")
	t.Log("than a statement -- which is the half a .sql project can never reach, every statement")
	t.Log("in one being a raw operation whose effect nothing here can read.")
}

func TestTheProjectsOwnAllowanceRunsTheSameMigration(t *testing.T) {
	config, _ := sqliteGoProject(t)
	stageDropping(t, config, "drop_column")

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up over a project that permits drop_column: %v", err)
	}

	if err := migr(t, config, "down"); err != nil {
		t.Log("Rolling this back adds the column again, and the earlier migrations' downs drop a")
		t.Log("table -- which nothing permits, because a rollback is not gated.")
		t.Fatalf("down over a project that permits drop_column: %v", err)
	}
}
