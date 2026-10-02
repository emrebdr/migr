package engine

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
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

// ErrDrifted marks a recorded migration whose file has been edited since that run, for errors.Is.
var ErrDrifted = errors.New("a recorded migration's file has been edited since the run that recorded it")

// ErrDirty marks a migration whose last run left the schema between states, for errors.Is.
var ErrDirty = errors.New("a migration left the schema between states")

// ErrUnknownRepair marks a way of resolving a dirty migration this build has no rule for, for errors.Is.
var ErrUnknownRepair = errors.New("this build does not know that way of resolving a dirty migration")

// unknownRepairError refuses a repair rather than guessing which of the three was meant.
func unknownRepairError(mode RepairMode) error {
	return fmt.Errorf("%w: %q is not one of %v", ErrUnknownRepair, mode, Repairs())
}

// ErrNoReason marks a resolution whose reason is missing where one is owed or given where none is.
var ErrNoReason = errors.New("a resolution records the operator's reason for it")

// missingReasonError refuses an asserted repair that records no account of what was actually done.
func missingReasonError(mode RepairMode) error {
	return fmt.Errorf("%w, and --%s records one: the history row is the only thing that will still "+
		"say why this migration reads as settled when nobody remembers the incident", ErrNoReason, mode)
}

// unwantedReasonError refuses a reason on a resume, which records what it ran rather than a claim.
func unwantedReasonError(mode RepairMode) error {
	return fmt.Errorf("%w, and --%s is not one: it runs the statements the dirty run never reached, "+
		"so what it records is what it did rather than an account of what somebody else did",
		ErrNoReason, mode)
}

// ErrSeveralDirty marks a database carrying more than one migration between states, for errors.Is.
var ErrSeveralDirty = errors.New("this database carries more than one migration between states")

// severalDirtyError refuses a resolution rather than picking which of several dirty migrations to settle.
func severalDirtyError(dirty []history.Status) error {
	return fmt.Errorf("%w: %s -- no migr run can produce that, since a dirty migration refuses "+
		"both directions before anything else runs, so these were not all left by this tool. "+
		"Settling one would be a guess at which",
		ErrSeveralDirty, describeDirty(dirty))
}

// describeDirty lists the migrations a database left between states, by the names a message renders.
func describeDirty(dirty []history.Status) string {
	named := make([]string, 0, len(dirty))
	for _, st := range dirty {
		named = append(named, describeVersion(st))
	}

	return strings.Join(named, ", ")
}

// ErrNotResumable marks a dirty migration whose recorded index no longer names a statement, for errors.Is.
var ErrNotResumable = errors.New("this migration cannot be resumed from the recorded statement")

// ErrUnknownState marks a reconciled state this build has no rule for, for errors.Is.
var ErrUnknownState = errors.New("this build does not know that migration state")

// ErrNoDownAuthored marks a migration whose source carries no down of its own, for the engine to derive one.
var ErrNoDownAuthored = errors.New("this migration authors no down of its own")

// ErrDeclaredIrreversible marks a migration whose author stated it cannot be undone, for errors.Is.
var ErrDeclaredIrreversible = errors.New("this migration declares that it cannot be undone")

// DeclaredIrreversibleError reports a migration its author stated cannot be undone, and why.
type DeclaredIrreversibleError struct {
	// Reason is what its author gave for it having no undo, which invariant #10 requires.
	Reason string
}

// Error states the declaration and the reason, leaving the migration to whoever names it.
func (e *DeclaredIrreversibleError) Error() string {
	return ErrDeclaredIrreversible.Error() + ": " + e.Reason
}

// Unwrap reports the sentinel so errors.Is(err, ErrDeclaredIrreversible) succeeds.
func (e *DeclaredIrreversibleError) Unwrap() error {
	return ErrDeclaredIrreversible
}

// DeclaredIrreversible builds the refusal a source reports for a migration declaring it has no undo.
func DeclaredIrreversible(reason string) error {
	return &DeclaredIrreversibleError{Reason: reason}
}

