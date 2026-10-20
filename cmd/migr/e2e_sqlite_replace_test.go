package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An expand/contract column replacement walked end to end on SQLite, one phase per migr up, then
// rolled back whole. Untagged, because this engine needs no daemon.
//
// What the rows hold is asked by migrations rather than by this file, which composes no SQL above a
// driver package (invariant #3): the check migration reads them through host.db.query, operator SQL
// as every fixture's is, and fails unless every row kept its value under the column's own name.

// replacedRows is how many rows the replaced column holds.
const replacedRows = "200"

// replacing is the declaration every phase file restates, as an author would.
const replacing = `migr.ReplaceColumn("accounts", "email", migr.String(255)).With(migr.String(320))`

// createAccounts creates the table whose column is replaced.
const createAccounts = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(CreateAccounts{}) }

// CreateAccounts creates the table.
type CreateAccounts struct{}

// Up creates it.
func (CreateAccounts) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("accounts").
		Column("id", migr.Int64()).
		Column("email", migr.String(255)).
		PrimaryKey("id").
		Apply(ctx)
}

// Down drops it.
func (CreateAccounts) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("accounts").Drop().Apply(ctx)
}
`

// fillAccounts inserts the rows, beside an index so the migration has an operation to run.
const fillAccounts = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(FillAccounts{}) }

// FillAccounts inserts the rows the replacement carries over.
type FillAccounts struct{}

// Up inserts them, mixed case, so a value that was rewritten would read differently.
func (FillAccounts) Up(ctx context.Context, s *migr.Schema) error {
	if _, err := s.Exec(ctx, "WITH RECURSIVE g(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM g "+
		"WHERE i < ` + replacedRows + `) INSERT INTO accounts (id, email) "+
		"SELECT i, 'User' || i || '@Example.COM' FROM g"); err != nil {
		return err
	}

	return s.Table("accounts").AddIndex("accounts_id_idx", "id").Apply(ctx)
}

// Irreversible says why.
func (FillAccounts) Irreversible() string { return "the rows it inserted are not told apart" }
`

// phaseFile is one phase of the replacement, which is the whole of its migration file.
func phaseFile(phase string) string {
	return `package migrations

import migr "github.com/emrebdr/migr/sdk/go"

func init() { migr.Register(` + replacing + `.` + phase + `()) }
`
}

// checkAccounts fails unless every row kept its value under the column's own name.
const checkAccounts = `package migrations

import (
	"context"
	"fmt"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(CheckAccounts{}) }

// CheckAccounts asserts what the replacement left, then indexes the column.
type CheckAccounts struct{}

// Up fails unless every row holds its own address, unchanged, in email.
func (CheckAccounts) Up(ctx context.Context, s *migr.Schema) error {
	rows, err := s.Query(ctx, "SELECT count(*) FROM accounts WHERE email = 'User' || id || '@Example.COM'")
	if err != nil {
		return err
	}

	if got := *rows.Values[0][0]; got != "` + replacedRows + `" {
		return fmt.Errorf("%s rows hold their own address after the replacement", got)
	}

	return s.Table("accounts").AddIndex("accounts_email_idx", "email").Apply(ctx)
}

// Down drops the index.
func (CheckAccounts) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("accounts").DropIndex("accounts_email_idx").Was(migr.Columns("email")).Apply(ctx)
}
`

// writeReplacement stages the replacement's migrations, over a table of the given name.
func writeReplacement(t *testing.T, dir, table string) {
	t.Helper()

	for name, body := range map[string]string{
		"20261001T090000_create_accounts.go": createAccounts,
		"20261001T090100_fill_accounts.go":   fillAccounts,
		"20261001T090200_expand_email.go":    phaseFile("Expand"),
		"20261001T090300_backfill_email.go":  phaseFile("Backfill"),
		"20261001T090400_swap_email.go":      phaseFile("Swap"),
		"20261001T090500_contract_email.go":  phaseFile("Contract"),
		"20261001T090600_check_accounts.go":  checkAccounts,
	} {
		body = strings.ReplaceAll(body, "accounts", table)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", name, err)
		}
	}
}

// replaceProject is a Go project on SQLite whose migrations replace accounts.email in four phases.
func replaceProject(t *testing.T) string {
	t.Helper()

	config, _ := sqliteGoProject(t)
	dir := filepath.Join(filepath.Dir(config), "migrations")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the staged migrations: %v", err)
	}

	for _, entry := range entries {
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			t.Fatalf("clearing %s: %v", entry.Name(), err)
		}
	}

	writeReplacement(t, dir, "accounts")

	appendConfig(t, config, "safety:\n  chunk_rows: 50\n  chunk_pause: 1ms\n"+
		"validation:\n  allow_destructive: [data_transform]\n")

	return config
}

// permit replaces the kinds the project permits to destroy, as an operator editing migr.yaml would.
func permit(t *testing.T, config, kinds string) {
	t.Helper()

	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatalf("reading migr.yaml: %v", err)
	}

	edited := strings.Replace(string(body), "allow_destructive: [data_transform]",
		"allow_destructive: ["+kinds+"]", 1)
	if err := os.WriteFile(config, []byte(edited), 0o600); err != nil {
		t.Fatalf("writing migr.yaml: %v", err)
	}
}

// migrBoth runs the CLI and returns what it printed on each stream, failing the test on an error.
func migrBoth(t *testing.T, config string, args ...string) (stdout, stderr string) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("opening a pipe: %v", err)
	}

	logged := drained(t, r, args[0])

	saved := os.Stderr
	os.Stderr = w
	stdout = migrOut(t, config, args...)
	os.Stderr = saved

	if err := w.Close(); err != nil {
		t.Fatalf("closing the pipe: %v", err)
	}

	return stdout, <-logged
}

