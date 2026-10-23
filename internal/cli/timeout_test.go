package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
)

// bounding stages a project whose migr.yaml ends in a safety block of the test's choosing.
func bounding(t *testing.T, safety string) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)
	path := filepath.Join(dir, config.DefaultFilename)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading migr.yaml: %v", err)
	}

	write(t, path, string(body)+"safety:\n"+safety)

	return dir, f, drivers
}

// stagedSlowStatement is a project whose second migration's statement outlasts safety.statement_timeout.
func stagedSlowStatement(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := bounding(t, "  lock_timeout: 10ms\n  statement_timeout: 50ms\n")
	f.slow = contendedTable

	return dir, f, drivers
}

// stagedSlowMigration is a project whose second migration retries past safety.migration_timeout.
func stagedSlowMigration(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := bounding(t, "  lock_timeout: 10ms\n  statement_timeout: 50ms\n"+
		"  migration_timeout: 100ms\n  lock_backoff: 1h\n")
	f.contended, f.contentions = contendedTable, 100

	return dir, f, drivers
}

func TestAStatementPastStatementTimeoutFailsNamingTheKeyToRaise(t *testing.T) {
	dir, f, drivers := stagedSlowStatement(t)

	_, _, err := runWith(t, dir, noon, drivers, "up")
	if got := ExitCode(err); got != ExitRuntime {
		t.Fatalf("exit %d, want %d: %v", got, ExitRuntime, err)
	}

	var stopped *engine.TimeoutError
	if !errors.As(err, &stopped) || stopped.Bound != engine.BoundStatement {
		t.Fatalf("the failure is %v, want the statement bound", err)
	}

	if !strings.Contains(said(err), "raise safety.statement_timeout") {
		t.Errorf("the failure does not name the key to raise: %v", err)
	}

	if len(f.runs) != 2 || f.runs[0].Outcome != "applied" || f.runs[1].Outcome != "failed" {
		t.Errorf("the history holds %+v, want the first applied and the second failed", f.runs)
	}

	t.Log("Exit 1, as any runtime failure: nothing about the command line was wrong, and the")
	t.Log("same command succeeds once the statement fits its bound or the bound is raised.")
}

func TestAMigrationPastMigrationTimeoutFailsNamingTheKeyToRaise(t *testing.T) {
	dir, _, drivers := stagedSlowMigration(t)

	_, _, err := runWith(t, dir, noon, drivers, "up")

	var stopped *engine.TimeoutError
	if !errors.As(err, &stopped) || stopped.Bound != engine.BoundMigration {
		t.Fatalf("the failure is %v, want the migration bound", err)
	}

	if got := ExitCode(err); got != ExitLock {
		t.Errorf("exit %d, want %d", got, ExitLock)
	}

	var exhausted *engine.LockRetryError
	if errors.As(err, &exhausted) {
		t.Error("a run the bound cut off mid-backoff was reported as having spent every retry")
	}

	t.Log("Exit 3: what held the migration up for its whole bound was a lock someone else holds, and")
	t.Log("the same command succeeds once they let go -- the code follows the cause, not the clock.")
}
