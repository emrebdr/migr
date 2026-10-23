//go:build integration

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/schema"
)

// One directory, two languages, one `migr up`.
//
// M7-T8 proved each language alone against a real server and refused a directory holding both,
// because before that refusal the run applied every file its one runtime recognized and failed on
// the first it did not -- half applying the project. This is the other side of that: each file is
// routed by its own extension, so two runtime sessions are alive over one pinned
// driver.Conn, and the four migrations interleave by timestamp rather than by language.
//
// Two things here cannot be seen in a single-language suite. The order is the whole point -- the
// .ts migration at 180100 adds a column to the table the .go migration at 180000 created, and the
// .go migration at 180200 indexes it, so a run that grouped by language rather than by version
// would fail on the server rather than produce the wrong digest. And the history row's runtime is
// per migration, which engine.Options.Runtime being one string per run used to make impossible.

// mixedHistoryTable keeps this suite's runs out of the tables the other e2e suites use, and each
// test keeps its own so the append-only log one leaves behind is never the log the next one reads.
const mixedHistoryTable = "migr_e2e_mixed_migrations"

// mixedMigrations are the four this suite applies, alternating language and depending on order.
var mixedMigrations = []struct{ name, body string }{
	{
		name: "20260727T180000_add_e2e_mixed_accounts.go",
		body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddAccounts{}) }

// AddAccounts creates the table every migration after this one changes.
type AddAccounts struct{}

// Up creates the table the TypeScript migration after it adds a column to.
func (m AddAccounts) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("e2e_mixed_accounts").
		Column("id", migr.Int64()).
		Column("email", migr.String(255)).
		PrimaryKey("id").
		Apply(ctx)
}

// Down drops it, recording what it held so the drop inverts exactly.
func (m AddAccounts) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("e2e_mixed_accounts").Drop().Was(migr.TableDef{
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
		name: "20260727T180100_add_e2e_mixed_tier.ts",
		body: `import { defineMigration, literal, t } from "@migr/sdk";

enum Tier {
  Free = "free",
}

export default defineMigration({
  async up(s) {
    await s
      .table("e2e_mixed_accounts")
      .addColumn("tier", t.string(16))
      .default(literal(Tier.Free))
      .apply();
  },
  async down(s) {
    await s
      .table("e2e_mixed_accounts")
      .dropColumn("tier")
      .was({ name: "tier", type: t.string(16), nullable: false, default: literal(Tier.Free) })
      .apply();
  },
});
`,
	},
	{
		name: "20260727T180200_add_e2e_mixed_index.go",
		body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddIndex{}) }

// AddIndex indexes a column the migration before it created, so the order is load-bearing.
type AddIndex struct{}

// Up indexes the email column the first migration created.
func (m AddIndex) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("e2e_mixed_accounts").
		AddIndex("idx_e2e_mixed_accounts_email", "email").
		Apply(ctx)
}

