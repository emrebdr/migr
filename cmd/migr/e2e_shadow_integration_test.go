//go:build integration

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shadowHistoryTable keeps this case's runs apart from every other suite's.
const shadowHistoryTable = "migr_e2e_shadow_migrations"

// shadowMigrations create a keyed table and then index it through a shadow table.
var shadowMigrations = []struct{ name, body string }{
	{
		name: "20261020T140000_add_e2e_shadow_people.go",
		body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddPeople{}) }

// AddPeople creates the table the shadow-table migration changes.
type AddPeople struct{}

// Up creates the table.
func (AddPeople) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("e2e_shadow_people").Column("id", migr.Int64()).
		Column("label", migr.String(16)).Null().PrimaryKey("id").Apply(ctx)
}
`,
	},
	{
		name: "20261020T140100_index_e2e_shadow_people.go",
		body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(IndexPeople{}) }

// IndexPeople indexes the label through a shadow table.
type IndexPeople struct{}

// Online declares the shadow table.
func (IndexPeople) Online() migr.Strategy { return migr.ShadowTable }

// Up indexes the label.
func (IndexPeople) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("e2e_shadow_people").AddIndex("e2e_shadow_people_label", "label").Apply(ctx)
}
`,
	},
}

// shadowProject writes the Go project the shadow-table migration lives in, against one server.
func shadowProject(t *testing.T, driverName, target string) string {
	t.Helper()

	dir := t.TempDir()
	config := filepath.Join(dir, "migr.yaml")

	body := fmt.Sprintf(`version: 1
migrations:
  dir: ./migrations
  language: go
databases:
  default:
    driver: %s
    dsn: %s
history:
  table: %s
`, driverName, target, shadowHistoryTable)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writeGoModule(t, dir)

	migrations := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migrations, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, m := range shadowMigrations {
		if err := os.WriteFile(filepath.Join(migrations, m.name), []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}

	return config
}

func TestAShadowTableMigrationIsRefusedBeforeAnythingIsCreatedNamingWhatEachServerLacks(t *testing.T) {
	for _, tc := range []struct {
		name, driver, dsn, lacks string
	}{
		{name: "postgres16", driver: "postgres", dsn: strings.Replace(defaultDSN, ":55417/", ":55416/", 1),
			lacks: "wal_level = logical (it is replica"},
		{name: "postgres17", driver: "postgres", dsn: defaultDSN,
			lacks: "the postgres driver does not perform shadow-table migration"},
		{name: "mysql80", driver: "mysql", dsn: "migr:migr@tcp(127.0.0.1:55480)/migr",
			lacks: "the REPLICATION SLAVE privilege"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := shadowProject(t, tc.driver, tc.dsn)

			t.Cleanup(func() {
				if err := migr(t, config, "down", "--step", "1"); err != nil {
					t.Errorf("leaving the server as this suite found it: %v", err)
				}
			})

			if planned := migrOut(t, config, "plan"); !strings.Contains(planned, tc.lacks) {
				t.Errorf("the plan does not say %q:\n%s", tc.lacks, planned)
			}

			stderr := migrFailure(t, config, "up")
			if !strings.Contains(stderr, tc.lacks) || !strings.Contains(stderr, "nothing was created") {
				t.Errorf("up does not refuse naming %q:\n%s", tc.lacks, stderr)
			}

			status := migrOut(t, config, "status")
			if state := stateOf(t, status, "index_e2e_shadow_people"); state != "pending" {
				t.Errorf("the refused migration is %q, want pending:\n%s", state, status)
			}

			if strings.Contains(migrOut(t, config, "history"), "index_e2e_shadow_people") {
				t.Error("the refused migration left a history row, and nothing was to be created")
			}
		})
	}

	t.Log("Every server in compose refuses for its own reason: one for a setting, one because migr's")
	t.Log("driver reads no log yet, one for a privilege -- and each before a copy or a stream exists.")
}
