package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/cli"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/sqlite"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// M11-T5's own done-when, measured rather than argued: the command compares the live database to
// the desired schema and writes a migration file in the configured language. Nothing short of a
// real database and a real toolchain can say it did -- the written file has to compile, and running
// it has to leave the database holding the schema the migrations describe.
//
// Untagged for the reason this engine's other suites are: a t.TempDir() and the Go toolchain are
// its whole requirement (ADR 0050, M6-T6). The project is Go rather than .sql because every .sql
// migration reaches the database as ir.Raw, so such a project has no desired schema to fold at all.

// diffHistoryTable keeps this suite's runs out of every other table here.
const diffHistoryTable = "migr_e2e_sqlite_diff"

// diffPreamble is the module header every migration in this suite starts from.
const diffPreamble = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)
`

// diffBase is the one migration this suite's project holds before anything is generated.
const diffBase = diffPreamble + `
func init() { migr.Register(AddPeople{}) }

// AddPeople creates the table every case here drifts away from.
type AddPeople struct{}

// Up creates the table, its index and its unique constraint.
func (m AddPeople) Up(ctx context.Context, s *migr.Schema) error {
	if err := s.CreateTable("people").
		Column("id", migr.Int64()).
		Column("email", migr.String(255)).
		Column("note", migr.Text()).Null().Default(migr.Literal("")).
		PrimaryKey("id").
		Apply(ctx); err != nil {
		return err
	}

	return s.Table("people").
		AddIndex("idx_people_email", "email").
		AddUnique("uq_people_email", "email").
		Apply(ctx)
}

// Down drops the table, recording what it held so the drop inverts exactly.
func (m AddPeople) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").
		DropUnique("uq_people_email").Was(migr.ColumnsDef{Columns: []string{"email"}}).
		DropIndex("idx_people_email").Was(migr.ColumnsDef{Columns: []string{"email"}}).
		Drop().Was(migr.TableDef{
		Columns: []migr.Column{
			{Name: "id", Type: migr.Int64()},
			{Name: "email", Type: migr.String(255)},
			{Name: "note", Type: migr.Text(), Nullable: true, Default: migr.Literal("")},
		},
		PrimaryKey: []string{"id"},
	}).Apply(ctx)
}
`

// diffProject writes a Go project whose one migration creates the table the drift is applied to.
func diffProject(t *testing.T) (config, database string) {
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
validation:
  allow_destructive: [drop_column, drop_table]
`, database, diffHistoryTable)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writeSQLiteGoModule(t, dir)

	migrations := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migrations, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	write(t, filepath.Join(migrations, "20260930T120000_add_people.go"), diffBase)

	return config, database
}

func TestADiffOverADatabaseTheMigrationsDescribeProposesNothing(t *testing.T) {
	config, _ := diffProject(t)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	out := migrOut(t, config, "diff")

	if !strings.Contains(out, "nothing to write") {
		t.Fatalf("a diff over an up-to-date database did not come back empty:\n%s", out)
	}

	if generated := diffGenerated(t, config); len(generated) != 0 {
		t.Errorf("it wrote %v over a database that already matches", generated)
	}

	t.Log("This is M11-T6's done-when reached from the other side: apply everything, introspect,")
	t.Log("diff against what the migrations describe, and get nothing. Every one of this fixture's")
	t.Log("column types is one SQLite resolves exactly or guesses correctly -- int64 and text are")
	t.Log("ambiguous there and both guess right, where an int32 column would be refused by design")
	t.Log("(ADR 0070, C1), which is what M11-T6's own note predicts.")
}

func TestADiffWritesAMigrationThatCompilesAndClosesTheGap(t *testing.T) {
	config, database := diffProject(t)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	want := sqliteSnapshotOf(t, database, diffHistoryTable)

	driftAway(t, database)

	if drifted := sqliteSnapshotOf(t, database, diffHistoryTable); drifted.Equal(*want) {
		t.Fatal("the drift this case applies changed nothing")
	}

	out := migrOut(t, config, "diff", "close_the_gap")

	generated := diffGenerated(t, config)
	if len(generated) != 1 {
		t.Fatalf("it wrote %v, want one migration:\n%s", generated, out)
	}

	t.Logf("what it wrote:\n%s", read(t, generated[0]))

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("the migration it wrote does not apply: %v\n\n%s", err, read(t, generated[0]))
	}

	if closed := sqliteSnapshotOf(t, database, diffHistoryTable); !closed.Equal(*want) {
		t.Errorf("the generated migration did not close the gap:\n%s",
			sqliteDigestOf(t, database, diffHistoryTable))
	}

	t.Log("The written file went through the Go toolchain to get here: migr up builds the project's")
	t.Log("migrations into a runner, so a rendered chain that did not compile would fail above.")
}

