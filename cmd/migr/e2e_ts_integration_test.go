//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver/postgres/pgtest"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/rule"
)

// The TypeScript half of the stack, driven the whole way down to a real server.
//
// M7-T7 proved the SDK emits the same IR as Go for a set of fixtures, over a database that answered
// from a manifest. Nothing below this had ever compiled one of those operations or sent it anywhere:
// migr up starts the runtime the adapter found, the driver compiles what the migration emits, and
// PostgreSQL runs it. The measure is a schema digest either side, since the history says a migration
// is applied whether or not anything happened.
//
// Four migrations, and two of them are here for paths no language has exercised end to end:
//
//   - one reads the live server with host.db.query and branches on the answer, which is what ADR
//     0009's "migrations may branch on live query results" has always claimed and nothing ran;
//   - one sends a statement of its own through host.db.exec, which is committed outside the
//     transaction its operations run in (ADR 0032) and which migr plan holds rather than running
//     (ADR 0036).
//
// M7-T8 could not prove one of those two the whole way: that a migration failing *after* an exec
// leaves that statement committed, which is ADR 0032's warning. M7-T10 proves it, and the cleanup is
// what made it possible -- a pgtest probe rather than a compensating migration, for two reasons a
// reader will otherwise try both of. A migration whose only effect is an exec compiles to no
// statements and the engine refuses it, since reporting success for a change that never happened is
// what that refusal exists to prevent; and one that dropped the table as an *operation* would be
// recorded as applied, so a second run of the test would skip it and meet the table still there.
// Invariant #3 is why the SQL lives in the driver's own subtree and not here.

// tsHistoryTable keeps this suite's runs out of the tables the .sql, Go, and driver suites use.
const tsHistoryTable = "migr_e2e_ts_migrations"

// tsMigrations are the four this suite applies, written the way migr create --lang ts writes one.
var tsMigrations = []struct{ name, body string }{
	{
		name: "20260727T170000_add_e2e_ts_accounts.ts",
		body: `import { defineMigration, t } from "@migr/sdk";

enum Width {
  Email = 255,
}

export default defineMigration({
  async up(s) {
    await s
      .createTable("e2e_ts_accounts")
      .column("id", t.int64())
      .column("email", t.string(Width.Email))
      .primaryKey("id")
      .apply();

    await s.table("e2e_ts_accounts").addIndex("idx_e2e_ts_accounts_email", "email").apply();
  },
  async down(s) {
    await s
      .table("e2e_ts_accounts")
      .dropIndex("idx_e2e_ts_accounts_email")
      .was({ columns: ["email"] })
      .drop()
      .was({
        columns: [
          { name: "id", type: t.int64() },
          { name: "email", type: t.string(Width.Email) },
        ],
        primary_key: ["id"],
      })
      .apply();
  },
});
`,
	},
	{
		name: "20260727T170100_add_e2e_ts_balance.ts",
		body: `import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s.table("e2e_ts_accounts").addColumn("balance", t.decimal(12, 2)).null().apply();
  },
});
`,
	},
	{
		name: "20260727T170200_verify_e2e_ts_accounts.ts",
		body: `import { defineMigration, literal, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    const found = await s.query(
      "SELECT count(*) FROM information_schema.tables WHERE table_name = $1",
      "e2e_ts_accounts",
    );

    const verified = s.table("e2e_ts_accounts").addColumn("verified", t.bool());

    if (counted(found) > 0) {
      verified.default(literal("false"));
    } else {
      verified.null();
    }

    await verified.apply();
  },
  async down(s) {
    await s
      .table("e2e_ts_accounts")
      .dropColumn("verified")
      .was({ name: "verified", type: t.bool(), default: literal("false") })
      .apply();
  },
});

function counted(rows) {
  const row = rows.rows[0];

  return row === undefined || row[0] === null ? 0 : Number(row[0]);
}
`,
	},
	{
		name: "20260727T170300_audit_e2e_ts_accounts.ts",
		body: `import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s.exec("CREATE TABLE e2e_ts_audit (note text NOT NULL)");

    await s.table("e2e_ts_accounts").addColumn("audited", t.bool()).null().apply();
  },
  async down(s) {
    await s
      .table("e2e_ts_accounts")
      .dropColumn("audited")
      .was({ name: "audited", type: t.bool(), nullable: true })
      .apply();

    await s.exec("DROP TABLE e2e_ts_audit");
  },
});
`,
	},
}

