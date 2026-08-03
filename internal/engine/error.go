package engine

import (
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// ErrNothingToRun marks a migration that compiled to no statements at all, for errors.Is.
var ErrNothingToRun = errors.New("this migration compiles to no statements")

// ErrNotAtomic marks a migration this executor cannot apply as one all-or-nothing unit.
var ErrNotAtomic = errors.New("this migration cannot be applied atomically")

// ErrNoSource marks an apply run given nothing to ask for a migration's operations.
var ErrNoSource = errors.New("a source is required: nothing else can say what a migration does")

// ErrNoClock marks an apply run given no clock, which this package may not read for itself.
var ErrNoClock = errors.New("a clock is required: this package may not read one (invariant #8)")

// ErrNoRuntime marks an apply run that could not say what language its migrations are written in.
var ErrNoRuntime = errors.New("a runtime name is required: every history row records one")

// ErrNoHistory marks a session that handed back no history store to record runs in.
var ErrNoHistory = errors.New("this session has no migration history store")

// ErrDrifted marks an applied migration whose file has been edited since, for errors.Is.
var ErrDrifted = errors.New("an applied migration has been edited since it was applied")

// ErrDirty marks a migration whose last run left the schema between states, for errors.Is.
var ErrDirty = errors.New("a migration left the schema between states")

// ErrUnknownState marks a reconciled state this build has no rule for, for errors.Is.
var ErrUnknownState = errors.New("this build does not know that migration state")

// ErrNoDownAuthored marks a migration whose source carries no down of its own, for the engine to derive one.
var ErrNoDownAuthored = errors.New("this migration authors no down of its own")

// ErrMissing marks a version the database carries that no migration file describes, for errors.Is.
var ErrMissing = errors.New("the database carries a version no migration file describes")

// stamp renders a recorded instant in a message, in UTC because a history row is read anywhere.
const stamp = "2006-01-02 15:04:05 UTC"

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

// MigrationError names the migration a failure happened in, and the operation it came from.
type MigrationError struct {
	// Migration is the migration that failed, so every message names it (invariant #9).
	Migration migration.Migration
	// Batch is the batch the run belonged to, zero when the failure preceded one.
	Batch int64
	// Ordinal is the run's position within that batch, zero when the failure preceded one.
	Ordinal int32
	// Operation is the operation the failing statement was compiled from, nil for a refusal.
	Operation ir.Operation
	// Err is what went wrong.
	Err error
}

// Error names the migration, the operation the failure came from, and what went wrong.
func (e *MigrationError) Error() string {
	return fmt.Sprintf("%s%s: %v", e.Migration.Filename(), describeOperation(e.Operation), e.Err)
}

// Unwrap reports the cause, so errors.Is and errors.As reach the driver's own error.
func (e *MigrationError) Unwrap() error {
	return e.Err
}

// describeOperation names the operation a failure came from, when it came from one.
func describeOperation(op ir.Operation) string {
	if op == nil {
		return ""
	}

	return " (" + op.Kind().String() + ")"
}

// driftedError refuses the whole run, naming the migration edited after it was applied.
func driftedError(st history.Status) error {
	return fmt.Errorf("%w: %s was applied at %s and its source has changed since -- verifying "+
		"checksums and reporting exit code 4 is M4-T7's, so nothing here can say which half moved",
		ErrDrifted, describeVersion(st), st.Recorded.AppliedAt.UTC().Format(stamp))
}

// dirtyError refuses the whole run, naming the migration whose last run stopped partway.
func dirtyError(st history.Status) error {
	return fmt.Errorf("%w: %s stopped after statement %d and nothing here can resolve that -- the "+
		"dirty-state model that makes such a run recoverable is M4-T9's",
		ErrDirty, describeVersion(st), st.Recorded.LastStatement)
}

// missingError refuses the whole rollback, naming a version the database carries and no file does.
func missingError(st history.Status) error {
	return fmt.Errorf("%w: %s -- a rollback runs newest first, so stepping around it would undo "+
		"the migrations applied after it and leave a schema nothing describes. migr up skips such "+
		"a version because it has nothing to run for it, and this run has",
		ErrMissing, describeVersion(st))
}

// irreversibleError refuses the whole rollback, naming what has no inverse and why nothing ran.
func irreversibleError(cause error) error {
	return fmt.Errorf("nothing here can undo it: %w -- and the whole rollback is refused before "+
		"anything runs, because one that stopped at an operation with no inverse would leave a "+
		"schema no migration describes (ADR 0008); author a down for this migration to roll it back",
		cause)
}

// unknownStateError refuses a run rather than guessing at a state this build has no rule for.
func unknownStateError(st history.Status) error {
	return fmt.Errorf("%w: %s reconciled to %q", ErrUnknownState, describeVersion(st), st.State)
}

// duplicateError refuses a queue holding one version twice, which would apply it twice.
func duplicateError(version string) error {
	return fmt.Errorf("%w: version %s appears twice in the migrations this run was given, so it "+
		"would be applied twice under two ordinals", migration.ErrDuplicateVersion, version)
}

// describeVersion names a migration by its filename, falling back to the version a file cannot.
func describeVersion(st history.Status) string {
	if st.Migration == (migration.Migration{}) {
		return "version " + st.Version
	}

	return st.Migration.Filename()
}
