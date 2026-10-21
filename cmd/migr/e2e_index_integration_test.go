//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// indexHistoryTable keeps this case's runs apart from every other suite's.
const indexHistoryTable = "migr_e2e_index_migrations"

// indexMigrations create a table holding a duplicate, then build a unique index over it concurrently.
var indexMigrations = []struct{ name, body string }{
	{
		name: "20261021T120000_add_e2e_dups.sql",
		body: "-- migr:up\nCREATE TABLE e2e_dups (id int PRIMARY KEY, v int);\n" +
			"INSERT INTO e2e_dups VALUES (1, 1), (2, 1);\n\n-- migr:down\nDROP TABLE e2e_dups;\n",
	},
	{
		name: "20261021T120100_index_e2e_dups.sql",
		body: "-- migr:up\n-- migr:no-transaction\nCREATE UNIQUE INDEX CONCURRENTLY e2e_dups_v ON e2e_dups (v);\n\n" +
			"-- migr:down\nDROP INDEX e2e_dups_v;\n",
	},
}

// indexProject writes the project whose second migration's concurrent build fails on the duplicate.
func indexProject(t *testing.T) string {
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
`, dsn(), indexHistoryTable)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "migrations"), 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, m := range indexMigrations {
		if err := os.WriteFile(filepath.Join(dir, "migrations", m.name), []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}

	return config
}

func TestAFailedConcurrentBuildLeavesNoInvalidIndexBehindAgainstPostgreSQL(t *testing.T) {
	config := indexProject(t)

	t.Cleanup(func() {
		if err := migr(t, config, "down", "--step", "1"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	stderr := migrFailure(t, config, "up")
	if !strings.Contains(stderr, `dropped the INVALID index "e2e_dups_v"`) {
		t.Errorf("up does not say it dropped what the failed build left:\n%s", stderr)
	}

	var status struct {
		Invalid []struct {
			Index string `json:"index"`
		} `json:"invalid_indexes"`
	}

	if err := json.Unmarshal([]byte(migrOut(t, config, "status", "--json")), &status); err != nil {
		t.Fatalf("reading status --json: %v", err)
	}

	for _, left := range status.Invalid {
		if left.Index == "e2e_dups_v" {
			t.Error("status still reports the failed build's index INVALID, and it was dropped")
		}
	}

	if state := stateOf(t, migrOut(t, config, "status"), "index_e2e_dups"); state == "applied" {
		t.Errorf("the failed build is %q", state)
	}

	t.Log("A raw statement declaring no-transaction is repaired too: PostgreSQL left the unique index")
	t.Log("INVALID when it met the duplicate, and the run dropped it before reporting the failure.")
}
