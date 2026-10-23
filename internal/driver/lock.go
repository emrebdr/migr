package driver

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrLockTimeout marks an acquire that gave up waiting, which is not one that was refused.
var ErrLockTimeout = errors.New("gave up waiting for the migration lock")

// ErrStatementLockTimeout marks a statement that gave up waiting for a table lock under safety.lock_timeout.
var ErrStatementLockTimeout = errors.New("a statement gave up waiting for a lock")

// LockHolder is whoever a driver found holding the migration lock, in the terms its own engine uses.
type LockHolder struct {
	// Process names the holder the way its engine does, so an operator has something to act on.
	Process string
	// Application is what the holder called itself, empty where the engine would not say.
	Application string
	// Elapsed is a duration the engine measured about the holder, zero where it would not say.
	Elapsed time.Duration
	// Measured names what Elapsed is a duration of, the three engines answering different questions.
	Measured string
}

// String names the holder, and says which of the two things about it the engine would not answer.
func (h LockHolder) String() string {
	return h.Process + h.describeApplication() + h.describeElapsed()
}

// describeApplication renders what the holder called itself, saying so when the engine withheld it.
func (h LockHolder) describeApplication() string {
	if h.Application == "" {
		return " (which did not name itself)"
	}

	return fmt.Sprintf(" (%q)", h.Application)
}

// describeElapsed renders the duration the engine measured, naming which duration it is.
func (h LockHolder) describeElapsed() string {
	if strings.TrimSpace(h.Measured) == "" {
		return h.describeUnmeasured()
	}

	return fmt.Sprintf(", whose %s is %s", h.Measured, h.Elapsed)
}

// describeUnmeasured says that no duration was read, or names a driver that read one and said nothing.
func (h LockHolder) describeUnmeasured() string {
	if h.Elapsed == 0 {
		return ", whose age this connection is not permitted to read"
	}

	return fmt.Sprintf(", and %s has passed without this driver saying what it measured, which is "+
		"a bug in the driver rather than something about the holder", h.Elapsed)
}

// snapshotCaveat states that a holder was read after the wait failed, so it is who holds it now.
const snapshotCaveat = "That holder was read after the wait had already failed, so it is who " +
	"holds the lock now rather than provably who blocked this run."

// LockWaitNext is what to do about a migration lock another run holds, whichever driver reported it.
const LockWaitNext = "another migr run holds the migration lock: let it finish and run the command " +
	"again, or wait longer with lock.timeout in migr.yaml or --lock-timeout"

// LockBusyError reports a wait that gave up, naming whoever held the lock when the driver looked.
type LockBusyError struct {
	// Driver is the driver that gave up, since each engine bounds its own wait its own way.
	Driver string
	// Wait is the bound the acquire was given, which is what giving up was measured against.
	Wait time.Duration
	// Holder is who held the lock when the driver looked, and is nil when it could not find out.
	Holder *LockHolder
	// Unidentified states why there is no holder, and is empty when there is one.
	Unidentified string
	// Err is the engine's own failure, unwrapped for errors.Is and errors.As.
	Err error
}

// Error names the driver, the bound it gave up at, and what it could learn about the holder.
func (e *LockBusyError) Error() string {
	return fmt.Sprintf("%s %v after %s: %s", e.Driver, ErrLockTimeout, e.Wait, e.describeHolder())
}

// Next says to let the holder finish or to wait longer for it.
func (e *LockBusyError) Next() string {
	return LockWaitNext
}

// describeHolder names who holds the lock, or says why this driver could not find out.
func (e *LockBusyError) describeHolder() string {
	if e.Holder == nil {
		return "the holder could not be identified: " + e.describeUnidentified()
	}

	return "it is held by " + e.Holder.String() + ". " + snapshotCaveat
}

// describeUnidentified states why no holder was found, refusing to leave the reason blank.
func (e *LockBusyError) describeUnidentified() string {
	if strings.TrimSpace(e.Unidentified) == "" {
		return "this driver gave no reason, which is a bug in the driver rather than a diagnosis"
	}

	return e.Unidentified
}

// Unwrap reports the sentinel and the engine's own failure, so errors.Is finds either.
func (e *LockBusyError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrLockTimeout}
	}

	return []error{ErrLockTimeout, e.Err}
}
