package sqlite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

func TestTheLockFileSitsBesideTheDatabaseItLocks(t *testing.T) {
	if got := lockPath("/srv/app.db"); got != "/srv/app.db-migr.lock" {
		t.Errorf("the lock file for /srv/app.db is %q", got)
	}

	if got := lockPath("/srv/app"); got != "/srv/app-migr.lock" {
		t.Errorf("a database with no extension locks on %q", got)
	}
}

func TestOneDatabaseFileIsOneLockWhateverTheHistoryTableIsCalled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")

	first := open(t, path, driver.Options{HistoryTable: "migr_migrations", LockWait: impatient})
	second := open(t, path, driver.Options{HistoryTable: "other_migrations", LockWait: impatient})

	held(t, first)

	if _, err := second.Lock(t.Context()); err == nil {
		t.Error("two installations in one file each took the lock")
		t.Log("PostgreSQL keys its advisory lock to schema and table because two installations")
		t.Log("in one database do not contend. SQLite takes one write lock for the whole file,")
		t.Log("so they do, and the lock is per file rather than per installation.")
	}
}

func TestARecordIsOverwrittenWithNothingOfTheOldOneLeftBehind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db-migr.lock")

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, lockMode)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer file.Close()

	long := record{PID: 4242, Host: strings.Repeat("a", 200), Application: "migr dev",
		Acquired: "2026-09-28T19:00:00.000Z"}
	if err := writeRecord(file, long); err != nil {
		t.Fatalf("writing the long record: %v", err)
	}

	short := record{PID: 7, Host: "b", Application: "migr dev", Acquired: "2026-09-28T19:00:01.000Z"}
	if err := writeRecord(file, short); err != nil {
		t.Fatalf("writing the short record: %v", err)
	}

	recorded, why, err := readRecord(file)
	if err != nil || why != "" {
		t.Fatalf("reading it back: why=%q err=%v", why, err)
	}

	if recorded.PID != short.PID || recorded.Host != short.Host {
		t.Errorf("the record reads back as %+v, want %+v", recorded, short)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file: %v", err)
	}

	if strings.Contains(string(contents), strings.Repeat("a", 200)) {
		t.Error("the longer record is still in the file underneath the shorter one")
	}
}

func TestARecordNamesTheHostBecauseAPidAloneDoesNot(t *testing.T) {
	named := record{PID: 4242, Host: "build-7"}
	if got := named.describeProcess(); got != "pid 4242 on host build-7" {
		t.Errorf("the holder renders as %q", got)
	}

	anonymous := record{PID: 4242}
	if got := anonymous.describeProcess(); got != "pid 4242" {
		t.Errorf("a record with no host renders as %q", got)
	}
}

func TestAProcessIdThatCouldNotBeOneIsNotAlive(t *testing.T) {
	for _, pid := range []int{0, -1, -4242} {
		if alive(pid) {
			t.Errorf("pid %d reads as running", pid)
		}
	}

	if !alive(os.Getpid()) {
		t.Error("this process reads as gone")
	}

	if !alive(1) {
		t.Error("pid 1 reads as gone")
		t.Log("It is owned by root and signaling it is refused, which is EPERM rather than")
		t.Log("ESRCH -- measured. Reading a refused signal as gone would take a lock a running")
		t.Log("process holds, which is the one mistake this check exists to not make.")
	}
}

func TestTheSessionCarriesTheWaitMigrYamlConfigured(t *testing.T) {
	c := open(t, memory, driver.Options{LockWait: 7 * time.Second})
	if c.lockWait != 7*time.Second {
		t.Errorf("the session waits %s, want 7s", c.lockWait)
	}

	unset := open(t, memory, driver.Options{})
	if unset.lockWait != driver.DefaultLockWait {
		t.Errorf("an unset wait is %s, want %s", unset.lockWait, driver.DefaultLockWait)
	}
}