func TestSomeMigrationHereCarriesSyntaxNodeWillNotStrip(t *testing.T) {
	carrying := 0

	for _, m := range tsMigrations {
		if strings.Contains(m.body, "\nenum ") {
			carrying++
		}
	}

	if carrying == 0 {
		t.Error("no migration here declares an enum, so this suite would pass with the transpiler off")
		t.Log("Node strips types natively and refuses an enum, so an enum is what says the SDK")
		t.Log("compiled the file rather than Node merely loading it. Rewriting the last one as a")
		t.Log("plain number emits identical IR and passes every other test here, which is exactly")
		t.Log("why it needs a test of its own.")
	}
}

// tsProject writes a TypeScript project migr can run, and returns the path of its migr.yaml.
func tsProject(t *testing.T) string {
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
`, config.LanguageTypeScript, dsn(), tsHistoryTable)

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writePackage(t, dir)
	writeTSMigrations(t, filepath.Join(dir, "migrations"))

	return path
}

// writePackage puts the manifest and the linked SDK where an npm install in this project would.
func writePackage(t *testing.T, dir string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"e2e","type":"module"}`+"\n"), 0o600); err != nil {
		t.Fatalf("staging package.json: %v", err)
	}

	scope := filepath.Join(dir, "node_modules", "@migr")

	if err := os.MkdirAll(scope, 0o750); err != nil {
		t.Fatalf("staging node_modules: %v", err)
	}

	sdk, err := filepath.Abs(filepath.Join("..", "..", "sdk", "typescript"))
	if err != nil {
		t.Fatalf("resolving the SDK: %v", err)
	}

	if err := os.Symlink(sdk, filepath.Join(scope, "sdk")); err != nil {
		t.Fatalf("linking the SDK: %v", err)
	}
}

