package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/schema"
	"github.com/emrebdr/migr/internal/source/golang"
	"github.com/emrebdr/migr/internal/source/sql"
)

// seedsDir is where these tests stage seeds, which is the default seeds.dir beside migr.yaml.
const seedsDir = "seeds"

// seedingConfig is the project config declaring the two environments a seeds directory may hold.
var seedingConfig = projectConfig + "seeds:\n  environments: [default, production]\n"

// seedAt returns the version the nth staged seed carries, apart from every staged migration's.
func seedAt(n int) string {
	return versionAt(n + 30)
}

// seedFile returns the filename the nth staged seed is stored under.
func seedFile(n int, name string) string {
	return seedAt(n) + "_" + name + sql.Ext
}

// inserting is a seed that adds one row and removes it again.
func inserting(name string) stagedMigration {
	return stagedMigration{name: name, up: "INSERT INTO users (id) VALUES (1); -- " + name,
		down: "DELETE FROM users; -- " + name}
}

// stageSeed writes one seed into a directory under seeds/, which is "" for the common one.
func stageSeed(t *testing.T, dir, env, filename, body string) {
	t.Helper()

	at := filepath.Join(dir, seedsDir, env)
	if err := os.MkdirAll(at, 0o750); err != nil {
		t.Fatalf("staging %s: %v", at, err)
	}

	write(t, filepath.Join(at, filename), body)
}

// seededProject stages the two-table project, one common seed, one of its own, and one of production's.
func seededProject(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)
	write(t, filepath.Join(dir, config.DefaultFilename), seedingConfig)

	stageSeed(t, dir, "", seedFile(0, "countries"), inserting("countries").body())
	stageSeed(t, dir, "default", seedFile(1, "demo_users"), inserting("demo_users").body())
	stageSeed(t, dir, "production", seedFile(2, "admin"), inserting("admin").body())

	return dir, f, drivers
}

// migrated runs migr up, which is what every seed run needs to have happened first.
func migrated(t *testing.T, dir string, drivers *driver.Registry) {
	t.Helper()

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}
}

// seeded returns the versions a fake database holds a completed seed run for, in the order they ran.
func seeded(f *fake) []string {
	var versions []string

	for _, rec := range f.seedRuns {
		if rec.Outcome == history.Applied {
			versions = append(versions, rec.Version)
		}
	}

	return versions
}

func TestSeedRunsTheCommonSeedsAndThisEnvironmentsOwnAndRecordsThemApart(t *testing.T) {
	dir, f, drivers := seededProject(t)
	migrated(t, dir, drivers)

	stdout, _, err := runWith(t, dir, noon, drivers, "seed")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	if got := seeded(f); !slices.Equal(got, []string{seedAt(0), seedAt(1)}) {
		t.Errorf("the seed history holds %q, want the common seed and this environment's own", got)
		t.Log("seeds/production/ belongs to another environment, so running it here would put")
		t.Log("production's rows in a development database.")
	}

	if got := applied(f); !slices.Equal(got, []string{versionAt(0), versionAt(1)}) {
		t.Errorf("the migration history holds %q, want the two migrations and no seed", got)
	}

	for _, statement := range f.statements {
		if strings.Contains(statement, "admin") {
			t.Errorf("another environment's seed ran: %q", statement)
		}
	}

	for _, want := range []string{`environment "default"`, seedFile(0, "countries"),
		seedFile(1, "demo_users"), "seeded 2 seeds"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the summary does not mention %q:\n%s", want, stdout)
		}
	}
}

