package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/version"
)

// impatient is a wait short enough that a test giving up on the lock is not a test waiting.
const impatient = 150 * time.Millisecond

// shared connects two sessions to one database file, which is what contends for one lock.
func shared(t *testing.T, opts driver.Options) (string, *sqliteConn, *sqliteConn) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "app.db")

	return path, open(t, path, opts), open(t, path, opts)
}

// held takes the migration lock, failing the test rather than the caller, and gives the release back.
func held(t *testing.T, c *sqliteConn) driver.Unlock {
	t.Helper()

	unlock, err := c.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	t.Cleanup(func() {
		if err := unlock(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("unlock: %v", err)
		}
	})

	return unlock
}

// busyReport takes the failure a wait gave up with apart, failing the test for any other failure.
func busyReport(t *testing.T, err error) *driver.LockBusyError {
	t.Helper()

	if !errors.Is(err, driver.ErrLockTimeout) {
		t.Fatalf("the wait failed with %v, want driver.ErrLockTimeout", err)
	}

	var report *driver.LockBusyError
	if !errors.As(err, &report) {
		t.Fatalf("the failure is %T, and carries no driver.LockBusyError", err)
	}

	return report
}

func TestASecondSessionWaitsForTheLockAndThenGivesUpNamingWhoHoldsIt(t *testing.T) {
	_, first, second := shared(t, driver.Options{LockWait: impatient})

	held(t, first)

	_, err := second.Lock(t.Context())

	report := busyReport(t, err)
	if report.Holder == nil {
		t.Fatalf("the report identified nobody: %v", report.Unidentified)
	}

	if want := "pid " + strconv.Itoa(os.Getpid()); !strings.Contains(report.Holder.Process, want) {
		t.Errorf("the holder is %q, and does not name %q", report.Holder.Process, want)
	}

	if !strings.Contains(report.Holder.Process, hostname()) {
		t.Errorf("the holder is %q, and does not name this host %q", report.Holder.Process, hostname())
	}

	if report.Holder.Application != version.Program+" "+version.Version {
		t.Errorf("the holder called itself %q, want %q", report.Holder.Application,
			version.Program+" "+version.Version)
	}

	t.Log("Both sessions are in this process, and flock still separates them: it conflicts")
	t.Log("between two open file descriptions rather than between two processes. Measured.")
}

func TestTheHoldersAgeComesFromTheEnginesOwnClock(t *testing.T) {
	_, first, second := shared(t, driver.Options{LockWait: impatient})

	held(t, first)

	time.Sleep(300 * time.Millisecond)

	report := busyReport(t, errFrom(second.Lock(t.Context())))
	if report.Holder == nil {
		t.Fatalf("the report identified nobody: %v", report.Unidentified)
	}

	if report.Holder.Elapsed < 250*time.Millisecond {
		t.Errorf("the lock reads as held for %s, and it has been held for at least 300ms",
			report.Holder.Elapsed)
	}

	if report.Holder.Measured != measuredHold {
		t.Errorf("the hold is labeled %q, want %q", report.Holder.Measured, measuredHold)
	}

	if strings.Contains(report.Error(), "session") {
		t.Errorf("the report calls a hold a session age: %v", report)
	}

	t.Log("forbidigo bans time.Now in a driver, so the stamp is SQLite's own and SQLite")
	t.Log("subtracts it -- ADR 0020 E2's shape, with the in-process library as the server.")
	t.Log("")
	t.Log("What this driver measures is the hold itself, which is the better of the two numbers a")
	t.Log("report can carry and is not what PostgreSQL measures. Until ADR 0060 the field carrying")
	t.Log("it rendered as \"whose session has been open for\", which was false here -- a hold is not")
	t.Log("a session age, and this lock file outlives neither.")
}

func TestTheWaitIsAWaitRatherThanARefusal(t *testing.T) {
	_, first, second := shared(t, driver.Options{LockWait: 10 * time.Second})

	unlock, err := first.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	released := make(chan error, 1)

	go func() {
		time.Sleep(200 * time.Millisecond)
		released <- unlock(context.WithoutCancel(t.Context()))
	}()

	held(t, second)

	if err := <-released; err != nil {
		t.Fatalf("the first session's release: %v", err)
	}
}

func TestASecondAcquireOnOneSessionIsRefusedRatherThanGranted(t *testing.T) {
	c := openFile(t)

	held(t, c)

	if _, err := c.Lock(t.Context()); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("the second acquire returned %v, want %v", err, ErrLockHeld)
	}

	t.Log("It is refused rather than waited on: flock grants a second lock on the same open")
	t.Log("file description without blocking, so a session that asked twice would hold one")
	t.Log("lock and release it twice. Measured.")
}

func TestReleasingTwiceIsNotAFailure(t *testing.T) {
	c := openFile(t)

	unlock, err := c.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	if err := unlock(t.Context()); err != nil {
		t.Fatalf("the first release: %v", err)
	}

	if err := unlock(t.Context()); err != nil {
		t.Errorf("the second release: %v, and the contract says a release is safe to call twice", err)
	}
}

func TestTheLockIsGivenBackAndTheNextSessionTakesIt(t *testing.T) {
	_, first, second := shared(t, driver.Options{LockWait: impatient})

	unlock, err := first.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	if err := unlock(t.Context()); err != nil {
		t.Fatalf("release: %v", err)
	}

	held(t, second)
}