func TestAGeneratedMigrationIsReversibleByDerivationAndPassesValidate(t *testing.T) {
	config, database := diffProject(t)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	before := sqliteSnapshotOf(t, database, diffHistoryTable)

	driftAway(t, database)

	if err := migr(t, config, "diff", "close_the_gap"); err != nil {
		t.Fatalf("diff: %v", err)
	}

	if err := migr(t, config, "validate"); err != nil {
		t.Fatalf("the migration it wrote does not pass this project's own validate: %v\n\n%s",
			err, read(t, diffGenerated(t, config)[0]))
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if err := migr(t, config, "down", "--step", "1"); err != nil {
		t.Fatalf("the generated migration has no inverse that runs: %v", err)
	}

	if rolled := sqliteSnapshotOf(t, database, diffHistoryTable); rolled.Equal(*before) {
		t.Error("rolling the generated migration back landed on the schema it was meant to reach")
	}

	t.Log("The generated migration authors no Down, so migr derives one from what Up emitted --")
	t.Log("which works only because every operation the differ emits carries a complete from")
	t.Log("payload and the renderer writes every one of them out (ADR 0070, ADR 0073 D). A")
	t.Log("renderer that dropped the payloads would fail validate's own reverse check here.")
}

func TestADiffRefusesADatabaseWithMigrationsStillToRun(t *testing.T) {
	cases := []struct {
		name  string
		stage func(*testing.T) (string, string)
		says  string
	}{
		{"nothing has run at all", diffProject, "no migration history table at all"},
		{"one migration has run and another has not", pendingDiffProject, "1 pending"},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			config, _ := one.stage(t)

			reported := migrFailure(t, config, "diff")

			for _, want := range []string{"not the one the migrations describe",
				"Run `migr up` first", one.says} {
				if !strings.Contains(reported, want) {
					t.Errorf("the refusal does not say %q:\n%s", want, reported)
				}
			}

			if got := cli.ExitCode(migr(t, config, "diff")); got != cli.ExitRuntime {
				t.Errorf("migr diff exited %d, want %d", got, cli.ExitRuntime)
				t.Log("ADR 0029's rule: the same command line succeeds once migr up has run.")
			}

			if generated := diffGenerated(t, config); len(generated) != 0 {
				t.Errorf("a refused diff wrote %v", generated)
			}
		})
	}

	t.Log("A desired schema folded from every migration is the schema the database should have")
	t.Log("once they have all run, so a diff over a half-migrated project would propose")
	t.Log("everything the pending ones do and then duplicate them.")
}

// diffSecond is the second migration pendingDiffProject leaves unapplied.
const diffSecond = diffPreamble + `
func init() { migr.Register(AddTeams{}) }

// AddTeams is the migration pendingDiffProject never runs.
type AddTeams struct{}

// Up creates a second table, which the live schema therefore does not hold.
func (m AddTeams) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("teams").
		Column("id", migr.Int64()).
		PrimaryKey("id").
		Apply(ctx)
}
`

// pendingDiffProject applies the first migration and leaves a second one on disk, unrun.
func pendingDiffProject(t *testing.T) (config, database string) {
	t.Helper()

	config, database = diffProject(t)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	write(t, filepath.Join(filepath.Dir(config), "migrations",
		"20260930T120100_add_teams.go"), diffSecond)

	return config, database
}

// The drift is applied through the driver's own compiler rather than by composing SQL, which is
// invariant #3: a test above a driver package may not write SQL for a database. An operator's hand
// is what this stands in for, so what it does is exactly what no migration recorded.

// driftAway changes the database behind migr's back, which is the gap a diff exists to close.
func driftAway(t *testing.T, database string) {
	t.Helper()

	d, err := sqlite.New(driver.Options{HistoryTable: diffHistoryTable})
	if err != nil {
		t.Fatalf("building the driver: %v", err)
	}

	ctx := context.WithoutCancel(t.Context())
	conn := sqliteOpened(t, database, diffHistoryTable)

	for _, op := range driftOperations() {
		apply(ctx, t, d, conn, op)
	}
}

// driftOperations are what an operator did by hand that no migration recorded.
func driftOperations() ir.Operations {
	return ir.Operations{
		ir.DropIndex{Table: "people", Name: "idx_people_email"},
		ir.AddColumn{Table: "people", Column: ir.Column{Name: "stray",
			Type: mustLogical("string(16)"), Nullable: true}},
		ir.DropColumn{Table: "people", Column: "note"},
	}
}

// apply compiles one operation and runs it, through the session where this driver needs one.
func apply(ctx context.Context, t *testing.T, d driver.Driver, conn driver.Conn, op ir.Operation) {
	t.Helper()

	stmts, err := compiled(ctx, d, conn, op)
	if err != nil {
		t.Fatalf("compiling the drift's %s: %v", op.Kind(), err)
	}

	for _, stmt := range stmts {
		if err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("applying the drift's %s: %v", op.Kind(), err)
		}
	}
}

// compiled renders one operation, through the session for the ones this engine emulates.
func compiled(ctx context.Context, d driver.Driver, conn driver.Conn,
	op ir.Operation) ([]driver.Statement, error) {
	stmts, err := d.Compile(op)
	if err == nil {
		return stmts, nil
	}

	expander, emulates := conn.(driver.Expander)
	if !emulates {
		return nil, err
	}

	return expander.Expand(ctx, op)
}

// mustLogical parses one of this suite's logical types, which are all written out here.
func mustLogical(spelling string) schema.Type {
	parsed, err := schema.Parse(spelling)
	if err != nil {
		panic(err)
	}

	return parsed
}

// diffGenerated names every migration in the project beyond the one its fixture staged.
func diffGenerated(t *testing.T, config string) []string {
	t.Helper()

	dir := filepath.Join(filepath.Dir(config), "migrations")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the migrations directory: %v", err)
	}

	var written []string

	for _, entry := range entries {
		if !strings.Contains(entry.Name(), "add_people") &&
			!strings.Contains(entry.Name(), "add_teams") {
			written = append(written, filepath.Join(dir, entry.Name()))
		}
	}

	return written
}

// read returns what one file holds, which is what a failure about a generated migration prints.
func read(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	return string(body)
}
