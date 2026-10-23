//go:build integration

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/schema"
)

// concurrentHistory keeps this case's runs apart from the round trip's, whose counts it would move.
const concurrentHistory = "migr_e2e_concurrently"

// concurrentMigration fills a table and builds its index concurrently, outside any transaction.
const concurrentMigration = "-- migr:up\nCREATE TABLE e2e_people (id bigint PRIMARY KEY, email text);\n" +
	"INSERT INTO e2e_people SELECT i, 'p' || i FROM generate_series(1, 1000) i;\n" +
	"-- migr:no-transaction\nCREATE INDEX CONCURRENTLY idx_e2e_people_email ON e2e_people (email);\n\n" +
	"-- migr:down\nDROP TABLE e2e_people;\n"

// concurrentProject writes a migr.yaml over this suite's server with the one concurrent migration.
func concurrentProject(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	config := filepath.Join(dir, "migr.yaml")

	body := fmt.Sprintf(`version: 1
migrations:
  dir: ./migrations
  language: sql
databases:
  default:
    driver: postgres
    dsn: %s
history:
  table: %s
validation:
  allow_destructive: [raw]
`, dsn(), concurrentHistory)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	migrations := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migrations, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	path := filepath.Join(migrations, "20260727T153200_index_e2e_people.sql")
	if err := os.WriteFile(path, []byte(concurrentMigration), 0o600); err != nil {
		t.Fatalf("staging the migration: %v", err)
	}

	return config
}

func TestCreateIndexConcurrentlyRunsEndToEndAgainstPostgreSQL(t *testing.T) {
	config := concurrentProject(t)

	if err := migr(t, config, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	before := digest(t, concurrentHistory)

	if plan := migrOut(t, config, "plan"); !strings.Contains(plan, "[no transaction]") ||
		!strings.Contains(plan, "runs in 2 parts") {
		t.Errorf("migr plan does not mark the concurrent build:\n%s", plan)
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up returned %v, want CREATE INDEX CONCURRENTLY run outside any transaction", err)
	}

	if got := digest(t, concurrentHistory); !strings.Contains(got, "idx_e2e_people_email") {
		t.Errorf("after the run the schema holds no index:\n%s", got)
	}

	if got := applied(recorded(t, concurrentHistory), "up"); got < 1 {
		t.Errorf("the history holds %d applied runs, want the concurrent migration's", got)
	}

	if err := migr(t, config, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	if got := digest(t, concurrentHistory); got != before {
		t.Errorf("the rollback did not restore the starting schema:\n%s", schema.Difference(before, got))
	}

	t.Log("The task's done-when: a .sql migration marks one statement with migr:no-transaction,")
	t.Log("migr up runs the table and its rows in one transaction and the index build bare, and")
	t.Log("the history says how far it got after each part.")
}