// writeTSMigrations stages every migration this suite runs, in the directory migr.yaml names.
func writeTSMigrations(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, m := range tsMigrations {
		if err := os.WriteFile(filepath.Join(dir, m.name), []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}
}

func TestATypeScriptMigrationAppliesAndRollsBackAgainstPostgreSQL(t *testing.T) {
	cfg := tsProject(t)

	if err := migr(t, cfg, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	before := digest(t)

	t.Cleanup(func() {
		if err := migr(t, cfg, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, cfg, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	applied := digest(t)
	if applied == before {
		t.Fatal("up changed nothing about the schema, so there is no round trip to measure")
	}

	t.Log("The width of email comes from an enum, which Node's own type stripping refuses. Reading")
	t.Log("it back out of the schema is what proves the CLI's runtime transpiled the file.")

	for _, want := range []string{
		"e2e_ts_accounts", "idx_e2e_ts_accounts_email", "balance", "verified", "audited",
		"e2e_ts_audit", "e2e_ts_accounts.email character varying(255)",
	} {
		if !strings.Contains(applied, want) {
			t.Errorf("the schema does not hold %q after up:\n%s", want, applied)
		}
	}

	if err := migr(t, cfg, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	t.Log("Both directions crossed the wire as IR from a TypeScript source: node was found, the")
	t.Log("SDK resolved, every operation compiled by the driver, and the escape hatch compensated.")

	if got := digest(t); got != before {
		t.Errorf("the schema did not return to what it was:\n before %s\n  after %s", before, got)
	}
}

func TestADerivedRollbackDropsWhatTheTypeScriptSDKNeverWroteAnUndoFor(t *testing.T) {
	cfg := tsProject(t)

	if err := migr(t, cfg, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, cfg, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, cfg, "up", "--to", "20260727T170100"); err != nil {
		t.Fatalf("up --to: %v", err)
	}

	if !strings.Contains(digest(t), "balance") {
		t.Fatal("the column the derived rollback is meant to drop was never added")
	}

	if err := migr(t, cfg, "down", "--step", "1"); err != nil {
		t.Fatalf("down --step 1: %v", err)
	}

	t.Log("The migration authors no down at all, so §7.4's no_down_authored came back over the")
	t.Log("wire and invariant #10's derivation inverted what its up had emitted.")

	rolled := digest(t)
	if strings.Contains(rolled, "balance") {
		t.Errorf("the derived rollback left the column behind:\n%s", rolled)
	}

	if !strings.Contains(rolled, "e2e_ts_accounts") {
		t.Errorf("the derived rollback took the table with it:\n%s", rolled)
	}
}

func TestASecondUpSucceedsAfterATypeScriptRollback(t *testing.T) {
	cfg := tsProject(t)

	if err := migr(t, cfg, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, cfg, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	t.Log("A rollback that recorded success without undoing anything fails on the second up and")
	t.Log("nowhere else -- the escape hatch's own drop included, since CREATE would collide.")

	for _, direction := range []string{"up", "down", "up"} {
		if err := migr(t, cfg, direction); err != nil {
			t.Fatalf("%s: %v", direction, err)
		}
	}
}

func TestAMigrationsLiveReadDecidesWhatItEmits(t *testing.T) {
	cfg := tsProject(t)

	if err := migr(t, cfg, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, cfg, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, cfg, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	t.Log("The migration asked the server whether the table exists and emitted a NOT NULL column")
	t.Log("with a default because the answer was yes. Its other branch emits a nullable one, so the")
	t.Log("nullability in the schema is what says the read happened and was answered.")

	if !strings.Contains(digest(t), "verified boolean not null default false") {
		t.Errorf("the column the populated branch emits is not there:\n%s", digest(t))
	}
}

func TestPlanHoldsTheStatementAMigrationSendsItself(t *testing.T) {
	cfg := tsProject(t)

	if err := migr(t, cfg, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	before := digest(t)
	planned := migrOut(t, cfg, "plan")

	for _, want := range []string{"create_table", "add_index", "add_column", "native",
		"CREATE TABLE e2e_ts_audit"} {
		if !strings.Contains(planned, want) {
			t.Errorf("migr plan does not print %q for a TypeScript project:\n%s", want, planned)
		}
	}

	t.Log("A held statement is printed above the operations and marked, because that is when it")
	t.Log("runs: on its own, before the transaction, and not undone by a later failure (ADR 0036).")

	if !strings.Contains(planned, "! ") {
		t.Errorf("migr plan does not mark the statement it held:\n%s", planned)
	}

	if got := digest(t); got != before {
		t.Errorf("a preview reached the database:\n before %s\n  after %s", before, got)
	}
}

func TestStatusAndTheHistoryRowReportATypeScriptProject(t *testing.T) {
	cfg := tsProject(t)

	if err := migr(t, cfg, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, cfg, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, cfg, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	status := migrOut(t, cfg, "status")
	for _, m := range tsMigrations {
		if got := stateOf(t, status, m.name); got != "applied" {
			t.Errorf("%s reads %q after up, want applied:\n%s", m.name, got, status)
		}
	}

	t.Log("The newest batch rather than the whole log: history is append-only, so a row an earlier")
	t.Log("run wrote -- including one a mutation sweep wrote -- is not this run's answer.")

	for _, rec := range lastBatch(recorded(t, tsHistoryTable)) {
		if rec.Runtime != string(config.LanguageTypeScript) {
			t.Errorf("%s was recorded as runtime %q, want %q", rec.Version, rec.Runtime,
				config.LanguageTypeScript)
		}
	}
}

// assertWarnsOfTheTwoPatternsTheseFixturesUse holds the two lint rules these fixtures trip (M10-T5).
func assertWarnsOfTheTwoPatternsTheseFixturesUse(t *testing.T, out string) {
	t.Helper()

	for _, want := range []string{"2 warnings",
		string(rule.MissingDown), "20260727T170100",
		string(rule.MixedDataMigration), "20260727T170300"} {
		if !strings.Contains(out, want) {
			t.Errorf("validate does not warn about %q:\n%s", want, out)
		}
	}

	t.Log("Both fixtures use their pattern deliberately: one omits its down so the engine derives")
	t.Log("the inverse, and one execs a CREATE TABLE beside an addColumn to prove ADR 0032's")
	t.Log("warning that the statement is committed on its own. That is why neither rule is an")
	t.Log("error -- a legitimate use of a pattern is what a warning is for.")

	if strings.Contains(out, "problem") && !strings.Contains(out, "no problems found") {
		t.Errorf("a warning was counted as a problem:\n%s", out)
	}
}

func TestValidateChecksATypeScriptProjectWithoutOpeningADatabase(t *testing.T) {
	cfg := tsProject(t)

	out := migrOut(t, cfg, "validate")

	t.Log("ADR 0040 again, from the second language: the process starts and every database call it")
	t.Log("could make refuses, so a migration that reads is reported unchecked rather than passed.")

	if !strings.Contains(out, "not checked") {
		t.Errorf("validate does not report the reading migration as unchecked (not checked):\n%s", out)
	}

	if !strings.Contains(out, "20260727T170200") {
		t.Errorf("validate does not name the migration it could not check:\n%s", out)
	}

	assertWarnsOfTheTwoPatternsTheseFixturesUse(t, out)

	t.Cleanup(func() {
		if err := migr(t, cfg, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, cfg, "up"); err != nil {
		t.Errorf("what validate passed, up refused: %v", err)
	}
}

// orphanTable is what the failing migration below execs into being, and never drops.
const orphanTable = "e2e_ts_orphan"

// orphanMigration execs a statement and then emits an operation the server refuses.
//
// The operation names a table that is there and asks for something the server will not do: a NOT
// NULL column with no default, added to a table that already holds a row. It named a table nothing
// created until M11-T2, which engine.Unrestorable refuses before anything compiles -- and this case
// is about what an exec leaves behind when the *server* refuses what follows it.
const orphanMigration = `import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s.exec("CREATE TABLE ` + orphanTable + ` (id bigint)");
    await s.exec("INSERT INTO ` + orphanTable + ` (id) VALUES (1)");
    await s.table("` + orphanTable + `").addColumn("late", t.bool()).apply();
  },
});
`

func TestAStatementAMigrationRanItselfSurvivesTheFailureThatFollowedIt(t *testing.T) {
	failing := oneMigrationProject(t, tsHistoryTable+"_orphan",
		"20260727T190000_leave_e2e_ts_orphan.ts", orphanMigration)

	dropOrphan(t)
	t.Cleanup(func() { dropOrphan(t) })

	reported := migrFailure(t, failing, "up")

	t.Log("ADR 0032's whole warning, measured: host.db.exec runs while the migration is deciding")
	t.Log("what it does, which is before the engine has compiled anything or opened a transaction.")
	t.Log("So the statement is committed on its own and a later failure does not undo it -- the")
	t.Log("compensation is somebody's to write, and here it is a probe rather than a migration.")

	if !strings.Contains(reported, orphanTable) {
		t.Errorf("the failure does not name what the server refused:\n%s", reported)
	}

	if !strings.Contains(reported, "contains null values") {
		t.Errorf("the failure does not carry the server's own refusal:\n%s", reported)
	}

	if applied := digest(t); !strings.Contains(applied, orphanTable) {
		t.Errorf("the table the migration execed is gone, and nothing rolled it back:\n%s", applied)
	}

	runs := recorded(t, tsHistoryTable+"_orphan")
	if len(runs) == 0 {
		t.Fatal("the history holds no row for the run that failed")
	}

	last := runs[len(runs)-1]

	if last.Outcome == history.Applied {
		t.Errorf("the run that failed is recorded as %q", last.Outcome)
		t.Log("The log is append-only, so this is the newest row rather than the only one: an")
		t.Log("earlier run of this test left its own, and neither is rolled back.")
	}
}

// oneMigrationProject writes a TypeScript project holding exactly one migration, and returns its config.
func oneMigrationProject(t *testing.T, table, name, body string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "migr.yaml")

	manifest := fmt.Sprintf(`version: 1
migrations:
  dir: ./migrations
  language: %s
databases:
  default:
    driver: postgres
    dsn: %s
history:
  table: %s
`, config.LanguageTypeScript, dsn(), table)

	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writePackage(t, dir)

	migrations := filepath.Join(dir, "migrations")

	if err := os.MkdirAll(migrations, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	if err := os.WriteFile(filepath.Join(migrations, name), []byte(body), 0o600); err != nil {
		t.Fatalf("staging %s: %v", name, err)
	}

	return path
}

// dropOrphan removes the table the failure leaves behind, which nothing in a migration can undo.
func dropOrphan(t *testing.T) {
	t.Helper()

	outliving := context.WithoutCancel(t.Context())

	if err := pgtest.Drop(outliving, openedWith(outliving, t, ""), targetSchema, orphanTable); err != nil {
		t.Errorf("dropping the table the escape hatch left behind: %v", err)
	}
}
