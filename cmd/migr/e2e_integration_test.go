//go:build integration

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/cli"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/postgres"
	"github.com/emrebdr/migr/internal/history"
)

// dsnEnv overrides the connection string below, as it does for the driver's own suite.
const dsnEnv = "MIGR_TEST_PG17_DSN"

// defaultDSN matches test/docker-compose.yml's postgres17 service, which make test-integration runs.
const defaultDSN = "postgres://migr:migr@127.0.0.1:55417/migr?sslmode=disable"

// historyTable keeps this suite's runs out of the table the driver's own tests use.
const historyTable = "migr_e2e_migrations"

// migrations are the two this suite applies, written in the format migr create writes.
var migrations = []struct{ name, body string }{
	{
		name: "20260727T153000_add_e2e_users.sql",
		body: "-- migr:up\nCREATE TABLE e2e_users (id bigint PRIMARY KEY);\n" +
			"CREATE INDEX idx_e2e_users_id ON e2e_users (id);\n\n" +
			"-- migr:down\nDROP TABLE e2e_users;\n",
	},
	{
		name: "20260727T153100_add_e2e_teams.sql",
		body: "-- migr:up\nCREATE TABLE e2e_teams (id bigint PRIMARY KEY);\n\n" +
			"-- migr:down\nDROP TABLE e2e_teams;\n",
	},
}

// dsn returns the server to run against, preferring the environment override.
func dsn() string {
	if override := os.Getenv(dsnEnv); override != "" {
		return override
	}

	return defaultDSN
}