// ErrMissing marks a version the database carries that no migration file describes, for errors.Is.
var ErrMissing = errors.New("the database carries a version no migration file describes")

// ErrNoSteps marks a step count that would leave a run with nothing to act on, for errors.Is.
var ErrNoSteps = errors.New("a step count must name at least one migration")

// ErrUnknownVersion marks a version to stop at that this run reconciled to nothing, for errors.Is.
var ErrUnknownVersion = errors.New("no migration in this run carries that version")

// ErrNotApplied marks a version to roll back to that the database does not carry, for errors.Is.
var ErrNotApplied = errors.New("this database does not carry that version")

// ErrNoBatch marks a batch selector given to an apply, which has no batch to select.
var ErrNoBatch = errors.New("only a rollback can act on the last batch")

// ErrUnknownScope marks a selector this build has no rule for, for errors.Is.
var ErrUnknownScope = errors.New("this build does not know that way of narrowing a run")

// ErrUnknownDirection marks a direction that is neither of the two a migration runs, for errors.Is.
var ErrUnknownDirection = errors.New("this build does not know that direction")

// unknownDirectionError reports a direction that is neither up nor down, naming what was asked for.
func unknownDirectionError(dir ir.Direction) error {
	return fmt.Errorf("%w: %q is neither %q nor %q", ErrUnknownDirection, dir, ir.Up, ir.Down)
}

// ErrUnknownMode marks a mode that is neither of the two a migration is asked in, for errors.Is.
var ErrUnknownMode = errors.New("this build does not know that mode")

// unknownModeError reports a mode that is neither a run nor a question, naming what was asked for.
func unknownModeError(mode Mode) error {
	return fmt.Errorf("%w: %q is not one of %v", ErrUnknownMode, mode, Modes())
}

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

// DriftError reports an applied migration whose file no longer hashes to what the run recorded.
type DriftError struct {
	// Migration is the migration that was edited, so the message names it (invariant #9).
	Migration migration.Migration
	// Version is the migration's UTC timestamp, which names it when no file carries one.
	Version string
	// Direction is the run this refused, since drift refuses a rollback for a stronger reason.
	Direction ir.Direction
	// Recorded is the checksum the run wrote, both halves, exactly as the history row holds it.
	Recorded checksum.Checksum
	// Source is what the file hashes to now, and it is the only half drift is decided on.
	Source checksum.Digest
	// AppliedAt is when the run that recorded it started.
	AppliedAt time.Time
	// Batch is the batch that run belonged to.
	Batch int64
	// Ordinal is its position within that batch.
	Ordinal int32
	// Resuming reports whether the run this refused was a resume, which the two cannot share advice.
	Resuming bool
}

// driftTemplate renders the two values on their own lines, since comparing 64 hex characters is the message's whole job.
const driftTemplate = `%s: %s
The source half of its checksum moved, and it is the only half drift is decided on:

  recorded   %s
             recorded %s, batch %d ordinal %d
  on disk    source %s

The recorded IR half is what that run emitted, and nothing here recomputes it: doing so
needs a language runtime and a live database, and a migration may branch on live query
results, so a mismatch there would not be drift (ADR 0009).

%s
No flag rewrites a recorded checksum (invariant #6), and this run stopped before its
first statement.%s`

// driftHeadline says what the edited file was recorded as, since the two refusals are not the same event.
const (
	appliedAndEdited = "an applied migration has been edited since it was applied."
	stoppedAndEdited = "this migration stopped partway, and its file has been edited since."
)

// driftAdvice says what to do about it, which differs because only one of the two can be resumed.
const (
	rewriteOrReauthor = "Restore the file to the bytes that were applied, or write a new " +
		"migration for the change."
	rewriteOrAssert = "Restore the file to the bytes that ran and resume, or record what you did\n" +
		"about it yourself with --applied or --rolled-back."
)

