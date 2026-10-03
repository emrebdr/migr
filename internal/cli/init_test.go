package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// noon is the instant these tests create migrations at, in a zone two hours ahead of UTC.
var noon = time.Date(2026, 7, 27, 17, 30, 0, 0, time.FixedZone("CEST", 2*60*60))

// noonVersion is the version noon renders to, which is that same instant in UTC.
const noonVersion = "20260727T153000"

// tick is how far a staged clock advances per reading, so a run's duration is never zero.
const tick = 250 * time.Millisecond

// runIn executes the root command inside dir, with the clock fixed so a version is predictable.
func runIn(t *testing.T, dir string, at time.Time, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	return runWith(t, dir, at, nil, args...)
}

// runWith executes the root command against a registry of drivers, on a clock that starts at at.
func runWith(t *testing.T, dir string, at time.Time, drivers *driver.Registry,
	args ...string) (stdout, stderr string, err error) {
	t.Helper()

	return runOptions(t, dir, &options{clock: ticking(at), drivers: drivers,
		runtimes: runtimeRegistry(t, config.LanguageGo, config.LanguageTypeScript)}, args...)
}

// runOptions runs the command tree against options a test built itself, for the ones that vary a build.
func runOptions(t *testing.T, dir string, opts *options,
	args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(dir)

	var out, errOut bytes.Buffer

	cmd := rootCmd(opts)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err = execute(context.Background(), cmd, opts)

	return out.String(), errOut.String(), err
}

// ticking returns a clock starting at at and advancing one tick per reading, so durations are real.
func ticking(at time.Time) func() time.Time {
	return func() time.Time {
		now := at
		at = at.Add(tick)

		return now
	}
}

// initialized runs init in a fresh directory with a DSN in the environment, ready for create.
func initialized(t *testing.T) string {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/migr")

	dir := t.TempDir()

	if _, _, err := runIn(t, dir, noon, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	return dir
}

// exists reports whether a path is there, failing the test on any answer other than yes or no.
func exists(t *testing.T, path string) bool {
	t.Helper()

	_, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}

	return err == nil
}

func TestInitWritesAConfigTheLoaderAcceptsAndADirectoryItNames(t *testing.T) {
	dir := initialized(t)

	cfg, err := config.Load(filepath.Join(dir, config.DefaultFilename))
	if err != nil {
		t.Fatalf("the config init wrote does not load: %v", err)
	}

	if !exists(t, cfg.Migrations.Dir) {
		t.Errorf("migrations.dir resolves to %q, which init did not create", cfg.Migrations.Dir)
		t.Log("init anchors the directory beside the config it writes and config.anchor anchors")
		t.Log("it beside the config it reads. This is what holds those two rules to one answer;")
		t.Log("apart, they agree by luck until someone changes one of them.")
	}

	if cfg.Migrations.Language != config.LanguageSQL {
		t.Errorf("the scaffold names language %q, want the one M5 can actually run",
			cfg.Migrations.Language)
	}
}

func TestTheScaffoldNamesEveryKeyThatHasNoDefaultAndNothingThatHasOne(t *testing.T) {
	dir := initialized(t)

	written, err := os.ReadFile(filepath.Join(dir, config.DefaultFilename))
	if err != nil {
		t.Fatalf("reading the scaffold: %v", err)
	}

	for _, defaulted := range []string{"lock:", "safety:", "history:"} {
		if strings.Contains(string(written), defaulted) {
			t.Errorf("the scaffold writes %q, which applyDefaults already owns:\n%s",
				defaulted, written)
			t.Log("A scaffold restating a default is a second definition of it, frozen into every")
			t.Log("project ever created. `migr config` prints the resolved set instead.")
		}
	}
}

func TestTheOneDefaultedKeyTheScaffoldWritesIsADecisionRatherThanARestatement(t *testing.T) {
	dir := initialized(t)

	written, err := os.ReadFile(filepath.Join(dir, config.DefaultFilename))
	if err != nil {
		t.Fatalf("reading the scaffold: %v", err)
	}

	if !strings.Contains(string(written), "allow_destructive: ["+string(ir.KindRaw)+"]") {
		t.Errorf("the scaffold permits no destruction, so nothing it writes can run:\n%s", written)
		t.Log("The scaffold names language: sql, and every statement in a .sql migration reaches")
		t.Log("the database as a raw operation -- which migr cannot read and therefore refuses")
		t.Log("until a project permits it. A scaffolded project that cannot run its own first")
		t.Log("migration is the same failure as a dsn that requires a credential to write a file.")
	}

	for _, defaulted := range []string{"allow_destructive: []", "max_lock_impact"} {
		if strings.Contains(string(written), defaulted) {
			t.Errorf("the scaffold writes %q, which is a default rather than a decision:\n%s",
				defaulted, written)
		}
	}
}

