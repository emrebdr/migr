package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/cli"
	"github.com/emrebdr/migr/internal/schema"
)

// The two gaps M3-T7 and M9-T7 pinned, closed end to end against a real database.
//
// Both were measured as gaps and left asserted as gaps: a reversed drop_column does not bring back
// the index the server dropped with the column, and a reversed add_primary_key does not undo the
// NOT NULL the key implied. Neither is reachable from rollback time -- by then the index is gone
// and nothing ever recorded the nullability -- so the refusal is before the operation runs, which
// is what engine.Unrestorable's implied half is.
//
// A .sql project cannot reach either: every statement in one is ir.Raw, which that gate never
// judges. So these are Go migrations, and they are untagged for the reason the rest of this
// engine's suites are -- a t.TempDir() and the Go toolchain are its whole requirement (ADR 0050,
// M6-T6).
//
// Each case is written twice: once as the migration an author would reach for, which is refused,
// and once with the remedy the refusal names, which applies and then round-trips. The second half
// is the point. A refusal nobody can act on is one they would work around.

// restorableHistoryTable keeps this suite's runs out of every other table here.
const restorableHistoryTable = "migr_e2e_sqlite_restorable"

// derivedPreamble is the module and the first migration every case in this suite starts from.
const derivedPreamble = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)
`

// baseMigration creates the table every case here changes, with an index and a nullable column.
const baseMigration = derivedPreamble + `
func init() { migr.Register(AddPeople{}) }

// AddPeople creates the table, its index, and the nullable column a key is added over.
type AddPeople struct{}

// Up creates the table and then indexes it, since a new table states only its key inline.
func (m AddPeople) Up(ctx context.Context, s *migr.Schema) error {
	if err := s.CreateTable("people").
		Column("id", migr.Int64()).
		Column("email", migr.String(255)).
		Column("tag", migr.String(64)).Null().
		Apply(ctx); err != nil {
		return err
	}

	return s.Table("people").
		AddIndex("idx_people_email", "email").
		Apply(ctx)
}

// Down drops the table, recording what it held so the drop inverts exactly.
func (m AddPeople) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").
		DropIndex("idx_people_email").Was(migr.Columns("email")).
		Drop().Was(migr.TableDef{
		Columns: []migr.Column{
			{Name: "id", Type: migr.Int64()},
			{Name: "email", Type: migr.String(255)},
			{Name: "tag", Type: migr.String(64), Nullable: true},
		},
	}).Apply(ctx)
}
`

// droppingAnIndexedColumn is the migration an author reaches for, and the one that is refused.
const droppingAnIndexedColumn = derivedPreamble + `
func init() { migr.Register(DropEmail{}) }

// DropEmail drops a column the live schema shows an index covers.
type DropEmail struct{}

// Up drops the column, recording what it was so migr can derive the inverse.
func (m DropEmail) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").
		DropColumn("email").Was(migr.Column{Name: "email", Type: migr.String(255)}).
		Apply(ctx)
}
`

// droppingTheIndexFirst is the remedy the refusal names, which round-trips on every engine.
const droppingTheIndexFirst = derivedPreamble + `
func init() { migr.Register(DropEmail{}) }

// DropEmail drops the index explicitly and then the column it covered.
type DropEmail struct{}

// Up drops the index first, which is what makes the column's own drop reversible.
func (m DropEmail) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").
		DropIndex("idx_people_email").Was(migr.Columns("email")).
		DropColumn("email").Was(migr.Column{Name: "email", Type: migr.String(255)}).
		Apply(ctx)
}
`

// keyingANullableColumn is the second case an author reaches for, and the second refusal.
const keyingANullableColumn = derivedPreamble + `
func init() { migr.Register(KeyPeople{}) }

// KeyPeople adds a primary key over a column the live schema says accepts null.
type KeyPeople struct{}

// Up adds the key, which every engine implies a NOT NULL with and no payload records.
func (m KeyPeople) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").
		AddPrimaryKey("pk_people", "tag").
		Apply(ctx)
}
`

// tighteningFirst is the remedy for the second case: the implied change, made explicit.
//
// A migration of its own rather than two operations in one, because this engine emulates both and
// expands each from the catalog as it was before any of them ran, so the second rebuild re-renders
// what the first changed. That is also why the rollback below is stepped one migration at a time:
// the same thing happens across the migrations of one run, since every inverse in a rollback is
// compiled before any of it runs (ADR 0022). TASKS.md records it as a finding of its own; it is
// older than this suite and not what this suite is about.
const tighteningFirst = derivedPreamble + `
func init() { migr.Register(TightenTag{}) }

// TightenTag states the nullability change the key would otherwise imply, as an operation.
type TightenTag struct{}