// undoingDrift is the further reason a rollback refuses, which an apply has no equivalent of.
const undoingDrift = "\n\nA rollback refuses for a stronger reason than an apply does: the " +
	"undo about to run\nwould be authored by bytes that are not the ones that ran."

// Error names the migration, both values in full, and which half moved.
func (e *DriftError) Error() string {
	return fmt.Sprintf(driftTemplate,
		describeMigration(e.Migration, e.Version),
		describeEdited(e.Resuming),
		e.Recorded.String(),
		e.AppliedAt.UTC().Format(stamp),
		e.Batch,
		e.Ordinal,
		e.Source.String(),
		describeRemedy(e.Resuming),
		describeDirection(e.Direction),
	)
}

// describeEdited says what the run that recorded this file left behind, which is not one thing.
func describeEdited(resuming bool) string {
	if resuming {
		return stoppedAndEdited
	}

	return appliedAndEdited
}

// describeRemedy says what to do about the edit, which only a resumable migration may resume from.
func describeRemedy(resuming bool) string {
	if resuming {
		return rewriteOrAssert
	}

	return rewriteOrReauthor
}

// Unwrap reports the sentinel so errors.Is(err, ErrDrifted) succeeds.
func (e *DriftError) Unwrap() error {
	return ErrDrifted
}

// describeDirection adds what a rollback owes an operator beyond what an apply does.
func describeDirection(dir ir.Direction) string {
	if dir != ir.Down {
		return ""
	}

	return undoingDrift
}

// driftedError refuses the whole run, naming the migration edited after the run that recorded it.
func driftedError(st history.Status, dir ir.Direction, resuming bool) error {
	return &DriftError{
		Migration: st.Migration,
		Version:   st.Version,
		Direction: dir,
		Recorded:  st.Recorded.Checksum,
		Source:    st.Source,
		AppliedAt: st.Recorded.AppliedAt,
		Batch:     st.Recorded.Batch,
		Ordinal:   st.Recorded.Ordinal,
		Resuming:  resuming,
	}
}

// DirtyError reports a run whose statements outlived the failure, with nothing here to undo them.
type DirtyError struct {
	// Migration is the migration that stopped partway, so the message names it (invariant #9).
	Migration migration.Migration
	// Direction is what the run was performing, since a rollback can leave this state too.
	Direction ir.Direction
	// LastStatement is the 1-based position of the last statement that succeeded, as the row records it.
	LastStatement int32
	// Statements is how many the migration compiled to, so the message can say how far it got.
	Statements int32
	// Batch is the batch the run belonged to.
	Batch int64
	// Ordinal is its position within that batch.
	Ordinal int32
	// TransactionalDDL is what the session declared, since a false one is what most needs explaining.
	TransactionalDDL bool
	// Err is what stopped it, a *StatementError where a statement did.
	Err error
}

// dirtyTemplate puts the statement that failed under the range that did not, since both are acted on.
const dirtyTemplate = `this migration left the schema between states -- statements 1 through %d of
%d succeeded and nothing rolled them back.

  failed at   %v
  recorded    batch %d, ordinal %d, dirty at statement %d
%s
Both up and down refuse against this database until the dirty state is resolved explicitly, and
nothing here undoes it -- nothing here knows what the statement that failed left behind.`

// autoCommitting is why a session's own declaration explains the window, when it is the reason.
const autoCommitting = "\nThis session reports no transactional DDL, so a migration is not one " +
	"all-or-nothing unit here.\n"

// Error names how far the run got, what stopped it, and what the row it wrote says.
func (e *DirtyError) Error() string {
	return fmt.Sprintf(dirtyTemplate,
		e.LastStatement,
		e.Statements,
		e.Err,
		e.Batch,
		e.Ordinal,
		e.LastStatement,
		describeAtomicity(e.TransactionalDDL),
	)
}

// Unwrap reports the sentinel and the cause, so errors.Is finds either.
func (e *DirtyError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrDirty}
	}

	return []error{ErrDirty, e.Err}
}

