package cli

import (
	"errors"

	"github.com/emrebdr/migr/internal/driver"
)

// Exit codes, as documented in CLAUDE.md §5.
//
// ExitCode resolves an explicit ExitError first, so a command may always classify a failure
// itself. Below that it reads driver.ErrLockTimeout, because giving up on the migration lock is
// contention rather than a runtime error, and recognizing the contract's sentinel in one place is
// what saves every command from having to remember it.
const (
	ExitOK         = 0
	ExitRuntime    = 1
	ExitValidation = 2
	ExitLock       = 3
	ExitDrift      = 4
	ExitLockImpact = 5
)

// ExitError carries the process exit code a failure should produce.
type ExitError struct {
	Code int
	Err  error
}

// Error implements error.
func (e *ExitError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying cause to errors.Is and errors.As.
func (e *ExitError) Unwrap() error { return e.Err }

// NewExitError wraps err so that it terminates the process with code.
func NewExitError(code int, err error) *ExitError {
	return &ExitError{Code: code, Err: err}
}

// ExitCode resolves an error to a process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}

	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}

	if errors.Is(err, driver.ErrLockTimeout) {
		return ExitLock
	}

	return ExitRuntime
}
