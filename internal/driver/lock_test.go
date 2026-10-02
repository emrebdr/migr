package driver

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// busy returns a report of a wait that gave up, for a test to vary one part of.
func busy() *LockBusyError {
	return &LockBusyError{
		Driver: "postgres",
		Wait:   30 * time.Second,
		Holder: &LockHolder{
			Process:     "pid 4711",
			Application: "migr 0.1.0",
			Elapsed:     4*time.Minute + 12*time.Second,
			Measured:    "session age",
		},
		Err: errors.New("ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)"),
	}
}

func TestABusyLockNamesTheHolderAndWhichDurationItMeasured(t *testing.T) {
	message := busy().Error()

	for _, want := range []string{"postgres", "30s", "pid 4711", "migr 0.1.0", "4m12s",
		"session age"} {
		if !strings.Contains(message, want) {
			t.Errorf("the report is %q and does not carry %q", message, want)
		}
	}

	t.Log("The label is carried because the three engines measure three different durations and")
	t.Log("only the one that measured can say which (ADR 0060). PostgreSQL reports the holder's")
	t.Log("session age, which bounds the hold from above; SQLite reports the hold itself, out of")
	t.Log("its own lock file; MySQL reports the holder's time in its current state, because the")
	t.Log("server records no connection start time at all. One sentence over all three would have")
	t.Log("to be wrong about two of them.")
}

func TestABusyLockSaysItsHolderWasReadAfterTheWaitFailed(t *testing.T) {
	message := busy().Error()

	if !strings.Contains(message, snapshotCaveat) {
		t.Errorf("the report is %q and does not say when the holder was read", message)
		t.Log("The acquire fails, then the holder is read, so the holder may already have")
		t.Log("changed. Without the caveat the message asserts causation it cannot prove, and")
		t.Log("an operator who terminates the named process may kill an innocent session.")
	}

	if strings.Contains(message, "blocked this run.") && !strings.Contains(message, "rather than") {
		t.Error("the report claims the named holder blocked this run")
	}
}

func TestABusyLockWithNoHolderSaysWhyItCouldNotSayWho(t *testing.T) {
	report := busy()
	report.Holder = nil
	report.Unidentified = "nobody held it by the time this session looked"

	message := report.Error()
	if !strings.Contains(message, report.Unidentified) {
		t.Errorf("the report is %q and does not say why there is no holder", message)
	}

	if strings.Contains(message, snapshotCaveat) {
		t.Error("the report carries the snapshot caveat with no holder to caveat")
	}
}

func TestADriverThatGivesNoReasonIsReportedAsABugRatherThanAsADiagnosis(t *testing.T) {
	report := busy()
	report.Holder = nil
	report.Unidentified = "   "

	if !strings.Contains(report.Error(), "bug in the driver") {
		t.Errorf("the report is %q and reads as though nothing were wrong", report)
		t.Log("A blank reason is a driver that forgot to fill one in, and saying \"the holder")
		t.Log("could not be identified: \" with nothing after it reads as a diagnosis.")
	}
}

func TestAHolderWhoseDetailsAreNotReadableStillNamesAProcess(t *testing.T) {
	report := busy()
	report.Holder = &LockHolder{Process: "pid 4711"}

	message := report.Error()
	if !strings.Contains(message, "pid 4711") {
		t.Fatalf("the report is %q and names no process", message)
	}

	if strings.Contains(message, "is 0s") {
		t.Error("a holder whose age was withheld is reported as brand new")
		t.Log("Measured: pg_stat_activity withholds backend_start across roles while leaving")
		t.Log("application_name visible. Coalescing the age to zero would render \"open for 0s\"")
		t.Log("for a session that has held the lock for an hour.")
	}

	if !strings.Contains(message, "not permitted to read") {
		t.Errorf("the report is %q and does not say the age was withheld", message)
	}
}

func TestAHolderThatDidNotNameItselfSaysSoRatherThanNamingNothing(t *testing.T) {
	report := busy()
	report.Holder = &LockHolder{Process: "pid 4711", Elapsed: time.Minute, Measured: "session age"}

	if !strings.Contains(report.Error(), "did not name itself") {
		t.Errorf("the report is %q and renders an empty application as though it were a name",
			report)
	}
}

func TestADurationWithNoLabelIsReportedAsADriverBugRatherThanAsAnAge(t *testing.T) {
	report := busy()
	report.Holder = &LockHolder{Process: "pid 4711", Elapsed: time.Minute}

	message := report.Error()
	if !strings.Contains(message, "bug in the driver") {
		t.Errorf("the report is %q and renders an unlabelled duration as though it meant something",
			message)
	}

	if !strings.Contains(message, "1m0s") {
		t.Errorf("the report is %q and drops the duration it could not explain", message)
	}

	t.Log("The shape capability.Emulated already has for a strategy nobody named: a verdict that")
	t.Log("cannot be built honestly degrades to one that names the omission. Rendering \"whose  is")
	t.Log("1m0s\" would read as a typo rather than as the missing label it is, and a caller would")
	t.Log("have no way to tell which duration a driver had measured.")
}

func TestGivingUpOnTheLockIsTheContractsTimeoutAndNotADriversOwn(t *testing.T) {
	report := busy()

	if !errors.Is(report, ErrLockTimeout) {
		t.Error("a busy lock does not unwrap to the contract's sentinel")
		t.Log("internal/cli cannot import a driver (invariant #1), so exit code 3 is reachable")
		t.Log("only through a sentinel that lives on the contract.")
	}

	if !errors.Is(report, report.Err) {
		t.Error("a busy lock does not unwrap to the engine's own failure")
	}
}

func TestABusyLockWithNoUnderlyingFailureStillCarriesTheSentinel(t *testing.T) {
	report := busy()
	report.Err = nil

	if !errors.Is(report, ErrLockTimeout) {
		t.Error("a busy lock carrying no cause lost the sentinel with it")
	}
}