func TestASeedRunTakesTheMigrationLockAndRecordsInsideItsTransaction(t *testing.T) {
	dir, f, drivers := seededProject(t)
	migrated(t, dir, drivers)

	f.calls = nil

	if _, _, err := runWith(t, dir, noon, drivers, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	want := []string{"open", "list", "lock", "seed-ensure", "seed-list", "seed-latest",
		"introspect", "begin", "seed-record", "commit", "introspect", "begin", "seed-record",
		"commit", "unlock", "close"}

	if !slices.Equal(f.calls, want) {
		t.Errorf("the session was asked for %q, want %q", f.calls, want)
		t.Log("A seed is recorded in the seed table, inside the transaction that ran it, under the")
		t.Log("one migration lock -- a lock of its own would let a seed run beside a migr up.")
	}
}

func TestASecondSeedRunHasNothingToDo(t *testing.T) {
	dir, f, drivers := seededProject(t)
	migrated(t, dir, drivers)

	if _, _, err := runWith(t, dir, noon, drivers, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ran := len(f.statements)

	stdout, _, err := runWith(t, dir, noon, drivers, "seed")
	if err != nil {
		t.Fatalf("a second seed: %v", err)
	}

	if len(f.statements) != ran {
		t.Errorf("a second seed ran %q", f.statements[ran:])
	}

	if !strings.Contains(stdout, "nothing to seed") {
		t.Errorf("a second seed did not say it had nothing to do:\n%s", stdout)
	}
}

func TestASeedDryRunExecutesAndRecordsNothing(t *testing.T) {
	dir, f, drivers := seededProject(t)
	migrated(t, dir, drivers)

	ran := len(f.statements)

	stdout, _, err := runWith(t, dir, noon, drivers, "seed", "--dry-run")
	if err != nil {
		t.Fatalf("seed --dry-run: %v", err)
	}

	if len(f.statements) != ran || len(f.seedRuns) != 0 || f.asked("seed-ensure") {
		t.Errorf("a dry run touched the database: %q", f.calls)
	}

	for _, want := range []string{"dry run", "no seed history table", "would seed 2 seeds"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the preview does not mention %q:\n%s", want, stdout)
		}
	}
}

func TestSeedRefusesADatabaseTheMigrationsHaveNotAllRunAgainst(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string, drivers *driver.Registry)
		want  string
	}{
		{name: "none has run", setup: func(*testing.T, string, *driver.Registry) {},
			want: "migr up"},
		{name: "one is pending", setup: func(t *testing.T, dir string, drivers *driver.Registry) {
			t.Helper()

			if _, _, err := runWith(t, dir, noon, drivers, "up", "--step", "1"); err != nil {
				t.Fatalf("up --step 1: %v", err)
			}
		}, want: fileAt(1, "add_teams")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, f, drivers := seededProject(t)
			tc.setup(t, dir, drivers)

			_, _, err := runWith(t, dir, noon, drivers, "seed")
			if err == nil || ExitCode(err) != ExitRuntime {
				t.Fatalf("seed returned %v (exit %d), want exit %d", err, ExitCode(err), ExitRuntime)
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not name %q: %v", tc.want, err)
			}

			if len(f.seedRuns) != 0 || f.asked("seed-ensure") {
				t.Errorf("a refused seed run touched the seed history: %q", f.calls)
				t.Log("A seed runs against the schema every migration leaves, never between two of")
				t.Log("them -- which is what keeps seeds out of the migrations' ordering entirely.")
			}
		})
	}
}

func TestSeedRefusesADatabaseLeftBetweenStates(t *testing.T) {
	dir, f, drivers := seededProject(t)
	migrated(t, dir, drivers)

	f.runs[len(f.runs)-1].Outcome = history.Dirty
	f.runs[len(f.runs)-1].LastStatement = 1
	f.runs[len(f.runs)-1].Error = "stopped"

	_, _, err := runWith(t, dir, noon, drivers, "seed")
	if err == nil || !strings.Contains(err.Error(), "migr resolve") {
		t.Fatalf("seed over a dirty migration returned %v, want a refusal naming migr resolve", err)
	}
}

func TestASubdirectoryNoEnvironmentRunsIsRefused(t *testing.T) {
	dir, f, drivers := seededProject(t)
	migrated(t, dir, drivers)
	stageSeed(t, dir, "prodution", seedFile(3, "typo"), inserting("typo").body())

	_, _, err := runWith(t, dir, noon, drivers, "seed")
	if ExitCode(err) != ExitValidation || !strings.Contains(err.Error(), "prodution") {
		t.Fatalf("seed returned %v (exit %d), want exit 2 naming the directory", err, ExitCode(err))
	}

	if !strings.Contains(err.Error(), "environments: default, production") {
		t.Errorf("the refusal does not list the environments a directory may be for: %v", err)
		t.Log("The fix is a rename, and the operator needs the names it could be renamed to.")
	}

	if len(f.seedRuns) != 0 {
		t.Errorf("seeds ran beside a directory no environment runs: %q", seeded(f))
		t.Log("A misspelled environment directory holds seeds nothing would ever run, and the")
		t.Log("project would find that out in the environment it meant -- §9's argument against")
		t.Log("skipping a hyphenated filename, applied to a directory.")
	}
}

