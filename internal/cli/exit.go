package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
)

// Exit codes, as documented in CLAUDE.md §5.
//
// ExitCode resolves an explicit ExitError first, so a command may always classify a failure
// itself. Below that it reads three sentinels, because recognizing them in one place is what saves
// every command from having to remember them: driver.ErrLockTimeout, since giving up on the
// migration lock is contention rather than a runtime error; engine.ErrDrifted, since an
// applied migration that was edited is the one failure invariant #6 gives a code of its own; and
// engine.ErrDestructive, which is the rule for exit 2 exactly -- the operator retypes the command
// with --allow-destructive, or names the kind in migr.yaml, and the same command then runs.
//
// Nothing else resolves itself here, and the boundary is deliberate. A history row this build
// cannot read reports history.ErrCorrupt and a row written under a future checksum scheme reports
// checksum.SchemeError; the first needs manual repair and the second needs a newer migr, so
// neither is drift. Reporting either as exit code 4 is how operators learn to ignore exit code 4.
//
// The selector refusals a run's own flags produce are tagged by the command that owns those flags
// rather than added here, for the same reason: a sentinel resolved in this function is one every
// command inherits, and engine.ErrNoSteps belongs to the two that register --step (ADR 0029, E).
//
// ExitLockImpact has no producer yet. --require-safe and safety.max_lock_impact are M13-T6's, and
// every .sql migration compiles to ir.Raw, which every driver declares LockUnknown.
const (
	ExitOK         = 0
	ExitRuntime    = 1
	ExitValidation = 2
	ExitLock       = 3
	ExitDrift      = 4
	ExitLockImpact = 5
)

// errValidationFailed marks a validation run that found something, which is a failure of the project.
var errValidationFailed = errors.New("this project has migrations that will not do what they say")

// argsOf tags what a command refuses about its own positional arguments, which is a typo.
func argsOf(check cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := check(cmd, args); err != nil {
			return NewExitError(ExitValidation, err)
		}

		return nil
	}
}

// flagRefused tags what the flag parser would not accept, which the operator fixes by retyping it.
func flagRefused(_ *cobra.Command, err error) error {
	return NewExitError(ExitValidation, err)
}

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

	if errors.Is(err, engine.ErrDrifted) {
		return ExitDrift
	}

	if errors.Is(err, engine.ErrDestructive) {
		return ExitValidation
	}

	return ExitRuntime
}
