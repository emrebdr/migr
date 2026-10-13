//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/cli"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/postgres"
	"github.com/emrebdr/migr/internal/driver/postgres/pgtest"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/schema"
)

// Seeds against PostgreSQL, the engine whose migration lock is keyed by the history table's name.
//
// SQLite takes one lock per database file whatever the tables are called, so only here can a seed
// table that keyed a lock of its own be observed: a seed run would then proceed while migr up held
// the lock it believes excludes every other run. Nothing in a migration can undo a seed yet (that is
// M12-T5), so the seed table is dropped through pgtest.Drop -- the one place invariant #3 leaves for
// SQL a test needs to compose.

// seedTable keeps this suite's seed runs out of every other suite's.
const seedTable = "migr_e2e_seeds"

// seedingProject is the suite's project with a short lock wait, one seed, and its own seed table.
func seedingProject(t *testing.T) string {
	t.Helper()

	config := project(t)

	f, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("opening migr.yaml: %v", err)
	}

	if _, err := f.WriteString("lock:\n  timeout: 1s\nseeds:\n  table: " + seedTable + "\n"); err != nil {
		t.Fatalf("staging the seeds block: %v", err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("closing migr.yaml: %v", err)
	}

	seeds := filepath.Join(filepath.Dir(config), "seeds")
	if err := os.MkdirAll(seeds, 0o750); err != nil {
		t.Fatalf("staging the seeds directory: %v", err)
	}

	body := "-- migr:up\nINSERT INTO e2e_users (id) VALUES (1);\n\n" +
		"-- migr:down\nDELETE FROM e2e_users WHERE id = 1;\n"
	if err := os.WriteFile(filepath.Join(seeds, "20260727T160000_root.sql"), []byte(body),
		0o600); err != nil {
		t.Fatalf("staging the seed: %v", err)
	}

	return config
}

// seedSession dials the server as this suite's migr.yaml does, so both of its tables are migr's own.
func seedSession(t *testing.T) driver.Conn {
	t.Helper()

	d, err := postgres.New(driver.Options{HistoryTable: historyTable, SeedTable: seedTable})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conn, err := d.Open(t.Context(), dsn())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })

	return conn
}

// seededDigest renders the target schema as a session knowing both of migr's tables reads it.
func seededDigest(t *testing.T) string {
	t.Helper()

	held, err := seedSession(t).Introspect(t.Context())
	if err != nil {
		t.Fatalf("introspecting the schema: %v", err)
	}

	return schema.Render(*held)
}

// seededVersions returns what the seed table records as applied, through the store contract alone.
func seededVersions(t *testing.T) []string {
	t.Helper()

	runs, err := seedSession(t).Seeds().List(t.Context())
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

func TestASeedRunWaitsOnTheLockMigrUpTakesAgainstPostgreSQL(t *testing.T) {
	config := seedingProject(t)
	outliving := context.WithoutCancel(t.Context())

	if err := migr(t, config, "down"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	if err := pgtest.Drop(outliving, openedWith(outliving, t, ""), targetSchema, seedTable); err != nil {
		t.Fatalf("clearing an earlier run's seed history: %v", err)
	}

	t.Cleanup(func() {
		if err := pgtest.Drop(outliving, openedWith(outliving, t, ""), targetSchema,
			seedTable); err != nil {
			t.Errorf("dropping the seed history: %v", err)
		}

		if err := migr(t, config, "down"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}
	})

	if err := migr(t, config, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	holder := opened(t, historyTable)

	unlock, err := holder.Lock(t.Context())
	if err != nil {
		t.Fatalf("taking the migration lock: %v", err)
	}

	if err := migr(t, config, "seed"); cli.ExitCode(err) != cli.ExitLock {
		t.Errorf("migr seed beside a held migration lock returned %v (exit %d), want exit %d",
			err, cli.ExitCode(err), cli.ExitLock)
		t.Log("The advisory key is derived from the history table's name, so a seed run keyed on")
		t.Log("its own table would take a lock nothing else holds and run beside a migr up.")
	}

	if err := unlock(outliving); err != nil {
		t.Fatalf("releasing the migration lock: %v", err)
	}

	before := seededDigest(t)

	if err := migr(t, config, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if got := seededVersions(t); !slices.Equal(got, []string{"20260727T160000"}) {
		t.Errorf("the seed history holds %v, want the one seed", got)
	}

	if after := seededDigest(t); after != before {
		t.Errorf("seeding changed what the snapshot reads, so a diff would act on it:\n%s",
			schema.Difference(before, after))
	}
}
