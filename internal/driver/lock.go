package driver

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrLockTimeout marks an acquire that gave up waiting, which is not one that was refused.
var ErrLockTimeout = errors.New("gave up waiting for the migration lock")

// LockHolder is whoever a driver found holding the migration lock, in the terms its own engine uses.
type LockHolder struct {
	// Process names the holder the way its engine does, so an operator has something to act on.
	Process string
	// Application is what the holder called itself, empty where the engine would not say.
	Application string
	// Connected is how long the holder's session had been open, zero when the engine would not say.
	Connected time.Duration
}

// String names the holder, and says which of the two things about it the engine would not answer.
func (h LockHolder) String() string {
	return h.Process + h.describeApplication() + h.describeAge()
}

// describeApplication renders what the holder called itself, saying so when the engine withheld it.
func (h LockHolder) describeApplication() string {
	if h.Application == "" {
		return " (which did not name itself)"
	}

	return fmt.Sprintf(" (%q)", h.Application)
}

// describeAge renders how long the holder's session has been open, or that this build cannot see it.
func (h LockHolder) describeAge() string {
	if h.Connected == 0 {
		return ", whose session age this connection is not permitted to read"
	}

	return fmt.Sprintf(", whose session has been open for %s", h.Connected)
}

// snapshotCaveat states that a holder was read after the wait failed, so it is who holds it now.
const snapshotCaveat = "That holder was read after the wait had already failed, so it is who " +
	"holds the lock now rather than provably who blocked this run."

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
