//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver/postgres/pgtest"
)

// The expand/contract walk e2e_sqlite_replace_test.go runs, against PostgreSQL 17 through the whole
// stack. The fill migration is irreversible by design, so migr down cannot clear what a run leaves
// and the table goes through pgtest.Drop -- the one place invariant #3 leaves for SQL a test needs.

// replaceTable keeps this suite's table out of every other suite's.
const replaceTable = "migr_e2e_replaced"

// replaceHistoryTable keeps this suite's runs, and their checkpoints, out of every other suite's.
const replaceHistoryTable = "migr_e2e_replace_migrations"

// pgReplaceProject is a Go project on PostgreSQL whose migrations replace one column in four phases.
func pgReplaceProject(t *testing.T) string {
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
safety:
  chunk_rows: 50
  chunk_pause: 1ms
validation:
  allow_destructive: [data_transform, drop_column]
`, dsn(), replaceHistoryTable)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writeGoModule(t, dir)

	migrations := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migrations, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	writeReplacement(t, migrations, replaceTable)
	clearReplacement(t)
	t.Cleanup(func() { clearReplacement(t) })

	return config
}

// clearReplacement drops what a run of this suite leaves: its table, its history and its checkpoints.
func clearReplacement(t *testing.T) {
	t.Helper()

	outliving := context.WithoutCancel(t.Context())

	for _, table := range []string{replaceTable, replaceHistoryTable, replaceHistoryTable + "_progress",
		replaceHistoryTable + "_releases"} {
		if err := pgtest.Drop(outliving, openedWith(outliving, t, ""), targetSchema, table); err != nil {
			t.Fatalf("dropping %s: %v", table, err)
		}
	}
}

func TestAColumnReplacementWalksItsFourPhasesAndRollsBackWholeOnPostgreSQL(t *testing.T) {
	config := pgReplaceProject(t)

	if err := migr(t, config, "up", "--step", "2"); err != nil {
		t.Fatalf("creating and filling the table: %v", err)
	}

	before := digest(t, replaceHistoryTable)

	for i, phase := range []string{"expand", "backfill", "swap", "contract"} {
		release := fmt.Sprintf("r%d", i+1)

		out := migrOut(t, config, "up", "--step", "1", "--release", release)
		if phase == "backfill" && !strings.Contains(out, "5 chunks, 200 rows written") {
			t.Errorf("the backfill did not run in chunks:\n%s", out)
		}

		if held := migrOut(t, config, "up", "--release", release); phase != "contract" &&
			!strings.Contains(held, "waits for a later release") {
			t.Errorf("the step after %s ran in the deploy that ran %s:\n%s", phase, phase, held)
		}
	}

	if err := migr(t, config, "up", "--release", "r5"); err != nil {
		t.Fatalf("the check that every row kept its value under its own name: %v", err)
	}

	if after := digest(t, replaceHistoryTable); strings.Contains(after, "email_old") ||
		strings.Contains(after, "email_next") || !strings.Contains(after, "string(320)") {
		t.Errorf("the replacement left a schema that is not the one it declared:\n%s", after)
	}

	rolledBack := migrOut(t, config, "down", "--step", "5")
	if !strings.Contains(rolledBack, "20261001T090300_backfill_email.go  0 statements") {
		t.Errorf("the backfill's down was not recorded as undoing nothing:\n%s", rolledBack)
	}

	if restored := digest(t, replaceHistoryTable); restored != before {
		t.Errorf("the rollback did not restore the schema\nbefore\n%s\nafter\n%s", before, restored)
	}

	for _, release := range []string{"r6", "r7", "r8", "r9"} {
		if err := migr(t, config, "up", "--release", release); err != nil {
			t.Fatalf("running the replacement and its check again under %s: %v", release, err)
		}
	}

	if status := migrOut(t, config, "status"); strings.Contains(status, "pending") {
		t.Errorf("four deploys did not carry the replacement and its check through:\n%s", status)
	}
}
