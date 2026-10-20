//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/cli"
)

// safetyProject is the Go project with the third migration that changes an existing table, nothing applied.
func safetyProject(t *testing.T) string {
	t.Helper()

	config := goProject(t)

	path := filepath.Join(filepath.Dir(config), "migrations", saferName)
	if err := os.WriteFile(path, []byte(saferBody), 0o600); err != nil {
		t.Fatalf("staging %s: %v", saferName, err)
	}

	if err := migr(t, config, "down", "--step", "3"); err != nil {
		t.Logf("clearing what an earlier run left behind: %v", err)
	}

	return config
}

// switchingOff appends a safety section turning the concurrent build off, so the plain one is judged.
func switchingOff(t *testing.T, config string) {
	t.Helper()

	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatalf("reading migr.yaml: %v", err)
	}

	body = append(body, []byte("safety:\n  rewrites:\n    concurrent_index: off\n")...)
	if err := os.WriteFile(config, body, 0o600); err != nil {
		t.Fatalf("writing migr.yaml: %v", err)
	}
}

func TestRequireSafePassesWhatPostgreSQLPerformsSafelyAndFailsWhatItWouldNot(t *testing.T) {
	config := safetyProject(t)

	plan := migrOut(t, config, "plan", "--safety")
	for _, want := range []string{
		"add_index  native  lock_free (on a table this migration creates)",
		"add_column  native  brief_exclusive",
		"add_check  native  brief_exclusive (declared long_exclusive, rewritten)",
		"add_index  native  lock_free (declared long_exclusive, rewritten)",
		"no operation exceeds safety.max_lock_impact (brief_exclusive)",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("migr plan --safety does not say %q:\n%s", want, plan)
		}
	}

	for _, command := range [][]string{{"plan", "--require-safe"}, {"validate", "--require-safe"}} {
		if err := migr(t, config, command...); err != nil {
			t.Errorf("migr %v over operations PostgreSQL performs safely: %v", command, err)
		}
	}

	switchingOff(t, config)

	for _, command := range [][]string{{"plan", "--require-safe"}, {"validate", "--require-safe"}} {
		err := migr(t, config, command...)
		if got := cli.ExitCode(err); got != cli.ExitLockImpact {
			t.Errorf("migr %v with the concurrent build off exited %d, want 5: %v", command, got, err)
		}
	}

	failure := migrFailure(t, config, "plan", "--require-safe")
	for _, want := range []string{saferName, "operation 2 (add_index)", "long_exclusive"} {
		if !strings.Contains(failure, want) {
			t.Errorf("the failure does not name %q:\n%s", want, failure)
		}
	}

	t.Log("The gate reads what a run would cost this server: an index built concurrently blocks")
	t.Log("nothing, and the same index built plainly holds a lock for the length of its scan.")
}