// project writes a migr.yaml and the migrations beside it, and returns the config's path.
func project(t *testing.T) string {
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
`, dsn(), historyTable)

	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "migrations"), 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, m := range migrations {
		if err := os.WriteFile(filepath.Join(dir, "migrations", m.name), []byte(m.body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", m.name, err)
		}
	}

	return config
}

// migr runs the CLI the way the binary does, against the driver registry this binary carries.
func migr(t *testing.T, config string, args ...string) error {
	t.Helper()

	registry, err := drivers()
	if err != nil {
		t.Fatalf("building the driver registry: %v", err)
	}

	ctx := context.WithoutCancel(t.Context())

	return cli.Execute(ctx, registry, append([]string{"--config", config}, args...))
}

// migrOut runs the CLI and returns what it printed, for the commands whose whole product is output.
func migrOut(t *testing.T, config string, args ...string) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("opening a pipe: %v", err)
	}

	stdout := os.Stdout
	os.Stdout = w
	runErr := migr(t, config, args...)
	os.Stdout = stdout

	if err := w.Close(); err != nil {
		t.Fatalf("closing the pipe: %v", err)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading what migr %s printed: %v", args[0], err)
	}

	if runErr != nil {
		t.Fatalf("migr %s: %v\n%s", args[0], runErr, out)
	}

	return string(out)
}

// stateOf returns the state migr status printed for one migration, so a test asserts a row.
func stateOf(t *testing.T, out, name string) string {
	t.Helper()

	for line := range strings.Lines(out) {
		if strings.Contains(line, name) {
			return strings.Fields(line)[0]
		}
	}

	t.Fatalf("migr status printed no row for %s:\n%s", name, out)

	return ""
}

// countRuns counts the lines migr history printed, keyed by the migration and direction each names.
func countRuns(out string) map[string]int {
	counts := make(map[string]int)

	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) > migrationField+1 && slices.Contains(directions, fields[migrationField+1]) {
			counts[fields[migrationField]+" "+fields[migrationField+1]]++
		}
	}

	return counts
}

// migrationField is where a printed run names its migration: after the batch and the ordinal.
const migrationField = 2

// directions are what a run's direction column may hold, which is what marks a line as a run.
var directions = []string{"up", "down"}

// recorded returns every run this suite's history table holds, through the store contract alone.
func recorded(t *testing.T) []history.Record {
	t.Helper()

	d, err := postgres.New(driver.Options{HistoryTable: historyTable})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conn, err := d.Open(t.Context(), dsn())
	if err != nil {
		t.Fatalf("Open: %v\n\nRun `make test-integration`, which brings the server up first.", err)
	}

	defer func() {
		if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	runs, err := conn.History().List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	return runs
}

// applied counts the completed runs in one direction, which is what a round trip is measured in.
func applied(runs []history.Record, direction string) int {
	found := 0

	for _, rec := range runs {
		if rec.Outcome == history.Applied && string(rec.Direction) == direction {
			found++
		}
	}

	return found
}

func TestTheWholeStackAppliesAndRollsBackAgainstPostgreSQL(t *testing.T) {
	config := project(t)

	if err := migr(t, config, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	rows := len(recorded(t))
	ups := applied(recorded(t), "up")
	downs := applied(recorded(t), "down")

	t.Cleanup(func() {
		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, config, "up", "--dry-run"); err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}

	if got := len(recorded(t)); got != rows {
		t.Fatalf("a dry run wrote %d history rows", got-rows)
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if got := applied(recorded(t), "up"); got != ups+2 {
		t.Fatalf("the history holds %d applied runs, want %d", got, ups+2)
	}

	if err := migr(t, config, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	if got := applied(recorded(t), "down"); got != downs+2 {
		t.Fatalf("the history holds %d rollbacks, want %d", got, downs+2)
	}

	if err := migr(t, config, "up"); err != nil {
		t.Errorf("the second up: %v", err)
		t.Log("A rollback that reported success without dropping the tables would fail here, and")
		t.Log("nowhere else: the history says they are gone either way.")
	}
}

func TestStatusAndHistoryReportTheRoundTripAgainstPostgreSQL(t *testing.T) {
	config := project(t)

	if err := migr(t, config, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	base := countRuns(migrOut(t, config, "history"))

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	applied := migrOut(t, config, "status")
	for _, m := range migrations {
		if got := stateOf(t, applied, m.name); got != "applied" {
			t.Errorf("%s reads %q after up, want applied:\n%s", m.name, got, applied)
		}
	}

	log := countRuns(migrOut(t, config, "history"))
	for _, m := range migrations {
		name := strings.TrimSuffix(m.name, ".sql")

		if got := log[name+" up"] - base[name+" up"]; got != 1 {
			t.Errorf("up appended %d runs for %s to the log", got, m.name)
		}
	}

	if err := migr(t, config, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	pending := migrOut(t, config, "status")
	for _, m := range migrations {
		if got := stateOf(t, pending, m.name); got != "pending" {
			t.Errorf("%s reads %q after down, want pending:\n%s", m.name, got, pending)
		}
	}

	undone := countRuns(migrOut(t, config, "history"))
	for _, m := range migrations {
		name := strings.TrimSuffix(m.name, ".sql")

		if up, down := undone[name+" up"]-base[name+" up"], undone[name+" down"]-base[name+" down"]; up != 1 || down != 1 {
			t.Errorf("the log holds %d new up and %d new down runs for %s, want one of each", up, down, m.name)
			t.Log("A rollback appends a row rather than deleting one, so the log still shows the")
			t.Log("run it undid. That is what migr status folds and what this command prints.")
		}
	}
}

func TestValidatePassesWhatTheWholeStackThenRuns(t *testing.T) {
	config := project(t)

	if err := migr(t, config, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	rows := len(recorded(t))

	out := migrOut(t, config, "validate")
	if !strings.Contains(out, "no problems found") {
		t.Fatalf("validate found something in this suite's own migrations:\n%s", out)
	}

	if got := len(recorded(t)); got != rows {
		t.Errorf("validate wrote %d history rows", got-rows)
	}

	if err := migr(t, config, "up"); err != nil {
		t.Errorf("what validate passed, up refused: %v", err)
		t.Log("Validation compiles every migration through the same planner the executor uses,")
		t.Log("so a project it passes cannot fail for a reason a compile could have found.")
	}
}

func TestPlanPrintsTheStatementsARealServerWouldRun(t *testing.T) {
	config := project(t)

	if err := migr(t, config, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	rows := len(recorded(t))
	planned := migrOut(t, config, "plan")

	for _, want := range []string{
		"CREATE TABLE e2e_users (id bigint PRIMARY KEY);",
		"CREATE INDEX idx_e2e_users_id ON e2e_users (id);",
		"CREATE TABLE e2e_teams (id bigint PRIMARY KEY);",
		"native",
	} {
		if !strings.Contains(planned, want) {
			t.Errorf("migr plan does not print %q:\n%s", want, planned)
		}
	}

	if got := len(recorded(t)); got != rows {
		t.Fatalf("a plan wrote %d history rows", got-rows)
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	undo := migrOut(t, config, "plan", "--down")
	for _, want := range []string{"DROP TABLE e2e_teams;", "DROP TABLE e2e_users;"} {
		if !strings.Contains(undo, want) {
			t.Errorf("migr plan --down does not print %q:\n%s", want, undo)
			t.Log("A rollback is the direction that most needs this: an authored down is the only")
			t.Log("thing standing between an operator and statements nobody wrote.")
		}
	}
}

func TestNarrowingWorksAgainstARealServer(t *testing.T) {
	config := project(t)

	if err := migr(t, config, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	t.Cleanup(func() {
		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	before := applied(recorded(t), "up")

	if err := migr(t, config, "up", "--step", "1"); err != nil {
		t.Fatalf("up --step 1: %v", err)
	}

	if got := applied(recorded(t), "up"); got != before+1 {
		t.Fatalf("--step 1 applied %d migrations", got-before)
	}

	if err := migr(t, config, "up", "--to", "20260727T153100"); err != nil {
		t.Fatalf("up --to: %v", err)
	}

	if got := applied(recorded(t), "up"); got != before+2 {
		t.Errorf("--to left %d migrations applied, want the database at that version", got-before)
	}
}