func TestReleasingBlanksTheRecordAndLeavesTheFile(t *testing.T) {
	path, first, _ := shared(t, driver.Options{})

	unlock, err := first.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	if err := unlock(t.Context()); err != nil {
		t.Fatalf("release: %v", err)
	}

	contents, err := os.ReadFile(lockPath(path))
	if err != nil {
		t.Fatalf("the lock file is gone after a release: %v", err)
	}

	if len(contents) != 0 {
		t.Errorf("the lock file still holds %q after the release", contents)
	}

	t.Log("The file stays and the record does not. A record outliving its hold is what the")
	t.Log("staleness rung reads as a second writer, and unlinking would let one run lock a")
	t.Log("file another had already deleted.")
}

func TestOneDatabaseReachesOneLockFileHoweverItWasNamed(t *testing.T) {
	dir := t.TempDir()
	absolute := filepath.Join(dir, "app.db")

	first := open(t, absolute, driver.Options{LockWait: impatient})
	held(t, first)

	for _, dsn := range []string{
		"file:" + absolute,
		filepath.Join(dir, "sub", "..", "app.db"),
		"file:" + absolute + "?_pragma=cache_size(-2000)",
	} {
		rival := open(t, dsn, driver.Options{LockWait: impatient})

		if _, err := rival.Lock(t.Context()); !errors.Is(err, driver.ErrLockTimeout) {
			t.Errorf("%s took the lock the absolute path holds, failing with %v", dsn, err)
		}
	}

	t.Log("The path comes from PRAGMA database_list rather than from the DSN, and the engine")
	t.Log("answers with an absolute, symlink-resolved path. Measured: /var/... came back as")
	t.Log("/private/var/... on this machine, which no string handling here would have done.")
}

func TestAnInMemoryDatabaseLocksTheSessionAndWritesNoFile(t *testing.T) {
	dir := t.TempDir()

	before := entries(t, dir)

	for _, dsn := range []string{memory, "file::memory:", "file::memory:?cache=shared",
		"file:probe?mode=memory"} {
		c := open(t, dsn, driver.Options{LockWait: impatient})

		if c.path != "" {
			t.Errorf("%s reports its database file as %q, want no file at all", dsn, c.path)
		}

		unlock, err := c.Lock(t.Context())
		if err != nil {
			t.Fatalf("%s: Lock: %v", dsn, err)
		}

		if _, err := c.Lock(t.Context()); !errors.Is(err, ErrLockHeld) {
			t.Errorf("%s: a second acquire returned %v, want %v", dsn, err, ErrLockHeld)
		}

		if err := unlock(t.Context()); err != nil {
			t.Errorf("%s: release: %v", dsn, err)
		}
	}

	if after := entries(t, dir); len(after) != len(before) {
		t.Errorf("locking an in-memory database wrote %v", after)
	}

	t.Log("Every in-memory spelling reports an empty path -- measured, all four of them -- and")
	t.Log("such a database belongs to the connection that opened it. There is nobody to exclude,")
	t.Log("so the lock is the session's own flag and that is a complete answer, not a degraded one.")
}

func TestTwoInMemoryDatabasesDoNotContendBecauseTheyAreTwoDatabases(t *testing.T) {
	first, second := openMemory(t), openMemory(t)

	held(t, first)
	held(t, second)
}

func TestALockFileThatCannotBeCreatedIsNotReportedAsContention(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a directory whose permissions forbid it, so there is nothing to refuse")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")

	c := open(t, path, driver.Options{LockWait: impatient})

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := c.Lock(t.Context())
	if err == nil {
		t.Fatal("Lock succeeded in a directory nothing may be created in")
	}

	if errors.Is(err, driver.ErrLockTimeout) {
		t.Errorf("a directory that cannot be written reported as contention: %v", err)
		t.Log("Exit code 3 means another run holds the lock, and an operator who reads it that")
		t.Log("way waits for a run that does not exist.")
	}

	if !errors.Is(err, ErrLockFile) {
		t.Errorf("the failure is %v, and does not name the lock file", err)
	}
}

func TestACanceledContextIsNotReportedAsContentionEither(t *testing.T) {
	_, first, second := shared(t, driver.Options{LockWait: time.Hour})

	held(t, first)

	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := second.Lock(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the canceled wait failed with %v, want context.Canceled", err)
	}

	if errors.Is(err, driver.ErrLockTimeout) {
		t.Error("an interrupted run reported as contention, which is exit code 3 for a Ctrl-C")
	}
}

func TestTheLockFileIsNoneOfTheThreeSQLiteOwns(t *testing.T) {
	for _, owned := range []string{"-journal", "-wal", "-shm"} {
		if strings.HasSuffix(lockSuffix, owned) || lockSuffix == owned {
			t.Errorf("the lock file's suffix %q collides with SQLite's own %q", lockSuffix, owned)
		}
	}

	if !strings.HasPrefix(lockSuffix, "-") {
		t.Errorf("the lock file's suffix is %q, and SQLite's sidecars are all -something", lockSuffix)
	}
}

func TestAMigrationRunsWithTheLockHeld(t *testing.T) {
	c := openFile(t)

	held(t, c)

	exec(t, c, `CREATE TABLE users (id integer)`)

	if err := c.Transact(t.Context(), func(ctx context.Context) error {
		return c.Exec(ctx, driver.Statement{SQL: `INSERT INTO users VALUES (1)`})
	}); err != nil {
		t.Fatalf("a transaction under the lock: %v", err)
	}

	t.Log("The lock is taken before the transaction begins, and the lock file is a separate")
	t.Log("file from the database, so SQLite's own fcntl locking never meets migr's flock.")
}

// errFrom drops the release a successful acquire returns, leaving the failure a test asserts on.
func errFrom(_ driver.Unlock, err error) error {
	return err
}

// entries names what a directory holds, so a test can say nothing was written into it.
func entries(t *testing.T, dir string) []string {
	t.Helper()

	names, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	return names
}