func TestTheScaffoldReferencesAnEnvironmentVariableRatherThanHoldingADSN(t *testing.T) {
	dir := initialized(t)

	written, err := os.ReadFile(filepath.Join(dir, config.DefaultFilename))
	if err != nil {
		t.Fatalf("reading the scaffold: %v", err)
	}

	if !strings.Contains(string(written), "${DATABASE_URL") {
		t.Errorf("the scaffold's dsn is not an environment reference:\n%s", written)
		t.Log("A connection string carries a password, and this file is committed. Writing a")
		t.Log("literal one is how the first thing a new project commits is a credential.")
	}

	if strings.Contains(string(written), "@") {
		t.Errorf("the scaffold's dsn carries credentials:\n%s", written)
	}
}

func TestWritingAMigrationNeedsNoDatabaseCredential(t *testing.T) {
	withoutDSN(t)

	dir := t.TempDir()

	if _, _, err := runIn(t, dir, noon, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	if _, _, err := runIn(t, dir, noon, "create", "add_users"); err != nil {
		t.Errorf("create: %v", err)
		t.Log("Every command loads the whole config, and expansion refuses a reference with no")
		t.Log("value and no fallback -- so a scaffold naming a bare ${DATABASE_URL} makes writing")
		t.Log("a text file require a credential. The fallback is why `migr init && migr create`")
		t.Log("works in a directory where nobody has exported anything yet.")
	}
}

// withoutDSN removes the variable the scaffold refers to, restoring the environment afterwards.
func withoutDSN(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "")

	if err := os.Unsetenv("DATABASE_URL"); err != nil {
		t.Fatalf("unsetting DATABASE_URL: %v", err)
	}
}

func TestInitRefusesAnExistingConfigAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, config.DefaultFilename), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatalf("staging a config: %v", err)
	}

	_, _, err := runIn(t, dir, noon, "init")
	if err == nil {
		t.Fatal("init overwrote an existing config")
	}

	if !strings.Contains(err.Error(), config.DefaultFilename) {
		t.Errorf("the refusal does not name the file in the way: %v", err)
	}

	if got := ExitCode(err); got != ExitRuntime {
		t.Errorf("ExitCode = %d, want %d: the argument was fine, the world was not ready",
			got, ExitRuntime)
	}

	if exists(t, filepath.Join(dir, scaffoldDir)) {
		t.Error("a refused init created the migrations directory anyway")
		t.Log("The config is checked before anything is written, so a refusal leaves the project")
		t.Log("exactly as it found it rather than half-scaffolded.")
	}
}

func TestInitLeavesADirectoryThatIsAlreadyThereAlone(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/migr")

	dir := t.TempDir()
	existing := filepath.Join(dir, scaffoldDir)

	if err := os.MkdirAll(existing, 0o750); err != nil {
		t.Fatalf("staging a migrations directory: %v", err)
	}

	kept := filepath.Join(existing, "20260101T000000_first.sql")
	if err := os.WriteFile(kept, []byte(""), 0o600); err != nil {
		t.Fatalf("staging a migration: %v", err)
	}

	stdout, _, err := runIn(t, dir, noon, "init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	if !exists(t, kept) {
		t.Fatal("init emptied a migrations directory that was already there")
	}

	if !strings.Contains(stdout, "already exists") {
		t.Errorf("init reported creating a directory that was already there:\n%s", stdout)
	}
}

func TestInitReportsBothPathsAndTheStepStillOwed(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/migr")

	stdout, _, err := runIn(t, t.TempDir(), noon, "init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	for _, want := range []string{
		"created " + config.DefaultFilename,
		"created " + scaffoldDir,
		"DATABASE_URL",
		"migr create",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("init's report does not mention %q:\n%s", want, stdout)
		}
	}
}

func TestInitAnchorsTheDirectoryToItsConfigRatherThanToTheWorkingDirectory(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/migr")

	dir := t.TempDir()
	other := filepath.Join(dir, "other")

	if err := os.MkdirAll(other, 0o750); err != nil {
		t.Fatalf("staging a sibling project: %v", err)
	}

	if _, _, err := runIn(t, dir, noon, "--config", filepath.Join("other", config.DefaultFilename),
		"init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	if !exists(t, filepath.Join(other, scaffoldDir)) {
		t.Error("init created no migrations directory beside the config it wrote")
	}

	if exists(t, filepath.Join(dir, scaffoldDir)) {
		t.Error("init created a migrations directory beside the operator instead of the config")
		t.Log("`migr --config ../other/migr.yaml init` scaffolding into the current directory is")
		t.Log("the writing half of the mistake internal/config's anchor exists to prevent.")
	}
}

func TestInitJSONIsValidAndNamesWhatItCreated(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/migr")

	dir := t.TempDir()

	stdout, stderr, err := runIn(t, dir, noon, "init", "--json")
	if err != nil {
		t.Fatalf("init --json: %v", err)
	}

	var result struct {
		Config        string   `json:"config"`
		MigrationsDir string   `json:"migrations_dir"`
		Created       []string `json:"created"`
	}

	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\ngot: %q", err, stdout)
	}

	if len(result.Created) != 2 {
		t.Errorf("init reported creating %v, want both the config and the directory", result.Created)
	}

	if result.Config == "" || result.MigrationsDir == "" {
		t.Errorf("init's JSON leaves a path empty: %+v", result)
	}

	if stderr != "" {
		t.Errorf("nothing should reach stderr at default verbosity, got %q", stderr)
	}
}
