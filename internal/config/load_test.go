package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capability"
)

// env builds a lookup function over a fixed set of variables.
func env(pairs map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := pairs[name]

		return v, ok
	}
}

// loadFixture parses a testdata file with the given environment.
func loadFixture(t *testing.T, name string, vars map[string]string) (*Config, error) {
	t.Helper()

	path := filepath.Join("testdata", name)

	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	return parse(src, path, env(vars))
}

func TestExampleFromClaudeMDLoads(t *testing.T) {
	cfg, err := loadFixture(t, "example.yaml", map[string]string{
		"DATABASE_URL": "postgres://u:p@localhost:5432/db",
	})
	if err != nil {
		t.Fatalf("example config must load: %v", err)
	}

	checks := []struct {
		field string
		got   any
		want  any
	}{
		{field: "version", got: cfg.Version, want: 1},
		{field: "migrations.dir", got: cfg.Migrations.Dir, want: filepath.Join("testdata", "migrations")},
		{field: "migrations.language", got: cfg.Migrations.Language, want: LanguageTypeScript},
		{field: "databases.default.driver", got: cfg.Databases["default"].Driver, want: "postgres"},
		{field: "databases.default.schema", got: cfg.Databases["default"].Schema, want: "public"},
		{field: "history.table", got: cfg.History.Table, want: "migr_migrations"},
		{field: "lock.strategy", got: cfg.Lock.Strategy, want: LockAdvisory},
		{field: "lock.timeout", got: cfg.Lock.Timeout.Duration(), want: 30 * time.Second},
		{field: "safety.lock_timeout", got: cfg.Safety.LockTimeout.Duration(), want: 3 * time.Second},
		{field: "safety.lock_retries", got: cfg.Safety.LockRetries, want: 5},
		{field: "safety.max_lock_impact", got: cfg.Safety.MaxLockImpact, want: capability.BriefExclusive},
		{field: "safety.auto_rewrite", got: cfg.Safety.AutoRewrite, want: true},
		{field: "validation.allow_destructive", got: cfg.Validation.AllowDestructive, want: false},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}

	if got := cfg.Databases["default"].DSN.Reveal(); got != "postgres://u:p@localhost:5432/db" {
		t.Errorf("dsn was not expanded: %q", got)
	}
}

func TestUnknownKeyIsRejectedWithSourceContext(t *testing.T) {
	_, err := loadFixture(t, "unknown_key.yaml", nil)
	if err == nil {
		t.Fatal("an unknown key must be an error")
	}

	msg := err.Error()
	for _, want := range []string{"unknown field", "drivr", "databases:"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q, got:\n%s", want, msg)
		}
	}
}

func TestMissingRequiredFieldsAreAllReportedWithPaths(t *testing.T) {
	_, err := loadFixture(t, "missing_required.yaml", nil)
	if err == nil {
		t.Fatal("missing required fields must be an error")
	}

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected a *ValidationError, got %T", err)
	}

	wantPaths := []string{
		"databases.primary.dsn",
		"databases.replica.driver",
		"lock.strategy",
		"migrations.language",
		"safety.max_lock_impact",
		"version",
	}

	got := make(map[string]string, len(verr.Errors))
	for _, fe := range verr.Errors {
		got[fe.Path] = fe.Message
	}

	for _, path := range wantPaths {
		if _, ok := got[path]; !ok {
			t.Errorf("no problem reported at path %q; got %v", path, got)
		}
	}

	if len(verr.Errors) != len(wantPaths) {
		t.Errorf("reported %d problems, want %d: %v", len(verr.Errors), len(wantPaths), got)
	}
}

// loadWithCeiling parses a minimal valid document carrying one max_lock_impact setting.
func loadWithCeiling(t *testing.T, ceiling string) (*Config, error) {
	t.Helper()

	src := []byte(`
version: 1
migrations:
  language: go
databases:
  default:
    driver: postgres
    dsn: postgres://localhost/db
safety:
  max_lock_impact: ` + ceiling + "\n")

	return parse(src, "inline.yaml", env(nil))
}

func TestTheAcceptedCeilingsAreTheOnesCapabilityDefines(t *testing.T) {
	for _, ceiling := range capability.Ceilings() {
		cfg, err := loadWithCeiling(t, string(ceiling))
		if err != nil {
			t.Errorf("max_lock_impact: %s was rejected: %v", ceiling, err)

			continue
		}

		if cfg.Safety.MaxLockImpact != ceiling {
			t.Errorf("max_lock_impact: %s loaded as %q", ceiling, cfg.Safety.MaxLockImpact)
		}
	}

	t.Log("Config validates against capability.Ceilings() rather than a list of its own, so")
	t.Log("renaming or removing a verdict on either side fails here instead of leaving")
	t.Log("migr.yaml quietly accepting a word no driver reports.")
}

func TestAnUnresolvedLockVerdictIsNotAValidCeiling(t *testing.T) {
	_, err := loadWithCeiling(t, string(capability.LockUnknown))
	if err == nil {
		t.Fatal("max_lock_impact: unknown was accepted")
	}

	if !strings.Contains(err.Error(), "safety.max_lock_impact") {
		t.Errorf("error does not name the offending field:\n%s", err)
		t.Log("\"Block anything worse than unknown\" has no meaning. Unknown is a verdict a driver")
		t.Log("reports, never a threshold an operator sets, and keeping Ceilings() separate from")
		t.Log("Impacts() is what enforces that here without a special case.")
	}
}

