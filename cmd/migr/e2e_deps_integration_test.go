//go:build integration

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/schema"
)

// A declared dependency, resolved against a real server, pointing backwards in time.
//
// This is the case timestamps cannot express and ADR 0018 exists for: two branches each add a
// migration, and the one written second carries the earlier timestamp. `migration.Order` has
// overlaid the declarations onto the version order since M4-T2, and until M7-T11 nothing in
// internal/cli passed it any -- `discover` called `Order(found, nil)`, so every declaration both
// SDKs reported was read, carried across the wire, and thrown away.
//
// The proof is the server rather than a digest. The .ts migration at 190000 adds a column to the
// table the .go migration at 190100 creates, so in timestamp order PostgreSQL refuses the first
// statement of the run. And the edge crosses languages: a TypeScript migration declares a Go one,
// which is two sessions over one pinned driver.Conn, each reporting its own half of one graph
// (ADR 0048, ADR 0049).

// depsHistoryTable keeps this suite's runs out of the tables the other e2e suites use, and each
// test keeps its own so the append-only log one leaves behind is never the log the next one reads.
const depsHistoryTable = "migr_e2e_deps_migrations"

// The two migrations this suite applies, whose timestamps and whose declaration disagree.
const (
	// depsDeclarer is the earlier-timestamped migration, which cannot run first.
	depsDeclarer = "20260727T190000_add_e2e_deps_sku.ts"
	// depsDeclared is the later-timestamped migration it declares, which must.
	depsDeclared = "20260727T190100_add_e2e_deps_items.go"
)

// depsMigrations are those two, in the order a directory listing puts them rather than the run's.
var depsMigrations = []struct{ name, body string }{
	{
		name: depsDeclarer,
		body: `import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  dependencies: ["20260727T190100"],
  async up(s) {
    await s.table("e2e_deps_items").addColumn("sku", t.string(32)).null().apply();
  },
  async down(s) {
    await s
      .table("e2e_deps_items")
      .dropColumn("sku")
      .was({ name: "sku", type: t.string(32), nullable: true })
      .apply();
  },
});
`,
	},
	{
		name: depsDeclared,
		body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddDepsItems{}) }

// AddDepsItems creates the table the earlier-timestamped migration adds a column to.
type AddDepsItems struct{}

// Up creates the table, which nothing before this migration could have added a column to.
func (m AddDepsItems) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("e2e_deps_items").
		Column("id", migr.Int64()).
		PrimaryKey("id").
		Apply(ctx)
}

// Down drops it, recording what it held so the drop inverts exactly.
func (m AddDepsItems) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("e2e_deps_items").Drop().Was(migr.TableDef{
		Columns:    []migr.Column{{Name: "id", Type: migr.Int64()}},
		PrimaryKey: []string{"id"},
	}).Apply(ctx)
}
`,
	},
}

// depsProject writes one project whose two languages are ordered by a declaration, not by time.
func depsProject(t *testing.T, table string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "migr.yaml")

	body := fmt.Sprintf(`version: 1
migrations:
  dir: ./migrations
  language: %s
databases:
  default:
    driver: postgres
    dsn: %s
history:
  table: %s
`, config.LanguageGo, dsn(), table)

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writeGoModule(t, dir)
	writePackage(t, dir)
	writeDepsMigrations(t, filepath.Join(dir, "migrations"))

	return path
}

// writeDepsMigrations stages both migrations in the directory migr.yaml names.
func writeDepsMigrations(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, m := range depsMigrations {
		if err := os.WriteFile(filepath.Join(dir, m.name), []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}
}

func TestADeclaredDependencyAppliesBeforeTheMigrationThatDeclaresIt(t *testing.T) {
	path := depsProject(t, depsHistoryTable+"_round_trip")

	if err := migr(t, path, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	before := digest(t, depsHistoryTable+"_round_trip")

	t.Cleanup(func() {
		if err := migr(t, path, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	planned := migrOut(t, path, "plan")
	if at, declarer := strings.Index(planned, depsDeclared),
		strings.Index(planned, depsDeclarer); at < 0 || at > declarer {
		t.Errorf("plan printed the declaring migration first:\n%s", planned)
	}

	if err := migr(t, path, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	applied := digest(t, depsHistoryTable+"_round_trip")
	if !strings.Contains(applied, "e2e_deps_items.sku string(32)") {
		t.Errorf("the schema does not hold the declared column after up:\n%s", applied)
	}

	runs := lastBatch(recorded(t, depsHistoryTable+"_round_trip"))
	if len(runs) != len(depsMigrations) {
		t.Fatalf("the newest batch holds %d rows, want %d", len(runs), len(depsMigrations))
	}

	for i, want := range []string{"20260727T190100", "20260727T190000"} {
		if runs[i].Version != want || runs[i].Ordinal != int32(i+1) {
			t.Errorf("ordinal %d is %s, want %s", runs[i].Ordinal, runs[i].Version, want)
			t.Log("The declared migration ran first though its timestamp is later, which is what a")
			t.Log("merged branch looks like (ADR 0018). In timestamp order the server would have")
			t.Log("refused the first statement, so this fails on PostgreSQL rather than on a digest.")
		}
	}

	if err := migr(t, path, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	if got := digest(t, depsHistoryTable+"_round_trip"); got != before {
		t.Errorf("the schema did not return to where it started:\n%s",
			schema.Difference(before, got))
	}

	t.Log("A rollback undoes by (batch, ordinal) descending rather than by version, so it gave the")
	t.Log("column back before the table it was on -- which is the half of ADR 0022 a declared edge")
	t.Log("pointing backwards is what makes observable.")
}

func TestStatusPrintsVersionOrderOverADirectoryARuntimeOrders(t *testing.T) {
	table := depsHistoryTable + "_status"
	path := depsProject(t, table)

	if err := migr(t, path, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, path, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, path, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	out := migrOut(t, path, "status")

	if at, declared := strings.Index(out, depsDeclarer), strings.Index(out, depsDeclared); at < 0 ||
		at > declared {
		t.Errorf("status did not print version order:\n%s", out)
	}

	if !strings.Contains(out, "version order") {
		t.Errorf("status claimed an order it started no runtime to read:\n%s", out)
		t.Log("The run applied these in the opposite order and the history says so. A report starts")
		t.Log("no runtime by design (ADR 0027), so it names its basis and points at migr plan --")
		t.Log("which is what keeps two commands from printing two orders and calling both the run's.")
	}

	for _, m := range depsMigrations {
		if got := stateOf(t, out, m.name); got != "applied" {
			t.Errorf("status reports %s as %q, want applied:\n%s", m.name, got, out)
		}
	}
}
