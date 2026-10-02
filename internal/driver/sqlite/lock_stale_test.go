package sqlite

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	stdexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// leave writes a record into the lock file beside a database, which is what a dead holder leaves.
func leave(t *testing.T, database string, contents string) string {
	t.Helper()

	path := lockPath(database)

	if err := os.WriteFile(path, []byte(contents), lockMode); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	return path
}

// recordOf renders a record the way a holder writes it, for a test to leave one behind.
func recordOf(t *testing.T, written record) string {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), "record")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}

	defer file.Close()

	if err := writeRecord(file, written); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}

	contents, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}

	return string(contents)
}

// deadPID returns a process id nothing is using, by starting one and waiting for it to finish.
func deadPID(t *testing.T) int {
	t.Helper()

	done := stdexec.Command("/bin/sh", "-c", "exit 0")
	if err := done.Run(); err != nil {
		t.Fatalf("running a process to completion: %v", err)
	}

	pid := done.Process.Pid

	if alive(pid) {
		t.Skipf("pid %d was reused between exiting and being looked at", pid)
	}

	return pid
}

// longRunning starts a process that outlives this test, for a record to name as a live holder.
func longRunning(t *testing.T) int {
	t.Helper()

	child := stdexec.Command("/bin/sh", "-c", "sleep 60")
	if err := child.Start(); err != nil {
		t.Fatalf("starting a long-running process: %v", err)
	}

	t.Cleanup(func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})

	return child.Process.Pid
}

func TestALockFileLeftByADeadProcessIsTakenOverRatherThanWaitedOn(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "app.db")

	leave(t, database, recordOf(t, record{PID: deadPID(t), Host: hostname(),
		Application: "migr dev", Acquired: "2026-09-28T19:00:00.000Z"}))

	c := open(t, database, driver.Options{LockWait: impatient})

	held(t, c)

	t.Log("Invariant #11 for this engine. A lock file does not vanish when its owner dies, so a")
	t.Log("run that crashed leaves both the file and its record behind -- measured, with a real")
	t.Log("SIGKILLed child. What the kernel does release is the flock, and that is the lock.")
}

func TestALockFileWhoseRecordNamesALiveProcessIsNotTakenEvenWhenFlockSaysItMayBe(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "app.db")

	rival := longRunning(t)

	leave(t, database, recordOf(t, record{PID: rival, Host: hostname(),
		Application: "migr dev", Acquired: "2026-09-28T19:00:00.000Z"}))

	c := open(t, database, driver.Options{LockWait: impatient})

	report := busyReport(t, errFrom(c.Lock(t.Context())))

	if report.Holder == nil {
		t.Fatalf("the report identified nobody: %v", report.Unidentified)
	}

	if !errors.Is(report, errUnenforced) {
		t.Errorf("the failure is %v, and does not say why a granted lock was refused", report)
	}

	t.Log("Nothing holds the flock here, so the kernel would grant it. Where flock is enforced")
	t.Log("that cannot happen while a holder is running, so the combination is evidence flock")
	t.Log("is not being enforced -- which is the network filesystem this strategy owes a story.")
}

func TestARecordNamingThisProcessIsOursToOverwrite(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "app.db")

	leave(t, database, recordOf(t, record{PID: os.Getpid(), Host: hostname(),
		Application: "migr dev", Acquired: "2026-09-28T19:00:00.000Z"}))

	c := open(t, database, driver.Options{LockWait: impatient})

	held(t, c)

	t.Log("The staleness rung exempts our own pid. A release that could not blank the record")
	t.Log("would otherwise wedge this process against itself for as long as it runs.")
}

func TestARecordFromAnotherHostIsTakenRatherThanWaitedOn(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "app.db")

	leave(t, database, recordOf(t, record{PID: longRunning(t), Host: hostname() + "-elsewhere",
		Application: "migr dev", Acquired: "2026-09-28T19:00:00.000Z"}))

	c := open(t, database, driver.Options{LockWait: impatient})

	held(t, c)

	t.Log("The pid this record names is running here, so only the host is what stops it being")
	t.Log("read as a live holder. A pid means nothing on another host, so there is no liveness")
	t.Log("to check and flock is all there is. Refusing here is what invariant #11 forbids: a")
	t.Log("container that crashes and comes back under a new hostname would be wedged forever")
	t.Log("by its own corpse.")
}

func TestAStampNothingCanSubtractIsNoAgeRatherThanAWrongOne(t *testing.T) {
	for _, tt := range []struct {
		name     string
		acquired string
	}{
		{name: "from the future", acquired: "2999-01-01T00:00:00.000Z"},
		{name: "not a time at all", acquired: "whenever"},
		{name: "absent", acquired: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			database := filepath.Join(dir, "app.db")

			leave(t, database, recordOf(t, record{PID: longRunning(t), Host: hostname(),
				Application: "migr dev", Acquired: tt.acquired}))

			c := open(t, database, driver.Options{LockWait: impatient})

			report := busyReport(t, errFrom(c.Lock(t.Context())))
			if report.Holder == nil {
				t.Fatalf("the report identified nobody: %v", report.Unidentified)
			}

			if report.Holder.Elapsed != 0 {
				t.Errorf("the hold reads as %s, want no age at all", report.Holder.Elapsed)
			}

			if report.Holder.Measured != "" {
				t.Errorf("a hold nothing could read is labeled %q", report.Holder.Measured)
			}
		})
	}

	t.Log("julianday answers NULL for a stamp it cannot read and a negative number for one in")
	t.Log("the future -- measured, both. An unlabeled zero is what LockHolder renders as an age")
	t.Log("this connection could not determine, and leaving the label off is what says so: since")
	t.Log("ADR 0060 the label rather than the duration carries that, because zero is a real answer")
	t.Log("on MySQL.")
}

