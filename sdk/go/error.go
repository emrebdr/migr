package migr

import (
	"errors"
	"strings"
)

// ErrMalformedName marks a migration registered from a file the filename grammar refuses.
var ErrMalformedName = errors.New("this file is not named as a migration")

// ErrDuplicate marks two migrations claiming one version, which history could not tell apart.
var ErrDuplicate = errors.New("two migrations claim one version")

// ErrNoMigration marks a registration with nothing to register.
var ErrNoMigration = errors.New("a registration needs a migration")

// ErrNoCaller marks a build that reports no file for a registration, leaving its version unknowable.
var ErrNoCaller = errors.New("this build reports no file for a registration")

// ErrBothDirections marks a migration that both authors an undo and declares it has none.
var ErrBothDirections = errors.New("a migration authors a down or declares it has none, not both")

// ErrNoReason marks a migration declaring itself irreversible without saying why.
var ErrNoReason = errors.New("an irreversible migration gives the reason a rollback halts with")

// ErrUnknownMigration marks a version naming no migration this program registered.
var ErrUnknownMigration = errors.New("no migration is registered under that version")

// ErrCanceled marks a call abandoned because the engine asked, which ends the migration running it.
var ErrCanceled = errors.New("the engine canceled this request")

// ErrClosed marks a callback made outside the window it is valid in, which is one execute.
var ErrClosed = errors.New("this call has ended, and its callbacks are no longer valid")

// RegistrationError reports everything wrong with what a program registered, rather than the first.
type RegistrationError struct {
	// Problems are the registrations that could not be accepted, in the order they were made.
	Problems []error
}

// Error renders every problem on one line, since a protocol message is one line.
func (e *RegistrationError) Error() string {
	found := make([]string, 0, len(e.Problems))
	for _, problem := range e.Problems {
		found = append(found, problem.Error())
	}

	return strings.Join(found, "; ")
}

// Unwrap reports each problem, so errors.Is finds the sentinel any one of them carries.
func (e *RegistrationError) Unwrap() []error {
	return e.Problems
}

// joinProblems reports every problem found, since one complaint would hide the rest.
func joinProblems(problems []error) error {
	if len(problems) == 1 {
		return problems[0]
	}

	return &RegistrationError{Problems: problems}
}