// Up makes the column NOT NULL, which this migration's inverse can undo because it is recorded.
func (m TightenTag) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").
		AlterColumn("tag").NotNull().WasNullable(true).
		Apply(ctx)
}
`

// restorableProject writes a Go project whose migrations after the first a case chooses.
func restorableProject(t *testing.T, rest ...string) (config, database string) {
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
  allow_destructive: [drop_column]
`, database, restorableHistoryTable)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writeSQLiteGoModule(t, dir)

	migrations := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migrations, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	write(t, filepath.Join(migrations, "20260930T120000_add_people.go"), baseMigration)

	for i, body := range rest {
		write(t, filepath.Join(migrations,
			fmt.Sprintf("20260930T1201%02d_change_people_%d.go", i, i)), body)
	}

	return config, database
}

// write stages one file of a project, failing the test rather than returning a reason.
func write(t *testing.T, path, body string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("staging %s: %v", path, err)
	}
}

func TestARunIsRefusedOverWhatOnlyTheLiveSchemaRevealsAndTheRemedyRoundTrips(t *testing.T) {
	cases := []struct {
		name    string
		refused []string
		remedy  []string
		says    string
	}{
		{
			name:    "an index the dropped column carries",
			refused: []string{droppingAnIndexedColumn},
			remedy:  []string{droppingTheIndexFirst},
			says:    `index "idx_people_email"`,
		},
		{
			name:    "the nullability a primary key implies",
			refused: []string{keyingANullableColumn},
			remedy:  []string{tighteningFirst, keyingANullableColumn},
			says:    "makes it NOT NULL",
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			checkRefused(t, one.says, one.refused...)
			checkRemedyRoundTrips(t, one.remedy...)
		})
	}

	t.Log("M3-T7 and M9-T7 left both of these asserted as gaps, and both of those assertions are")
	t.Log("about what a server does rather than about what migr does -- the server still drops the")
	t.Log("index with the column, and still keeps the NOT NULL. What changed is that migr no longer")
	t.Log("lets a migration reach either silently.")
}

// checkRefused holds the migration an author reaches for to being refused before anything runs.
func checkRefused(t *testing.T, says string, rest ...string) {
	t.Helper()

	config, database := restorableProject(t, rest...)

	if err := migr(t, config, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	before := sqliteDigestOf(t, database, restorableHistoryTable)

	reported := migrFailure(t, config, "up")

	for _, want := range []string{says, "explicit"} {
		if !strings.Contains(reported, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, reported)
		}
	}

	if got := cli.ExitCode(migr(t, config, "up")); got != cli.ExitRuntime {
		t.Errorf("migr up exited %d, want %d", got, cli.ExitRuntime)
		t.Log("ADR 0029's rule: the same command line succeeds against a database that does not")
		t.Log("contradict the migration, so the world is what is not ready rather than the argument.")
	}

	if after := sqliteDigestOf(t, database, restorableHistoryTable); after != before {
		t.Errorf("a refused migration changed the schema:\n%s", after)
	}

	if runs := countRuns(migrOut(t, config, "history")); len(runs) != 1 {
		t.Errorf("the history holds %v, want the one migration that did apply", runs)
	}
}

// The comparison is the neutral snapshot rather than the catalog text, and the difference is the
// point: a reversed drop_column appends the column rather than putting it back where it was, which
// no v1 operation can control -- MySQL's AFTER has no field in the IR (M9-T7). schema.Snapshot
// holds what a schema has and never the order it has it in, for exactly that reason (ADR 0065), so
// it compares equal where the recorded DDL does not.

// checkRemedyRoundTrips holds the remedy the refusal names to applying and then undoing itself.
func checkRemedyRoundTrips(t *testing.T, rest ...string) {
	t.Helper()

	config, database := restorableProject(t, rest...)

	if err := migr(t, config, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	before := sqliteSnapshotOf(t, database, restorableHistoryTable)

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("the remedy the refusal names was itself refused: %v", err)
	}

	if after := sqliteSnapshotOf(t, database, restorableHistoryTable); after.Equal(*before) {
		t.Fatalf("the remedy changed nothing:\n%s", sqliteDigestOf(t, database,
			restorableHistoryTable))
	}

	for range rest {
		if err := migr(t, config, "down", "--step", "1"); err != nil {
			t.Fatalf("down --step 1: %v", err)
		}
	}

	if restored := sqliteSnapshotOf(t, database, restorableHistoryTable); !restored.Equal(*before) {
		t.Errorf("the rollback did not restore the schema:\n%s", sqliteDigestOf(t, database,
			restorableHistoryTable))
	}
}

// sqliteSnapshotOf reads one database back as the neutral snapshot, which is what comparison is on.
func sqliteSnapshotOf(t *testing.T, database, table string) *schema.Snapshot {
	t.Helper()

	read, err := sqliteOpened(t, database, table).Introspect(context.WithoutCancel(t.Context()))
	if err != nil {
		t.Fatalf("introspecting %s: %v", database, err)
	}

	return read
}
