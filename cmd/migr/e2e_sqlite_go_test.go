package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver/sqlite"
)

// A table rebuild, end to end, which only a real operation can reach.
//
// Every .sql migration compiles to ir.Raw, so the suite beside this one can never produce an
// emulated operation however it is written. A Go migration emits real IR, so add_check arrives as
// ir.AddCheck, the compiler answers driver.ErrNeedsSession, and the session expands it against the
// live database (ADR 0053) into the statements ADR 0054 describes.
//
// Untagged like the rest of this engine's tests: the Go toolchain is present by definition when
// go test is what is running (M6-T6), so building a runner needs nothing make check lacks.
//
// One thing a rebuild cannot restore is the wording of the table it replaced: it re-renders the
// CREATE TABLE from its own splitter, so the columns, the key and the constraints come back and the
// recorded text is the rebuild's rather than the author's. checkRebuildRestored exempts exactly that
// one line, which is M8-T4's rule (checkOther, in the driver's own suite) applied end to end, and
// TestARebuiltTableIsStableAcrossASecondRoundTrip is what says the wording then stops moving.

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

	checkRebuildRestored(t, before, after)
}

// checkRebuildRestored holds a rollback to restoring everything but the rebuilt table's own wording.
func checkRebuildRestored(t *testing.T, before, after string) {
	t.Helper()

	was := make(map[string]bool)
	for _, line := range strings.Split(before, "\n") {
		was[line] = true
	}

	for _, line := range strings.Split(after, "\n") {
		if was[line] || strings.HasPrefix(line, "table rebuilt_accounts ") {
			continue
		}

		t.Errorf("the round trip changed an object the rebuild was not asked about:\n  %s", line)
	}

	for _, want := range []string{`index idx_rebuilt_accounts_email`, `"id"`, `"email"`,
		`PRIMARY KEY`} {
		if !strings.Contains(after, want) {
			t.Errorf("the rolled-back schema no longer holds %q:\n%s", want, after)
		}
	}
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
		t.Errorf("a second round trip did not land where the first did:\n%s\n---\n%s", settled, got)
	}

	t.Log("The rebuild's own rendering is what a rebuilt table keeps, so the first round trip moves")
	t.Log("the recorded wording once and every one after it is byte-identical. That is the strongest")
	t.Log("form the done-when can take for an emulated operation.")
}
