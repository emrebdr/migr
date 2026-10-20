//go:build integration

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lagHistoryTable keeps this case's runs apart from every other suite's.
const lagHistoryTable = "migr_e2e_lag_migrations"

// lagDSN is postgres16, the server the compose standby replays three seconds late.
func lagDSN() string {
	if override := os.Getenv("MIGR_TEST_PG16_DSN"); override != "" {
		return override
	}

	return strings.Replace(defaultDSN, ":55417/", ":55416/", 1)
}

// lagMigrations are a filled table and a data-only backfill of it, which runs in chunks.
var lagMigrations = []struct{ name, body string }{
	{
		name: "20261020T130000_add_e2e_lag_people.go",
		body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddPeople{}) }

// AddPeople creates and fills the table the backfill paces through.
type AddPeople struct{}

// Up creates the table and fills it.
func (AddPeople) Up(ctx context.Context, s *migr.Schema) error {
	if err := s.CreateTable("e2e_lag_people").Column("id", migr.Int64()).
		Column("label", migr.String(16)).Null().PrimaryKey("id").Apply(ctx); err != nil {
		return err
	}

	return s.Raw("postgres", "INSERT INTO e2e_lag_people SELECT i, NULL FROM generate_series(1, 200) i").Apply(ctx)
}

// Down drops it.
func (AddPeople) Down(ctx context.Context, s *migr.Schema) error {
	return s.Raw("postgres", "DROP TABLE e2e_lag_people").Apply(ctx)
}
`,
	},
	{
		name: "20261020T130100_label_e2e_lag_people.go",
		body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(LabelPeople{}) }

// LabelPeople fills every label, a chunk at a time.
type LabelPeople struct{}

// Up backfills the label.
func (LabelPeople) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("e2e_lag_people").Backfill("label", migr.String(16), migr.Literal("x")).Apply(ctx)
}

// Down undoes nothing of its own: the table's own down drops what it filled.
func (LabelPeople) Down(context.Context, *migr.Schema) error { return nil }
`,
	},
}

// lagProject writes the Go project the paced backfill lives in, and returns its migr.yaml.
func lagProject(t *testing.T) string {
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
  chunk_rows: 25
  chunk_pause: 300ms
  replica_lag_threshold: 1s
validation:
  allow_destructive: [raw]
`, lagDSN(), lagHistoryTable)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	writeGoModule(t, dir)

	migrations := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migrations, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, m := range lagMigrations {
		if err := os.WriteFile(filepath.Join(migrations, m.name), []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}

	return config
}

func TestABackfillIsHeldBackWhileTheStandbyTrailsAndSaysSo(t *testing.T) {
	config := lagProject(t)

	if err := migr(t, config, "down", "--step", "2"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, config, "down", "--step", "2"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if planned := migrOut(t, config, "plan"); !strings.Contains(planned,
		"held back while a replica trails by more than 1s") {
		t.Errorf("the plan does not say what holds a chunk back:\n%s", planned)
	}

	stdout, stderr := migrBoth(t, config, "-v", "up")

	for _, want := range []string{"holding the next chunk back", "standby16"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("-v does not say %q:\n%s", want, stderr)
		}
	}

	if !strings.Contains(stdout, "200 rows written") || !strings.Contains(stdout, "by replica lag") {
		t.Errorf("the summary does not report the rows and the hold:\n%s", stdout)
	}

	t.Log("The standby replays three seconds late, so once replay_lag passes a second the next")
	t.Log("chunk waits, polled, until the standby has replayed every byte -- and the run says so.")
}
