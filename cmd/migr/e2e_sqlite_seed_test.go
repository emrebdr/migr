package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/schema"
)

// Seeds against a real catalog, untagged for the reason the rest of this engine's suite is.
//
// The fake driver proves the command's shape; what only a real database can prove is that the seed
// table lives beside the migration history without becoming part of the schema. A snapshot that
// held it would have every migr diff propose dropping the seed history, so the Go project below
// seeds a database and then asks migr diff whether anything is out of place.

// seedEnvironments declares the environments a seeds directory may hold, beside the one database.
const seedEnvironments = "seeds:\n  environments: [default, production]\n"

// insertSeed is a .sql seed adding one row to the table the .sql project creates.
func insertSeed(email string) string {
	return "-- migr:up\nINSERT INTO e2e_users (email) VALUES ('" + email + "');\n\n" +
		"-- migr:down\nDELETE FROM e2e_users WHERE email = '" + email + "';\n"
}

// stageSeeds writes the seeds block into a project's migr.yaml and one seed per directory given.
func stageSeeds(t *testing.T, config string, seeds map[string]string) {
	t.Helper()

	f, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("opening migr.yaml: %v", err)
	}

	if _, err := f.WriteString(seedEnvironments); err != nil {
		t.Fatalf("declaring the environments: %v", err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("closing migr.yaml: %v", err)
	}

	for path, body := range seeds {
		full := filepath.Join(filepath.Dir(config), "seeds", path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("staging %s: %v", filepath.Dir(full), err)
		}

		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("staging %s: %v", path, err)
		}
	}
}

// sqliteSeeded returns the version of every seed the seed history holds, through the store contract.
func sqliteSeeded(t *testing.T, database, table string) []string {
	t.Helper()

	runs, err := sqliteOpened(t, database, table).Seeds().List(context.WithoutCancel(t.Context()))
	if err != nil {
		t.Fatalf("reading the seed history: %v", err)
	}

	var versions []string

	for _, rec := range runs {
		if rec.Outcome == history.Applied {
			versions = append(versions, rec.Version)
		}
	}

	return versions
}

func TestSeedsRunAfterEveryMigrationAndInTheirOwnHistory(t *testing.T) {
	config, database := sqliteProject(t)
	stageSeeds(t, config, map[string]string{
		"20260929T130000_root.sql":           insertSeed("root@example.com"),
		"default/20260929T130100_demo.sql":   insertSeed("demo@example.com"),
		"production/20260929T130200_ops.sql": insertSeed("ops@example.com"),
	})

	if out := migrFailure(t, config, "seed"); !strings.Contains(out, "migr up") {
		t.Errorf("seeding before migr up failed without saying to run it:\n%s", out)
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	before := sqliteDigest(t, database)

	out := migrOut(t, config, "seed")
	for _, want := range []string{"seeded 2 seeds", "20260929T130000_root.sql",
		"20260929T130100_demo.sql"} {
		if !strings.Contains(out, want) {
			t.Errorf("migr seed did not report %q:\n%s", want, out)
		}
	}

	want := []string{"20260929T130000", "20260929T130100"}
	if got := sqliteSeeded(t, database, sqliteHistoryTable); !slices.Equal(got, want) {
		t.Errorf("the seed history holds %v, want %v and nothing of production's", got, want)
	}

	if runs := sqliteRecorded(t, database); sqliteApplied(runs, "up") != len(sqliteMigrations) {
		t.Errorf("the migration history holds %d applied runs after seeding, want %d",
			sqliteApplied(runs, "up"), len(sqliteMigrations))
	}

	if after := sqliteDigest(t, database); after != before {
		t.Errorf("seeding changed the schema the snapshot reads:\n%s",
			schema.Difference(before, after))
		t.Log("A seed writes rows, and its history table is migr's own: neither is schema.")
	}

	if again := migrOut(t, config, "seed"); !strings.Contains(again, "nothing to seed") {
		t.Errorf("a second migr seed did not say it had nothing to do:\n%s", again)
	}
}

// goSeed is a Go seed that writes a row through a raw operation, which is data rather than schema.
const goSeed = `package seeds

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(RootAccount{}) }

// RootAccount adds the account every environment starts with.
type RootAccount struct{}

// Up inserts the row.
func (RootAccount) Up(ctx context.Context, s *migr.Schema) error {
	return s.Raw("sqlite", "INSERT INTO rebuilt_accounts (id, email) VALUES (1, 'root@example.com')").
		Apply(ctx)
}

// Down deletes it again.
func (RootAccount) Down(ctx context.Context, s *migr.Schema) error {
	return s.Raw("sqlite", "DELETE FROM rebuilt_accounts WHERE id = 1").Apply(ctx)
}
`

func TestAGoSeedRunsBesideGoMigrationsAndMigrDiffSeesNoTraceOfIt(t *testing.T) {
	config, database := sqliteGoProject(t)
	stageSeeds(t, config, map[string]string{"20260929T160000_root_account.go": goSeed})

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if err := migr(t, config, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if got := sqliteSeeded(t, database, sqliteGoHistoryTable); !slices.Equal(got,
		[]string{"20260929T160000"}) {
		t.Errorf("the seed history holds %v, want the one Go seed", got)
	}

	root := filepath.Dir(config)
	for _, built := range []string{"migrations", "seeds"} {
		if _, err := os.Stat(filepath.Join(root, ".migr", "go", built, "runner")); err != nil {
			t.Errorf("no runner was built for %s/: %v", built, err)
			t.Log("Two Go packages in one module each need a runner of their own, or every")
			t.Log("alternate migr up and migr seed rebuilds the other one's.")
		}
	}

	out := migrOut(t, config, "diff", "after_seeding")
	if !strings.Contains(out, "nothing to write") {
		t.Errorf("migr diff over a seeded database proposed something:\n%s", out)
		t.Log("Introspect leaves out both of migr's own tables. Were the seed history in the")
		t.Log("snapshot, every diff would propose dropping it.")
	}

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("a migr up after seeding: %v", err)
	}
}
