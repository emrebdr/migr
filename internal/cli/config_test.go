package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// examplePath points at the config fixture copied from CLAUDE.md §9.
var examplePath = filepath.Join("..", "config", "testdata", "example.yaml")

func TestConfigCommandRendersResolvedConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost/db")

	stdout, _, err := runCLI(t, "config", "--config", examplePath)
	if err != nil {
		t.Fatalf("config command failed: %v", err)
	}

	for _, want := range []string{"version: 1", "driver: postgres", "migr_migrations"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output should contain %q, got:\n%s", want, stdout)
		}
	}
}

func TestConfigCommandJSON(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost/db")

	stdout, _, err := runCLI(t, "config", "--config", examplePath, "--json")
	if err != nil {
		t.Fatalf("config --json failed: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal([]byte(stdout), &cfg); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\ngot: %s", err, stdout)
	}

	if cfg["version"] != float64(1) {
		t.Errorf("version = %v, want 1", cfg["version"])
	}
}

func TestConfigCommandNeverPrintsCredentials(t *testing.T) {
	const password = "hunter2"

	t.Setenv("DATABASE_URL", "postgres://user:"+password+"@localhost/db")

	for _, args := range [][]string{
		{"config", "--config", examplePath},
		{"config", "--config", examplePath, "--json"},
		{"config", "--config", examplePath, "-vv"},
		{"config", "--config", examplePath, "--json", "-vv"},
	} {
		stdout, stderr, err := runCLI(t, args...)
		if err != nil {
			t.Fatalf("%v failed: %v", args, err)
		}

		if strings.Contains(stdout+stderr, password) {
			t.Errorf("%v leaked the password", args)
		}
	}
}

func TestConfigCommandUsesValidationExitCode(t *testing.T) {
	_, _, err := runCLI(t, "config", "--config", filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil {
		t.Fatal("a missing config file must be an error")
	}

	if got := ExitCode(err); got != ExitValidation {
		t.Errorf("ExitCode = %d, want %d", got, ExitValidation)
	}
}
