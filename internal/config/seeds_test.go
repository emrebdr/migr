package config

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

// loadSeeding parses a minimal valid document with whatever seeds and databases blocks a test gives.
func loadSeeding(t *testing.T, seeds, databases string) (*Config, error) {
	t.Helper()

	if databases == "" {
		databases = "  default:\n    driver: postgres\n    dsn: postgres://localhost/db\n"
	}

	src := "version: 1\nmigrations:\n  language: sql\n" + seeds + "databases:\n" + databases

	return parse([]byte(src), filepath.Join("project", "migr.yaml"), env(nil))
}

// fieldProblem returns the message a validation failure gives one path, and whether it gave one.
func fieldProblem(err error, path string) (string, bool) {
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		return "", false
	}

	for _, fe := range invalid.Errors {
		if fe.Path == path {
			return fe.Message, true
		}
	}

	return "", false
}

func TestSeedsLiveApartFromMigrationsUnlessTheConfigSaysOtherwise(t *testing.T) {
	cfg, err := loadSeeding(t, "", "")
	if err != nil {
		t.Fatalf("a config naming no seeds block must load: %v", err)
	}

	if want := filepath.Join("project", "seeds"); cfg.Seeds.Dir != want {
		t.Errorf("seeds.dir resolved to %q, want %q beside migr.yaml", cfg.Seeds.Dir, want)
	}

	if cfg.Seeds.Table != driver.DefaultSeedTable || cfg.Seeds.Table == cfg.History.Table {
		t.Errorf("seeds.table defaulted to %q beside history.table %q, want %q",
			cfg.Seeds.Table, cfg.History.Table, driver.DefaultSeedTable)
	}

	if got := cfg.DriverOptions(cfg.Databases["default"]).SeedTable; got != cfg.Seeds.Table {
		t.Errorf("the driver is told to record seeds in %q, want %q", got, cfg.Seeds.Table)
		t.Log("The driver binds the seed store at Open, so a table name that never reaches it is")
		t.Log("a seeds.table that is accepted and ignored.")
	}
}

func TestADatabaseRunsTheEnvironmentItsOwnKeyNamesUnlessItSaysOtherwise(t *testing.T) {
	cfg, err := loadSeeding(t, "", "  staging:\n    driver: postgres\n    dsn: postgres://h/s\n"+
		"  local:\n    driver: postgres\n    dsn: postgres://h/l\n    environment: development\n")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	if got := cfg.Databases["staging"].Environment; got != "staging" {
		t.Errorf("a database naming no environment runs %q, want its own key", got)
	}

	if got := cfg.Databases["local"].Environment; got != "development" {
		t.Errorf("a database naming its environment runs %q, want the one it named", got)
	}

	if got := cfg.Environments(); !slices.Equal(got, []string{"development", "staging"}) {
		t.Errorf("the environments are %v, want what the databases run, sorted", got)
	}
}

func TestDeclaredEnvironmentsAreTheOnesASeedsDirectoryMayHold(t *testing.T) {
	cfg, err := loadSeeding(t, "seeds:\n  environments: [production, development]\n",
		"  default:\n    driver: postgres\n    dsn: postgres://h/d\n    environment: development\n")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	if got := cfg.Environments(); !slices.Equal(got, []string{"development", "production"}) {
		t.Errorf("the environments are %v, want the declared ones", got)
		t.Log("One migr.yaml commonly serves every environment through ${VAR}, so the databases")
		t.Log("it names on one machine are not every environment the seeds directory holds.")
	}
}

func TestADatabaseOutsideTheDeclaredEnvironmentsIsRefused(t *testing.T) {
	_, err := loadSeeding(t, "seeds:\n  environments: [development, production]\n",
		"  default:\n    driver: postgres\n    dsn: postgres://h/d\n    environment: prodution\n")

	msg, found := fieldProblem(err, "databases.default.environment")
	if !found || !strings.Contains(msg, "prodution") {
		t.Fatalf("a database running an undeclared environment loaded: %v", err)
	}
}

func TestSeedsMayShareNoTableAndNoDirectoryWithMigrations(t *testing.T) {
	tests := []struct {
		name  string
		seeds string
		path  string
	}{
		{name: "the history table", seeds: "seeds:\n  table: migr_migrations\n", path: "seeds.table"},
		{name: "the migrations directory", seeds: "seeds:\n  dir: ./migrations\n", path: "seeds.dir"},
		{name: "a directory inside it", seeds: "seeds:\n  dir: ./migrations/seeds\n", path: "seeds.dir"},
		{name: "a directory holding it", seeds: "seeds:\n  dir: .\n", path: "seeds.dir"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadSeeding(t, tc.seeds, "")
			if _, found := fieldProblem(err, tc.path); !found {
				t.Fatalf("seeds sharing %s with migrations loaded: %v", tc.name, err)
			}
		})
	}

	if _, err := loadSeeding(t, "seeds:\n  dir: ./migrations_seeds\n", ""); err != nil {
		t.Errorf("a directory that only shares a prefix with migrations.dir was refused: %v", err)
	}
}

func TestAnEnvironmentThatCannotNameOneDirectoryIsRefused(t *testing.T) {
	for _, env := range []string{"prod/eu", ".hidden", `win\dows`} {
		_, err := loadSeeding(t, "", "  default:\n    driver: postgres\n    dsn: postgres://h/d\n"+
			"    environment: '"+env+"'\n")
		if _, found := fieldProblem(err, "databases.default.environment"); !found {
			t.Errorf("environment %q loaded, and it names no single directory: %v", env, err)
		}
	}

	_, err := loadSeeding(t, "seeds:\n  environments: [dev, dev]\n",
		"  default:\n    driver: postgres\n    dsn: postgres://h/d\n    environment: dev\n")
	if _, found := fieldProblem(err, "seeds.environments[1]"); !found {
		t.Errorf("an environment declared twice loaded: %v", err)
	}
}
