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