// describeAtomicity explains the window with the session's own declaration, where that is the reason.
func describeAtomicity(transactional bool) string {
	if transactional {
		return ""
	}

	return autoCommitting
}

// DirtyStateError refuses a run against a database whose last run left a migration between states.
type DirtyStateError struct {
	// Migration is the file this version was found in, zero where the database carries one no file describes.
	Migration migration.Migration
	// Version is the migration's UTC timestamp, which names it when no file carries one.
	Version string
	// Direction is what the run that stopped was performing, since a rollback leaves this state too.
	Direction ir.Direction
	// LastStatement is the 1-based position of the last statement that succeeded, as the row records it.
	LastStatement int32
	// Batch is the batch that run belonged to.
	Batch int64
	// Ordinal is its position within that batch.
	Ordinal int32
	// RecordedAt is when that run started.
	RecordedAt time.Time
	// Cause is what the row says stopped it, which is text because a history column holds text.
	Cause string
}

// dirtyStateTemplate puts what survived above what did not, since the whole refusal is that pair.
const dirtyStateTemplate = `%s: this migration's last run left the schema between states.

  applied      %s, and nothing rolled them back
  stopped by   %s
  not applied  whatever that run had left after statement %d -- the row records how far it got,
               not how far it had to go, and nothing here recomputes that: the statements were
               the ones that run emitted, and a migration may branch on live query results
               (ADR 0009)
  recorded     batch %d, ordinal %d, at %s

Neither up nor down runs against this database until the dirty state is resolved explicitly, and
nothing here undoes it -- nothing here knows what the statement that failed left behind. migr
status and migr history print this row without failing, and migr resolve is what settles it:
--resume runs on from statement %d, and --applied or --rolled-back record what you did yourself.`

// Error names what survived the run that stopped, what stopped it, and what the row does not say.
func (e *DirtyStateError) Error() string {
	return fmt.Sprintf(dirtyStateTemplate,
		describeMigration(e.Migration, e.Version),
		describeSurvived(e.LastStatement, e.Direction),
		describeStopped(e.Cause),
		e.LastStatement,
		e.Batch,
		e.Ordinal,
		e.RecordedAt.UTC().Format(stamp),
		e.LastStatement,
	)
}

// Unwrap reports the sentinel so errors.Is(err, ErrDirty) succeeds, and nothing else.
func (e *DirtyStateError) Unwrap() error {
	return ErrDirty
}

// describeSurvived names the statements that are in the database, and which run put them there.
func describeSurvived(last int32, dir ir.Direction) string {
	if last == 1 {
		return fmt.Sprintf("statement 1 of the %s run", dir)
	}

	return fmt.Sprintf("statements 1 through %d of the %s run", last, dir)
}

// describeStopped renders what the row says ended the run, saying so when the row says nothing.
func describeStopped(cause string) string {
	if strings.TrimSpace(cause) == "" {
		return "nothing the row records, which is a bug in whatever wrote it"
	}

	return cause
}

// dirtyError refuses the whole run, naming the migration whose last run stopped partway.
func dirtyError(st history.Status) error {
	return &DirtyStateError{
		Migration:     st.Migration,
		Version:       st.Version,
		Direction:     st.Recorded.Direction,
		LastStatement: st.Recorded.LastStatement,
		Batch:         st.Recorded.Batch,
		Ordinal:       st.Recorded.Ordinal,
		RecordedAt:    st.Recorded.AppliedAt,
		Cause:         st.Recorded.Error,
	}
}

// missingError refuses the whole rollback, naming a version the database carries and no file does.
func missingError(st history.Status) error {
	return fmt.Errorf("%w: %s -- a rollback runs newest first, so stepping around it would undo "+
		"the migrations applied after it and leave a schema nothing describes. migr up skips such "+
		"a version because it has nothing to run for it, and this run has",
		ErrMissing, describeVersion(st))
}