// Down drops the index, recording which columns it covered.
func (m AddIndex) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("e2e_mixed_accounts").
		DropIndex("idx_e2e_mixed_accounts_email").Was(migr.Columns("email")).
		Apply(ctx)
}
`,
	},
	{
		name: "20260727T180300_add_e2e_mixed_verified.ts",
		body: `import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s.table("e2e_mixed_accounts").addColumn("verified", t.bool()).null().apply();
  },
});
`,
	},
}

// mixedLanguages is what each migration is written in, which is what its own history row records.
var mixedLanguages = map[string]config.Language{
	"20260727T180000": config.LanguageGo,
	"20260727T180100": config.LanguageTypeScript,
	"20260727T180200": config.LanguageGo,
	"20260727T180300": config.LanguageTypeScript,
}

// mixedProject writes one project holding both languages, and returns the path of its migr.yaml.
func mixedProject(t *testing.T, table string) string {
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
	writeMixedMigrations(t, filepath.Join(dir, "migrations"))

	return path
}

// writeMixedMigrations stages both languages side by side, which is what this suite is about.
func writeMixedMigrations(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, m := range mixedMigrations {
		if err := os.WriteFile(filepath.Join(dir, m.name), []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}
}

func TestOneDirectoryOfTwoLanguagesAppliesAndRollsBackAgainstPostgreSQL(t *testing.T) {
	path := mixedProject(t, mixedHistoryTable+"_round_trip")

	if err := migr(t, path, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	before := digest(t, mixedHistoryTable+"_round_trip")

	t.Cleanup(func() {
		if err := migr(t, path, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, path, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	applied := digest(t, mixedHistoryTable+"_round_trip")

	for _, want := range []string{
		"e2e_mixed_accounts.tier string(16)",
		"e2e_mixed_accounts.verified bool",
		"idx_e2e_mixed_accounts_email",
	} {
		if !strings.Contains(applied, want) {
			t.Errorf("the schema does not hold %q after up:\n%s", want, applied)
		}
	}

	t.Log("The .ts migration at 180100 changed the table the .go one at 180000 created, and the .go")
	t.Log("one at 180200 indexed a column of it. A run that grouped by language rather than by")
	t.Log("version would have failed on the server rather than produced a different digest.")

	if err := migr(t, path, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	if got := digest(t, mixedHistoryTable+"_round_trip"); got != before {
		t.Errorf("the schema did not return to where it started:\n%s",
			schema.Difference(before, got))
	}
}

func TestEachHistoryRowRecordsTheLanguageItsOwnMigrationIsWrittenIn(t *testing.T) {
	path := mixedProject(t, mixedHistoryTable+"_rows")

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

	runs := lastBatch(recorded(t, mixedHistoryTable+"_rows"))
	if len(runs) != len(mixedMigrations) {
		t.Fatalf("the newest batch holds %d rows, want %d", len(runs), len(mixedMigrations))
	}

	t.Log("The row is per migration where the option used to be per run, so a mixed run stamping")
	t.Log("one language on every row is the failure this asserts against.")

	for i, rec := range runs {
		if want := string(mixedLanguages[rec.Version]); rec.Runtime != want {
			t.Errorf("%s recorded runtime %q, want %q", rec.Version, rec.Runtime, want)
		}

		if want := int32(i + 1); rec.Ordinal != want {
			t.Errorf("%s is ordinal %d, want %d: the run order is the timestamp order",
				rec.Version, rec.Ordinal, want)
		}

		if want := mixedMigrations[i].name; rec.Version+"_"+rec.Name != strings.TrimSuffix(
			strings.TrimSuffix(want, ".go"), ".ts") {
			t.Errorf("row %d is %s_%s, and the %dth migration is %s", i+1, rec.Version, rec.Name,
				i+1, want)
		}
	}
}

func TestAMixedProjectIsReportedAndValidatedTheWayASingleLanguageOneIs(t *testing.T) {
	path := mixedProject(t, mixedHistoryTable+"_reports")

	if err := migr(t, path, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, path, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, path, "validate"); err != nil {
		t.Errorf("validate over a mixed project: %v", err)
	}

	if err := migr(t, path, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	out := migrOut(t, path, "status")

	for _, m := range mixedMigrations {
		if got := stateOf(t, out, m.name); got != "applied" {
			t.Errorf("status reports %s as %q, want applied:\n%s", m.name, got, out)
		}
	}

	t.Log("status builds no source at all, so a mixed directory is reportable on a machine with")
	t.Log("neither toolchain -- which is what makes it the command to reach for during a deploy.")
}

// lastBatch keeps the newest batch's rows, since the log is append-only and every run adds to it.
func lastBatch(runs []history.Record) []history.Record {
	var newest int64

	for _, rec := range runs {
		newest = max(newest, rec.Batch)
	}

	var batch []history.Record

	for _, rec := range runs {
		if rec.Batch == newest {
			batch = append(batch, rec)
		}
	}

	return batch
}
