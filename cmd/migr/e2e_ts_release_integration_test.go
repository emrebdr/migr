//go:build integration

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
)

// tsStepsHistoryTable keeps this case's runs apart from every other suite's.
const tsStepsHistoryTable = "migr_e2e_ts_steps"

// tsSteps are a table and the first two phases of replacing one of its columns, in TypeScript.
var tsSteps = []struct{ name, body string }{
	{
		name: "20260727T171000_add_e2e_ts_steps.ts",
		body: `import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s.createTable("e2e_ts_steps").column("id", t.int64()).column("email", t.string(255))
      .primaryKey("id").apply();
  },
  async down(s) {
    await s.table("e2e_ts_steps").drop().was({
      columns: [{ name: "id", type: t.int64() }, { name: "email", type: t.string(255) }],
      primary_key: ["id"],
    }).apply();
  },
});
`,
	},
	{
		name: "20260727T171100_expand_e2e_ts_steps_email.ts",
		body: `import { replaceColumn, t } from "@migr/sdk";

export default replaceColumn("e2e_ts_steps", "email", t.string(255)).with(t.string(320)).expand();
`,
	},
	{
		name: "20260727T171200_backfill_e2e_ts_steps_email.ts",
		body: `import { replaceColumn, t } from "@migr/sdk";

export default replaceColumn("e2e_ts_steps", "email", t.string(255)).with(t.string(320)).backfill();
`,
	},
}

// tsStepsProject writes the TypeScript project these phases live in, and returns its migr.yaml.
func tsStepsProject(t *testing.T) string {
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
validation:
  allow_destructive: [data_transform, drop_column, drop_table]
`, config.LanguageTypeScript, dsn(), tsStepsHistoryTable)

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writePackage(t, dir)

	migrations := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migrations, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, m := range tsSteps {
		if err := os.WriteFile(filepath.Join(migrations, m.name), []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}

	return path
}

func TestATypeScriptBackfillWaitsForTheReleaseAfterItsExpand(t *testing.T) {
	config := tsStepsProject(t)

	if err := migr(t, config, "down", "--step", "3"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, config, "down", "--step", "3"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	held := migrOut(t, config, "up", "--release", "r1")
	for _, want := range []string{"applied 2 migrations", "held: 20260727T171200_backfill_e2e_ts_steps_email.ts",
		`belongs to release "r1", which is this one`} {
		if !strings.Contains(held, want) {
			t.Errorf("up --release r1 does not say %q:\n%s", want, held)
		}
	}

	if later := migrOut(t, config, "up", "--release", "r2"); !strings.Contains(later, "applied 1 migration") ||
		strings.Contains(later, "held") {
		t.Errorf("up --release r2 did not run the backfill:\n%s", later)
	}

	t.Log("The TypeScript SDK declares each phase of a replacement the way the Go one does, and the")
	t.Log("engine holds the backfill to a release after its expand whichever language wrote it.")
}