func TestTwoSeedsClaimingOneVersionAcrossTheTwoDirectoriesAreRefused(t *testing.T) {
	dir, _, drivers := seededProject(t)
	migrated(t, dir, drivers)
	stageSeed(t, dir, "default", seedFile(0, "countries_again"), inserting("again").body())

	_, _, err := runWith(t, dir, noon, drivers, "seed")
	if ExitCode(err) != ExitValidation || !errors.Is(err, migration.ErrDuplicateVersion) {
		t.Fatalf("seed returned %v (exit %d), want exit 2 for a duplicate version", err,
			ExitCode(err))
	}
}

func TestASeedMayShareAVersionWithAMigration(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	stageSeed(t, dir, "", fileAt(0, "countries"), inserting("countries").body())
	migrated(t, dir, drivers)

	if _, _, err := runWith(t, dir, noon, drivers, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if got := seeded(f); !slices.Equal(got, []string{versionAt(0)}) {
		t.Errorf("the seed history holds %q, want the seed whose version a migration also has", got)
		t.Log("Seeds and migrations are two histories in two tables, so a version names one")
		t.Log("thing in each and nothing reconciles the one against the other.")
	}
}

func TestNoOtherCommandSeesASeedOrItsHistory(t *testing.T) {
	dir, f, drivers := seededProject(t)
	migrated(t, dir, drivers)

	if _, _, err := runWith(t, dir, noon, drivers, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, args := range [][]string{{"status"}, {"history"}, {"up"}, {"plan"}} {
		stdout, _, err := runWith(t, dir, noon, drivers, args...)
		if err != nil {
			t.Fatalf("%s: %v", args[0], err)
		}

		for _, seed := range []string{seedAt(0), seedAt(1)} {
			if strings.Contains(stdout, seed) {
				t.Errorf("migr %s reports the seed %s:\n%s", args[0], seed, stdout)
			}
		}
	}

	if got := applied(f); len(got) != 2 {
		t.Errorf("the migration history holds %q after a seed run, want the two migrations", got)
	}
}

func TestASeedThatChangesTheSchemaIsRefused(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	stageSeed(t, dir, "", seedAt(0)+"_tables"+golang.Ext, "package seeds\n")
	migrated(t, dir, drivers)

	_, _, err := runScripted(t, dir, actTypes, drivers, "seed")
	if ExitCode(err) != ExitValidation || !errors.Is(err, errSchemaInSeed) {
		t.Fatalf("seed returned %v (exit %d), want exit 2 for a schema change", err, ExitCode(err))
	}

	if len(f.seedRuns) != 0 {
		t.Errorf("a seed that changes the schema was recorded: %q", seeded(f))
		t.Log("A table a seed creates is one no migration knows about, which migr diff would then")
		t.Log("propose dropping -- the schema is the migrations', and only theirs.")
	}
}

func TestARuntimeSeedRunsWhereItIsData(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	write(t, filepath.Join(dir, config.DefaultFilename), seedingConfig)
	stageSeed(t, dir, "default", seedAt(0)+"_rows"+golang.Ext, "package seeds\n")
	migrated(t, dir, drivers)

	if _, _, err := runScripted(t, dir, actEmits, drivers, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if got := seeded(f); !slices.Equal(got, []string{seedAt(0)}) {
		t.Errorf("the seed history holds %q, want the one Go seed", got)
		t.Log("The runtime is started on the environment's own directory, so a seed written in a")
		t.Log("language only reaches the engine if that directory is the one it was told.")
	}
}

func TestASeedMayWriteDataThroughATypedOperation(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.live = keyedUsers()
	stageSeed(t, dir, "", seedAt(0)+"_plans"+golang.Ext, "package seeds\n")
	migrated(t, dir, drivers)

	if _, _, err := runScripted(t, dir, actBackfills, drivers, "seed"); err != nil {
		t.Fatalf("a seed emitting data_backfill was refused: %v", err)
	}

	if got := seeded(f); !slices.Equal(got, []string{seedAt(0)}) {
		t.Errorf("the seed history holds %q, want the one seed", got)
		t.Log("M12-T1 permitted raw alone because nothing else wrote data; the data operations")
		t.Log("are what a seed is for, and data_transform's destruction is the seed's own business.")
	}

	if len(f.chunks) != 1 || !f.asked("seed-clear-checkpoint") || f.asked("clear-checkpoint") {
		t.Errorf("a data-only seed ran %d chunks, and cleared the seed checkpoint %v and the "+
			"migration one %v", len(f.chunks), f.asked("seed-clear-checkpoint"),
			f.asked("clear-checkpoint"))
		t.Log("A data-only seed runs in chunks like a migration does, and its checkpoint lives")
		t.Log("beside the seed history -- a migration's checkpoint table is not the seeds' to touch.")
	}
}

// keyedUsers is a live schema holding the users table a scripted backfill writes, keyed by id.
func keyedUsers() *schema.Snapshot {
	return &schema.Snapshot{Tables: []schema.Table{{
		Name:       "users",
		Columns:    []schema.Column{{Name: "id"}, {Name: "plan", Nullable: true}},
		PrimaryKey: &schema.PrimaryKey{Columns: []string{"id"}},
	}}}
}

func TestASeedDeclaringAMigrationAsADependencyIsRefused(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	stageSeed(t, dir, "", seedAt(0)+"_rows"+golang.Ext, "package seeds\n")
	migrated(t, dir, drivers)

	_, _, err := runDeclaring(t, dir, actEmits, declaresFirst, drivers, "seed")
	if ExitCode(err) != ExitValidation || !strings.Contains(err.Error(), versionAt(0)) {
		t.Fatalf("seed returned %v (exit %d), want exit 2 naming the migration it declared",
			err, ExitCode(err))
	}

	if len(f.seedRuns) != 0 {
		t.Errorf("a seed ordered against a migration ran: %q", seeded(f))
		t.Log("Seeds are ordered among themselves alone, so a declared edge to a migration names")
		t.Log("a version that is not in the set being ordered.")
	}
}

func TestAProjectWithNoMigrationsAtAllMaySeed(t *testing.T) {
	dir, f, drivers := project(t)
	stageSeed(t, dir, "", seedFile(0, "countries"), inserting("countries").body())

	if _, _, err := runWith(t, dir, noon, drivers, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if got := seeded(f); !slices.Equal(got, []string{seedAt(0)}) {
		t.Errorf("the seed history holds %q, want the one seed", got)
		t.Log("No migration history table means no migration has run -- unless there are none to")
		t.Log("run, in which case the schema every migration leaves is the one already there.")
	}
}

func TestSeedingWithNoSeedsDirectoryFailsSayingWhere(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)
	migrated(t, dir, drivers)

	_, _, err := runWith(t, dir, noon, drivers, "seed")
	if ExitCode(err) != ExitRuntime || !errors.Is(err, errNoSeedsDir) {
		t.Fatalf("seed returned %v (exit %d), want exit 1 for a missing directory", err,
			ExitCode(err))
	}
}

// stagedSeedable is a seeded project whose migrations have all run, so a seed run has work to do.
func stagedSeedable(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := seededProject(t)
	migrated(t, dir, drivers)

	return dir, f, drivers
}

// stagedStraySeeds is a seedable project holding a directory no environment runs.
func stagedStraySeeds(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := stagedSeedable(t)
	stageSeed(t, dir, "prodution", seedFile(3, "typo"), inserting("typo").body())

	return dir, f, drivers
}

// stagedSeedLocked is a seedable project whose database will not hand over the migration lock.
func stagedSeedLocked(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := stagedSeedable(t)
	f.lockErr = driver.ErrLockTimeout

	return dir, f, drivers
}

// stagedSeedDrifted is a seedable project whose common seed was edited after it ran.
func stagedSeedDrifted(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := stagedSeedable(t)

	if _, _, err := runWith(t, dir, noon, drivers, "seed"); err != nil {
		t.Fatalf("staging a seed to edit: %v", err)
	}

	stageSeed(t, dir, "", seedFile(0, "countries"), inserting("countries_edited").body())

	return dir, f, drivers
}
