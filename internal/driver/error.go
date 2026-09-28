package driver

import (
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
)

// ErrUnsupported marks an operation a driver refuses to compile, for errors.Is.
var ErrUnsupported = errors.New("unsupported operation")

// ErrNotReadOnly marks a Query the server refused because it would write, which is not a bug.
var ErrNotReadOnly = errors.New("this statement writes, and a migration reads through host.db.query")

// ErrNeedsSession marks an operation a driver performs only against a live database, for errors.Is.
var ErrNeedsSession = errors.New("this operation needs a database to compile")

// NeedsSessionError reports an operation a driver emulates, which Compile alone cannot produce.
type NeedsSessionError struct {
	// Driver is the driver that cannot compile it without a session.
	Driver string
	// Kind is the operation it cannot compile without one.
	Kind ir.Kind
	// Reason states what the driver has to read first, in terms an operator can act on.
	Reason string
}

// Error names the driver, the operation, and what a session is needed for.
func (e *NeedsSessionError) Error() string {
	return fmt.Sprintf("%s compiles %s against the database: %s", e.Driver, e.Kind, e.Reason)
}

// Unwrap reports the sentinel so errors.Is(err, ErrNeedsSession) succeeds.
func (e *NeedsSessionError) Unwrap() error {
	return ErrNeedsSession
}

// NeedsSession builds a driver's report that it emulates an operation and needs a session to do it.
func NeedsSession(name string, kind ir.Kind, reason string) error {
	return &NeedsSessionError{Driver: name, Kind: kind, Reason: reason}
}

// UnsupportedError reports an operation a driver cannot perform, and why.
type UnsupportedError struct {
	// Driver is the driver that refused.
	Driver string
	// Kind is the operation it refused.
	Kind ir.Kind
	// Reason states what the engine cannot do, in terms an operator can act on.
	Reason string
}

// Error names the driver, the operation, and the reason it cannot be compiled.
func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("%s does not support %s: %s", e.Driver, e.Kind, e.Reason)
}

// Unwrap reports the sentinel so errors.Is(err, ErrUnsupported) succeeds.
func (e *UnsupportedError) Unwrap() error {
	return ErrUnsupported
}

// Unsupported builds a driver's refusal to compile an operation.
func Unsupported(name string, kind ir.Kind, reason string) error {
	return &UnsupportedError{Driver: name, Kind: kind, Reason: reason}
}