// DirectIrreversibleError reports a migration whose own statements no derived undo can invert.
type DirectIrreversibleError struct {
	// Direct are the statements the migration runs itself, which is what has no inverse.
	Direct []driver.Query
}

// Error names how many statements the migration runs itself and what the first of them is.
func (e *DirectIrreversibleError) Error() string {
	return fmt.Sprintf("it runs %s of its own, which the escape hatch gives no inverse: %s",
		describeDirect(len(e.Direct)), firstDirect(e.Direct))
}

// Unwrap reports ir's sentinel so this refusal reads as the irreversibility it is.
func (e *DirectIrreversibleError) Unwrap() error {
	return ir.ErrIrreversible
}

// describeDirect counts statements a migration runs itself with the right noun.
func describeDirect(n int) string {
	if n == 1 {
		return "1 statement"
	}

	return fmt.Sprintf("%d statements", n)
}

// firstDirect renders the first statement a migration runs itself, on one line.
func firstDirect(direct []driver.Query) string {
	if len(direct) == 0 {
		return ""
	}

	return strings.Join(strings.Fields(direct[0].SQL), " ")
}

// directIrreversibleError refuses a derived undo for a migration that reached the database itself.
func directIrreversibleError(direct []driver.Query) error {
	return irreversibleError(&DirectIrreversibleError{Direct: direct})
}

// irreversibleError refuses the whole rollback, naming what has no inverse and why nothing ran.
func irreversibleError(cause error) error {
	return fmt.Errorf("nothing here can undo it: %w -- and the whole rollback is refused before "+
		"anything runs, because one that stopped at an operation with no inverse would leave a "+
		"schema no migration describes (ADR 0008); author a down for this migration to roll it back",
		cause)
}

// stepsError refuses a step count that names no migration, which is a typo rather than a request.
func stepsError(steps int32) error {
	return fmt.Errorf("%w, and %d names none -- a run that would act on nothing is a mistyped "+
		"count rather than a request for one, and omitting the flag is how you ask for everything",
		ErrNoSteps, steps)
}

// blankVersionError refuses a version to stop at that names nothing, before anything is compared.
func blankVersionError() error {
	return fmt.Errorf("%w: the version to stop at is blank", ErrUnknownVersion)
}

// unknownVersionError refuses a version to stop at that this run reconciled to nothing at all.
func unknownVersionError(version string, reconciled []string) error {
	return fmt.Errorf("%w: %q -- this run reconciled %s. A version that is not one of those is "+
		"either a typo or a migration this run was never given, and applying every migration or "+
		"none of them are both worse answers than stopping",
		ErrUnknownVersion, version, describeVersions(reconciled))
}

// notAppliedError refuses leaving a database at a version it does not carry, which it cannot do.
func notAppliedError(version string, applied []string) error {
	return fmt.Errorf("%w: %q, so it cannot be left at it -- this database carries %s. Rolling "+
		"back to a version that is not applied would have to guess whether you meant to stop "+
		"before it or to undo everything",
		ErrNotApplied, version, describeVersions(applied))
}

// noBatchError refuses a batch selector on an apply, where nothing queued has been in a batch.
func noBatchError() error {
	return fmt.Errorf("%w: a pending migration has never run, so it belongs to no batch and there "+
		"is no last one to act on. Going up, --step and --to are what narrow a run", ErrNoBatch)
}

// unknownScopeError refuses a run rather than guessing at a selector this build has no rule for.
func unknownScopeError(kind string) error {
	return fmt.Errorf("%w: %q", ErrUnknownScope, kind)
}

// describeVersions lists the versions a refusal can offer instead, saying so plainly when none.
func describeVersions(versions []string) string {
	if len(versions) == 0 {
		return "no migrations at all"
	}

	return strings.Join(versions, ", ")
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
	return describeMigration(st.Migration, st.Version)
}

// describeMigration names a migration by its filename, falling back to the version a file cannot.
func describeMigration(m migration.Migration, version string) string {
	if m == (migration.Migration{}) {
		return "version " + version
	}

	return m.Filename()
}
