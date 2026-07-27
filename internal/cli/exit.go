package cli

import "errors"

// Exit codes, as documented in CLAUDE.md §5.
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

	return ExitRuntime
}
