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
	"github.com/emrebdr/migr/internal/history"
)

// contendedTable is the statement the contended stages make wait for a lock.
const contendedTable = "CREATE TABLE teams (id bigint);"

// contending stages a project whose second migration's statement gives up on a lock some number of times.
func contending(t *testing.T, times int) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)
	f.contended, f.contentions = contendedTable, times

	path := filepath.Join(dir, config.DefaultFilename)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading migr.yaml: %v", err)
	}

	write(t, path, string(body)+"safety:\n  lock_backoff: 1ms\n  lock_retries: 2\n")

	return dir, f, drivers
}

// stagedContended is a project whose statement outlasts every retry migr.yaml allows.
func stagedContended(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	return contending(t, 10)
}

// stagedContendedDown is an applied project whose rollback statement outlasts every retry.
func stagedContendedDown(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := contending(t, 0)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("staging the applied project: %v", err)
	}

	f.contended, f.contentions = "DROP TABLE teams;", 10

	return dir, f, drivers
}

func TestAStatementThatWaitedIsRetriedAndTheRunSaysSo(t *testing.T) {
	dir, f, drivers := contending(t, 2)

	stdout, stderr, err := runWith(t, dir, noon, drivers, "up")
	if err != nil {
		t.Fatalf("up: %v\n%s", err, stderr)
	}

	if !strings.Contains(stdout, "after 2 lock retries") {
		t.Errorf("the summary does not say the run was retried:\n%s", stdout)
	}

	if got := strings.Count(stderr, "gave up waiting for a lock, and will try again"); got != 2 {
		t.Errorf("warned %d times, want 2, at the default verbosity:\n%s", got, stderr)
	}

	if got := len(f.runs); got != 2 || f.runs[1].Outcome != history.Applied {
		t.Errorf("the history holds %+v, want both migrations applied and nothing for the attempts",
			f.runs)
	}

	t.Log("A retry is a warning rather than progress: contention on a production table is what an")
	t.Log("operator has to see without asking for -v.")
}

func TestAStatementStillWaitingAfterEveryRetryExitsAsLockContention(t *testing.T) {
	dir, _, drivers := stagedContended(t)

	_, stderr, err := runWith(t, dir, noon, drivers, "up")
	if got := ExitCode(err); got != ExitLock {
		t.Fatalf("exit %d, want %d: %v", got, ExitLock, err)
	}

	var exhausted *engine.LockRetryError
	if !errors.As(err, &exhausted) || exhausted.Attempts != 3 {
		t.Errorf("the failure is %v, want three attempts reported", err)
	}

	if !strings.Contains(err.Error(), "still waiting for a lock after 3 attempts") {
		t.Errorf("the failure does not say how often it was tried: %v", err)
	}

	if got := strings.Count(stderr, "will try again"); got != 2 {
		t.Errorf("warned %d times, want 2", got)
	}

	t.Log("Exit 3, because the same command succeeds once whoever holds the table lets go, which")
	t.Log("is the migration lock's reason for the same code.")
}

func TestLockRetriesZeroTriesAStatementOnce(t *testing.T) {
	dir, _, drivers := contending(t, 1)

	path := filepath.Join(dir, config.DefaultFilename)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading migr.yaml: %v", err)
	}

	write(t, path, strings.Replace(string(body), "lock_retries: 2", "lock_retries: 0", 1))

	_, stderr, err := runWith(t, dir, noon, drivers, "up")
	if got := ExitCode(err); got != ExitLock {
		t.Fatalf("exit %d, want %d: %v", got, ExitLock, err)
	}

	if strings.Contains(stderr, "will try again") {
		t.Errorf("a project asking for no retries was retried:\n%s", stderr)
	}
}
