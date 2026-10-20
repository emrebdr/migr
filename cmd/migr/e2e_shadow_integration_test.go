//go:build integration

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/mysql"
	"github.com/emrebdr/migr/internal/history"
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

	return shadowProjectIn(t, driverName, target, shadowHistoryTable)
}

// shadowProjectIn writes that project recording its runs in a history table of its own.
func shadowProjectIn(t *testing.T, driverName, target, historyTable string) string {
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
`, driverName, target, historyTable)

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
		{name: "mysql80 without the replication grants", driver: "mysql",
			dsn: "migr_plain:migr_plain@tcp(127.0.0.1:55480)/migr", lacks: "the REPLICATION SLAVE privilege"},
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

// shadowRunTable keeps the run that cuts over apart from the refusals, whose state it would otherwise meet.
const shadowRunTable = "migr_e2e_shadow_run_migrations"

// shadowVersion is the migration that runs through the shadow table.
const shadowVersion = "20261020T140100"

// mysqlSession opens a session on mysql80 bound to a history table, which is what an operator's tool would hold.
func mysqlSession(t *testing.T, historyTable string) driver.Conn {
	t.Helper()

	d, err := mysql.New(driver.Options{HistoryTable: historyTable})
	if err != nil {
		t.Fatalf("building the mysql driver: %v", err)
	}

	conn, err := d.Open(t.Context(), mysql80DSN)
	if err != nil {
		t.Fatalf("opening a session on mysql80: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })

	return conn
}

// mysql80DSN is the compose MySQL whose test user holds the replication grants.
const mysql80DSN = "migr:migr@tcp(127.0.0.1:55480)/migr"

// confirmWhenSyncing confirms the cutover once the copy is held in sync, as migr cutover --confirm will.
func confirmWhenSyncing(conn driver.Conn) chan error {
	confirmed := make(chan error, 1)
	store := conn.History().(history.Shadows)

	go func() {
		for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			states, err := store.ShadowStates(context.Background())
			if state, found := history.FindShadow(states, shadowVersion); err == nil && found &&
				state.Stage == history.StageSyncing {
				confirmed <- store.ConfirmShadow(context.Background(), shadowVersion)

				return
			}
		}

		confirmed <- errors.New("the shadow-table migration never reached syncing")
	}()

	return confirmed
}

// retireLeft drops what a cut-over shadow-table migration keeps, through the driver's own statements.
func retireLeft(t *testing.T, conn driver.Conn) {
	t.Helper()

	ctx := context.WithoutCancel(t.Context())
	store := conn.History().(history.Shadows)

	states, err := store.ShadowStates(ctx)
	state, found := history.FindShadow(states, shadowVersion)

	if err != nil || !found {
		return
	}

	stmts, err := conn.(driver.Shadower).DropRetired(driver.Shadow{Table: state.Table, Shadow: state.Shadow,
		Retired: state.Retired})
	if err != nil {
		t.Errorf("compiling the retirement: %v", err)
	}

	for _, stmt := range stmts {
		if err := conn.Exec(ctx, stmt); err != nil {
			t.Errorf("dropping %s: %v", state.Retired, err)
		}
	}

	if err := conn.Transact(ctx, func(ctx context.Context) error { return store.ClearShadow(ctx, shadowVersion) }); err != nil {
		t.Errorf("clearing the shadow-table state: %v", err)
	}
}

func TestAShadowTableMigrationOnMySQLHoldsUntilConfirmedAndThenCutsOver(t *testing.T) {
	config := shadowProjectIn(t, "mysql", mysql80DSN, shadowRunTable)
	conn := mysqlSession(t, shadowRunTable)

	t.Cleanup(func() {
		if err := migr(t, config, "down", "--step", "2"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}

		retireLeft(t, conn)
	})

	planned := migrOut(t, config, "plan")
	for _, want := range []string{"⧉  through a shadow table: copies e2e_shadow_people", "binlog_expire_logs_seconds"} {
		if !strings.Contains(planned, want) {
			t.Errorf("the plan does not say %q:\n%s", want, planned)
		}
	}

	confirmed := confirmWhenSyncing(conn)

	ran := make(chan string, 1)

	go func() {
		ran <- migrUp(t, config)
	}()

	if err := <-confirmed; err != nil {
		t.Fatalf("confirming the cutover: %v", err)
	}

	select {
	case out := <-ran:
		if !strings.Contains(out, "cut over through a shadow table") {
			t.Errorf("up does not say it cut over:\n%s", out)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("migr up did not cut over within two minutes of the confirmation")
	}

	status := migrOut(t, config, "status")
	if state := stateOf(t, status, "index_e2e_shadow_people"); state != "applied" {
		t.Errorf("the shadow-table migration is %q after its cutover, want applied:\n%s", state, status)
	}
}

// migrUp runs migr up and returns what it printed, from a goroutine the test does not fail from.
func migrUp(t *testing.T, config string) string {
	r, w, err := os.Pipe()
	if err != nil {
		return "opening a pipe: " + err.Error()
	}

	captured := make(chan string, 1)

	go func() {
		out, _ := io.ReadAll(r)
		captured <- string(out)
	}()

	stdout := os.Stdout
	os.Stdout = w
	runErr := migr(t, config, "up")
	os.Stdout = stdout
	_ = w.Close()

	out := <-captured
	if runErr != nil {
		return "migr up failed: " + runErr.Error() + "\n" + out
	}

	return out
}
