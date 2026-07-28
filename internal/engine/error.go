package engine

import (
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/driver"
)

// ErrNothingToRun marks a migration that compiled to no statements at all, for errors.Is.
var ErrNothingToRun = errors.New("this migration compiles to no statements")

// ErrNotAtomic marks a migration this executor cannot apply as one all-or-nothing unit.
var ErrNotAtomic = errors.New("this migration cannot be applied atomically")

// StatementError names the statement that failed and where in the migration it sits.
type StatementError struct {
	// Position is the statement's place in the migration, counting from one as golden files do.
	Position int32
	// Statement is the compiled statement the session refused or the server rejected.
	Statement driver.Statement
	// Err is what the driver reported.
	Err error
}

// Error names the failing statement's position, its text, and what went wrong with it.
func (e *StatementError) Error() string {
	return fmt.Sprintf("statement %d: %s: %v", e.Position, e.Statement.SQL, e.Err)
}

// Unwrap reports the cause, so errors.Is and errors.As reach what the driver returned.
func (e *StatementError) Unwrap() error {
	return e.Err
}