// undone returns the migrations a migr down printed as rolled back, in the order it undid them.
func undone(out string) []string {
	var names []string

	for line := range strings.Lines(out) {
		for _, field := range strings.Fields(line) {
			if strings.HasSuffix(field, ".go") {
				names = append(names, field)
			}
		}
	}

	return names
}

func TestAColumnReplacementWalksItsFourPhasesAndRollsBackWhole(t *testing.T) {
	config := replaceProject(t)
	database := filepath.Join(filepath.Dir(config), "app.db")

	if err := migr(t, config, "up", "--step", "2"); err != nil {
		t.Fatalf("creating and filling the table: %v", err)
	}

	before := sqliteDigestOf(t, database, sqliteGoHistoryTable)

	if _, logged := migrBoth(t, config, "plan", "--step", "1"); strings.Contains(logged, "unsafe step") {
		t.Errorf("expand's plan warned, and adding a nullable column is safe:\n%s", logged)
	}

	if err := migr(t, config, "up", "--step", "1", "--release", "r1"); err != nil {
		t.Fatalf("expand: %v", err)
	}

	if held := migrOut(t, config, "up", "--release", "r1"); !strings.Contains(held,
		"held: 20261001T090300_backfill_email.go waits for a later release") ||
		!strings.Contains(held, `belongs to release "r1", which is this one`) {
		t.Errorf("the backfill ran in the deploy that expanded, or the run did not say why not:\n%s", held)
	}

	planned := migrOut(t, config, "plan", "--step", "1", "--release", "r2")
	if !strings.Contains(planned, `chunks: 50 rows at a time by "accounts" (id)`) {
		t.Errorf("the backfill does not run in chunks:\n%s", planned)
	}

	backfilled := migrOut(t, config, "up", "--step", "1", "--release", "r2")
	if !strings.Contains(backfilled, "5 chunks, 200 rows written") {
		t.Errorf("the backfill did not report its chunks:\n%s", backfilled)
	}

	if _, logged := migrBoth(t, config, "plan", "--step", "1", "--release", "r3"); !strings.Contains(logged,
		`unsafe step: swap: \"accounts\".\"email\" now names the new column`) {
		t.Errorf("swap's plan does not warn that it is unsafe:\n%s", logged)
	}

	if err := migr(t, config, "up", "--step", "1", "--release", "r3"); err != nil {
		t.Fatalf("swap: %v", err)
	}

	if planned := migrOut(t, config, "plan", "--down", "--step", "2"); !strings.Contains(planned,
		"nothing to undo -- its down emits no operations") {
		t.Errorf("the plan of the backfill's down does not say it undoes nothing:\n%s", planned)
	}

	if dry := migrOut(t, config, "down", "--step", "2", "--dry-run"); !strings.Contains(dry,
		"20261001T090300_backfill_email.go  0 statements") {
		t.Errorf("the dry run does not count the backfill's down as no statements:\n%s", dry)
	}

	if refused := migrFailure(t, config, "up", "--step", "1", "--release", "r4"); !strings.Contains(refused,
		"drop_column") {
		t.Errorf("contract ran without its drop being permitted:\n%s", refused)
	}

	permit(t, config, "data_transform, drop_column")

	if _, logged := migrBoth(t, config, "up", "--step", "1", "--release", "r4"); !strings.Contains(logged,
		"unsafe step: contract") {
		t.Errorf("contract ran without warning that it is unsafe:\n%s", logged)
	}

	if err := migr(t, config, "up", "--release", "r5"); err != nil {
		t.Fatalf("the check that every row kept its value under its own name: %v", err)
	}

	after := sqliteDigestOf(t, database, sqliteGoHistoryTable)
	if strings.Contains(after, "email_old") || strings.Contains(after, "email_next") ||
		!strings.Contains(after, "string(320)") {
		t.Errorf("the replacement left a schema that is not the one it declared:\n%s", after)
	}

	if report, err := migrReport(t, config, "validate"); err != nil {
		t.Errorf("migr validate refused the project it just ran: %v\n%s", err, report)
	}

	rolledBack := migrOut(t, config, "down", "--step", "5")
	if got := undone(rolledBack); len(got) != 5 || got[2] != "20261001T090400_swap_email.go" {
		t.Errorf("the rollback undid %v, want the check and the four phases, newest first:\n%s",
			got, rolledBack)
	}

	if !strings.Contains(rolledBack, "20261001T090300_backfill_email.go  0 statements") {
		t.Errorf("the backfill's down was not recorded as undoing nothing:\n%s", rolledBack)
	}

	if restored := sqliteDigestOf(t, database, sqliteGoHistoryTable); restored != before {
		t.Errorf("the rollback did not restore the schema the replacement started from\nbefore\n%s\n"+
			"after\n%s", before, restored)
	}

	for _, release := range []string{"r6", "r7", "r8", "r9"} {
		if err := migr(t, config, "up", "--release", release); err != nil {
			t.Fatalf("running the replacement and its check again under %s: %v", release, err)
		}
	}

	if status := migrOut(t, config, "status"); strings.Contains(status, "pending") {
		t.Errorf("four deploys did not carry the replacement and its check through:\n%s", status)
	}

	t.Log("The second check passing is what says the rollback restored the values too: contract's")
	t.Log("down copied them back into the old column, swap's down made it NOT NULL again -- which")
	t.Log("would have refused a single row left NULL -- and the replacement then carried them over")
	t.Log("a second time, every row still holding its own address.")
}