func TestValidationErrorReportsEveryProblemAtOnce(t *testing.T) {
	_, err := loadFixture(t, "missing_required.yaml", nil)
	if err == nil {
		t.Fatal("expected an error")
	}

	if !strings.Contains(err.Error(), "6 problems") {
		t.Errorf("error should summarize the count, got:\n%s", err)
	}
}

func TestDefaultsAreApplied(t *testing.T) {
	src := []byte(`
version: 1
migrations:
  language: go
databases:
  default:
    driver: sqlite
    dsn: file:test.db
`)

	cfg, err := parse(src, "test.yaml", env(nil))
	if err != nil {
		t.Fatalf("minimal config must load: %v", err)
	}

	checks := []struct {
		field string
		got   any
		want  any
	}{
		{field: "migrations.dir", got: cfg.Migrations.Dir, want: "migrations"},
		{field: "history.table", got: cfg.History.Table, want: "migr_migrations"},
		{field: "lock.strategy", got: cfg.Lock.Strategy, want: LockAdvisory},
		{field: "lock.timeout", got: cfg.Lock.Timeout.Duration(), want: 30 * time.Second},
		{field: "safety.lock_timeout", got: cfg.Safety.LockTimeout.Duration(), want: 3 * time.Second},
		{field: "safety.lock_retries", got: cfg.Safety.LockRetries, want: 5},
		{field: "safety.statement_timeout", got: cfg.Safety.StatementTimeout.Duration(), want: 30 * time.Second},
		{field: "safety.max_lock_impact", got: cfg.Safety.MaxLockImpact, want: capability.BriefExclusive},
		{field: "safety.replica_lag_threshold", got: cfg.Safety.ReplicaLagThreshold.Duration(), want: 5 * time.Second},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("default %s = %v, want %v", c.field, c.got, c.want)
		}
	}
}

func TestEnvValueCannotInjectYAMLStructure(t *testing.T) {
	src := []byte(`
version: 1
migrations:
  language: go
databases:
  default:
    driver: postgres
    dsn: ${DATABASE_URL}
`)

	cfg, err := parse(src, "test.yaml", env(map[string]string{
		"DATABASE_URL": "x\nallow_destructive: true\nmalicious: true",
	}))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if cfg.Validation.AllowDestructive {
		t.Error("an environment value must not be able to set other config fields")
	}

	if got := cfg.Databases["default"].DSN.Reveal(); !strings.Contains(got, "malicious") {
		t.Errorf("the value should survive intact as a scalar, got %q", got)
	}
}

func TestEmptyFileIsRejected(t *testing.T) {
	if _, err := parse([]byte("\n"), "test.yaml", env(nil)); err == nil {
		t.Fatal("an empty config file must be an error")
	}
}

func TestLoadReportsMissingFileHelpfully(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil {
		t.Fatal("a missing file must be an error")
	}

	if !strings.Contains(err.Error(), "migr init") {
		t.Errorf("error should suggest a remedy, got: %v", err)
	}
}

func TestResolve(t *testing.T) {
	if got := Resolve(""); got != DefaultFilename {
		t.Errorf("Resolve(\"\") = %q, want %q", got, DefaultFilename)
	}

	if got := Resolve("custom.yaml"); got != "custom.yaml" {
		t.Errorf("Resolve(%q) = %q, want the explicit value", "custom.yaml", got)
	}
}

// withDir renders a minimal config naming a migrations directory, or omitting it for the default.
func withDir(dir string) []byte {
	src := "version: 1\nmigrations:\n  language: go\n"
	if dir != "" {
		src += "  dir: " + dir + "\n"
	}

	return []byte(src + "databases:\n  default:\n    driver: postgres\n    dsn: postgres://h/db\n")
}

func TestAConfigNamesItsOwnMigrationsFromAnywhere(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "elsewhere")

	tests := []struct {
		name   string
		dir    string
		source string
		want   string
	}{
		{
			name:   "relative to a config in another directory",
			dir:    "./migrations",
			source: filepath.Join("..", "other", "migr.yaml"),
			want:   filepath.Join("..", "other", "migrations"),
		},
		{
			name:   "relative to a config named by an absolute path",
			dir:    "db/changes",
			source: "/srv/app/migr.yaml",
			want:   "/srv/app/db/changes",
		},
		{
			name:   "an absolute dir is left where it points",
			dir:    absolute,
			source: filepath.Join("..", "other", "migr.yaml"),
			want:   absolute,
		},
		{
			name:   "the default, beside a config in the working directory",
			dir:    "",
			source: DefaultFilename,
			want:   "migrations",
		},
		{
			name:   "the default, beside a config somewhere else",
			dir:    "",
			source: filepath.Join("..", "other", "migr.yaml"),
			want:   filepath.Join("..", "other", "migrations"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parse(withDir(tc.dir), tc.source, env(nil))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			if cfg.Migrations.Dir != tc.want {
				t.Errorf("migrations.dir = %q, want %q", cfg.Migrations.Dir, tc.want)
			}
		})
	}

	t.Log("Read relative to the working directory instead, `migr --config ../other/migr.yaml up`")
	t.Log("would find whatever ./migrations happened to be beside the operator and apply it to")
	t.Log("the other project's database. Nothing had read migrations.dir before M4-T1, so")
	t.Log("nothing had noticed.")
}
