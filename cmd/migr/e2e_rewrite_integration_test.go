//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/schema"
)

// saferName is a third Go migration, changing a table the first one created and so rewritten.
const saferName = "20260727T160200_constrain_e2e_go_accounts.go"

// saferBody adds a check and an index to a table that already exists, both of which PostgreSQL rewrites.
const saferBody = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(ConstrainAccounts{}) }

// ConstrainAccounts adds a check and an index to a table an earlier migration created.
type ConstrainAccounts struct{}

// Up adds both, each of which this engine performs as a safer sequence than declared.
func (m ConstrainAccounts) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("e2e_go_accounts").
		AddCheck("e2e_go_accounts_positive", "id > 0").
		AddIndex("idx_e2e_go_accounts_id_email", "id", "email").
		Apply(ctx)
}

// Down drops both, recording what each held.
func (m ConstrainAccounts) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("e2e_go_accounts").
		DropIndex("idx_e2e_go_accounts_id_email").Was(migr.Columns("id", "email")).
		DropCheck("e2e_go_accounts_positive").Was(migr.CheckDef{Expression: "id > 0"}).
		Apply(ctx)
}
`

func TestRewritesAreShownInThePlanAndRoundTripAgainstPostgreSQL(t *testing.T) {
	config := goProject(t)

	path := filepath.Join(filepath.Dir(config), "migrations", saferName)
	if err := os.WriteFile(path, []byte(saferBody), 0o600); err != nil {
		t.Fatalf("staging %s: %v", saferName, err)
	}

	if err := migr(t, config, "down", "--step", "3"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	before := digest(t, goHistoryTable)

	t.Cleanup(func() {
		if err := migr(t, config, "down", "--step", "3"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	plan := migrOut(t, config, "plan")
	for _, want := range []string{
		"rewritten (safety.rewrites.not_valid): 1 operation, 2 statements instead of 1",
		"rewritten (safety.rewrites.concurrent_index)",
		"NOT VALID", "VALIDATE CONSTRAINT", "CREATE INDEX CONCURRENTLY",
		"safety.rewrites.not_valid: off, safety.rewrites.concurrent_index: off",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("migr plan does not show %q:\n%s", want, plan)
		}
	}

	if strings.Count(plan, "rewritten (") != 2 {
		t.Errorf("the plan rewrote other than the two operations on an existing table:\n%s", plan)
		t.Log("The first migration creates the table and indexes it, and nothing can be waiting on a")
		t.Log("table that did not exist, so its add_index runs as declared and keeps it atomic.")
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	applied := digest(t, goHistoryTable)
	for _, want := range []string{"e2e_go_accounts_positive", "idx_e2e_go_accounts_id_email"} {
		if !strings.Contains(applied, want) {
			t.Errorf("the schema does not hold %q after up:\n%s", want, applied)
		}
	}

	if err := migr(t, config, "down", "--step", "3"); err != nil {
		t.Fatalf("down: %v", err)
	}

	if got := digest(t, goHistoryTable); got != before {
		t.Errorf("the schema did not return to what it was:\n%s", schema.Difference(before, got))
	}
}