func TestARecordNothingCanReadIsReportedAsItselfRatherThanAsAHolder(t *testing.T) {
	for _, tt := range []struct {
		name     string
		contents string
		why      string
	}{
		{name: "empty", contents: "", why: emptyRecord},
		{name: "not a record at all", contents: "held by someone\n", why: unreadableRecord},
		{name: "cut off mid-write", contents: `{"pid":4242,"host":"bui`, why: unreadableRecord},
		{name: "naming no process", contents: `{"host":"build-7"}`, why: incompleteRecord},
		{name: "a negative process", contents: `{"pid":-1,"host":"build-7"}`, why: incompleteRecord},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			database := filepath.Join(dir, "app.db")

			path := leave(t, database, tt.contents)

			file, err := os.OpenFile(path, os.O_RDWR, lockMode)
			if err != nil {
				t.Fatalf("opening it: %v", err)
			}

			defer file.Close()

			taken, err := takeExclusive(file)
			if err != nil || !taken {
				t.Fatalf("holding the lock file: taken=%v err=%v", taken, err)
			}

			c := open(t, database, driver.Options{LockWait: impatient})

			report := busyReport(t, errFrom(c.Lock(t.Context())))

			if report.Holder != nil {
				t.Fatalf("a record nothing can read named %v", report.Holder)
			}

			if report.Unidentified != tt.why {
				t.Errorf("the report says %q, want %q", report.Unidentified, tt.why)
			}
		})
	}

	t.Log("A reader holds no lock, so a read can catch a write mid-flight. That is what buys")
	t.Log("this design out of needing an atomic write: the degraded answer already has a field.")
}

func TestARecordNothingCanReadDoesNotBlockAnOtherwiseFreeLock(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "app.db")

	leave(t, database, "held by someone\n")

	c := open(t, database, driver.Options{LockWait: impatient})

	held(t, c)

	t.Log("Unreadable is not the same as held. flock is what decides, and it was free.")
}

func TestAKilledHolderLeavesALockTheNextRunTakesImmediately(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "app.db")

	child := holder(t, database)

	c := open(t, database, driver.Options{LockWait: impatient})

	if _, err := c.Lock(t.Context()); !errors.Is(err, driver.ErrLockTimeout) {
		t.Fatalf("the lock was free while the child held it: %v", err)
	}

	if err := child.Process.Kill(); err != nil {
		t.Fatalf("killing the child: %v", err)
	}

	if _, err := child.Process.Wait(); err != nil {
		t.Fatalf("reaping the child: %v", err)
	}

	if _, err := os.Stat(lockPath(database)); err != nil {
		t.Fatalf("the lock file vanished with its owner: %v", err)
	}

	held(t, c)

	t.Log("This is the sentence the task is named for, run rather than argued: the file and its")
	t.Log("record both outlive a SIGKILL, and the flock does not. No operator step in between.")
}

// holder starts a child holding the lock on a database, and kills it however the test ends.
func holder(t *testing.T, database string) *stdexec.Cmd {
	t.Helper()

	child := stdexec.Command(os.Args[0], "-test.run=TestHoldTheLockForAParent")
	child.Env = append(os.Environ(), holdEnv+"="+database)

	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	if err := child.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}

	t.Cleanup(func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})

	waitFor(t, output)

	return child
}

// holdEnv is how a parent tells the re-executed test binary which database to hold the lock on.
const holdEnv = "MIGR_TEST_HOLD_LOCK"

// heldMarker is what the child prints once it holds the lock, so the parent waits for the fact.
const heldMarker = "the child holds the lock"

// waitFor reads the child's stdout until it says it holds the lock, rather than sleeping for it.
func waitFor(t *testing.T, output io.Reader) {
	t.Helper()

	scanner := bufio.NewScanner(output)

	for scanner.Scan() {
		if strings.Contains(scanner.Text(), heldMarker) {
			return
		}
	}

	t.Fatalf("the child never said it holds the lock: %v", scanner.Err())
}

func TestHoldTheLockForAParent(t *testing.T) {
	database := os.Getenv(holdEnv)
	if database == "" {
		t.Skip("this test is the child half of the killed-holder case, and runs only when asked")
	}

	c := open(t, database, driver.Options{LockWait: impatient})

	if _, err := c.Lock(t.Context()); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	fmt.Fprintln(os.Stdout, heldMarker)

	time.Sleep(time.Minute)
}
