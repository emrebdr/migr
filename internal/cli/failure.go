package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
)

// failure is what a command failed with, as fields, so --json never carries a paragraph.
type failure struct {
	// Code is the exit code this failure produced, which is the same one the process returns.
	Code int `json:"code"`
	// Error states what went wrong in one line, since the prose belongs to the terminal rendering.
	Error string `json:"error"`
	// Migration is the file the failure happened in, absent for one that is about no migration.
	Migration string `json:"migration,omitempty"`
	// Operation is the operation it came from, absent where the failure named none.
	Operation ir.Kind `json:"operation,omitempty"`
	// Statement is the 1-based position of the statement that failed, absent where none did.
	Statement int32 `json:"statement,omitempty"`
	// Drift is what an edited migration moved between, present only on a drift refusal.
	Drift *drifted `json:"drift,omitempty"`
	// Dirty is how far a run got before it stopped, present only on a dirty failure.
	Dirty *halted `json:"dirty,omitempty"`
}

// drifted is what an applied migration moved between, as fields rather than as the paragraph.
type drifted struct {
	// Version is the migration's UTC timestamp, which names it when no file carries one.
	Version string `json:"version"`
	// Direction is the run drift refused, since a rollback refuses for a stronger reason.
	Direction ir.Direction `json:"direction"`
	// RecordedSource is the source digest the run wrote, and the only half drift is decided on.
	RecordedSource string `json:"recorded_source"`
	// CurrentSource is what the file hashes to now.
	CurrentSource string `json:"current_source"`
	// RecordedChecksum is the whole recorded value, exactly as the history row holds it.
	RecordedChecksum string `json:"recorded_checksum"`
	// AppliedAt is when the run that recorded it started.
	AppliedAt time.Time `json:"applied_at"`
	// Batch is the batch that run belonged to.
	Batch int64 `json:"batch"`
	// Ordinal is its position within that batch.
	Ordinal int32 `json:"ordinal"`
}

// halted is how far a run got before it left the schema between states, as fields.
type halted struct {
	// Direction is what the run was performing, since a rollback can leave this state too.
	Direction ir.Direction `json:"direction"`
	// LastStatement is the 1-based position of the last statement that succeeded.
	LastStatement int32 `json:"last_statement"`
	// Statements is how many the migration compiled to, so a reader can say how far it got.
	Statements int32 `json:"statements"`
	// Batch is the batch the run belonged to.
	Batch int64 `json:"batch"`
	// Ordinal is its position within that batch.
	Ordinal int32 `json:"ordinal"`
	// TransactionalDDL is what the session declared, since a false one is what most needs explaining.
	TransactionalDDL bool `json:"transactional_ddl"`
	// Cause is what stopped it, in one line.
	Cause string `json:"cause"`
}

// reportFailure writes what failed a command to stderr, as fields under --json and as itself otherwise.
func reportFailure(w io.Writer, opts *options, err error) error {
	if opts.jsonOut {
		return writeFailure(w, err)
	}

	if _, werr := fmt.Fprintf(w, "migr: %v\n", err); werr != nil {
		return fmt.Errorf("reporting the failure: %w", werr)
	}

	return nil
}

// writeFailure renders a failure as the one JSON object per line stderr carries under --json.
func writeFailure(w io.Writer, err error) error {
	if encErr := json.NewEncoder(w).Encode(failureOf(err)); encErr != nil {
		return fmt.Errorf("reporting the failure: %w", encErr)
	}

	return nil
}

// failureOf reads whatever failed a command for the fields --json renders it as.
func failureOf(err error) failure {
	found := failure{Code: ExitCode(err), Error: err.Error()}

	found.name(err)
	found.locate(err)
	found.describeDrift(err)
	found.describeDirty(err)

	return found
}

// name reports the migration and the operation a failure came from, where it names one.
func (f *failure) name(err error) {
	var failed *engine.MigrationError
	if errors.As(err, &failed) {
		f.Migration = failed.Migration.Filename()
		f.Operation = kindOf(failed.Operation)
	}

	var refused *plan.CompileError
	if errors.As(err, &refused) {
		f.Migration = refused.Migration.Filename()
		f.Operation = refused.Kind
	}
}

// locate reports the statement a failure happened at, where a statement is what failed.
func (f *failure) locate(err error) {
	var failed *engine.StatementError
	if errors.As(err, &failed) {
		f.Statement = failed.Position
	}
}

// describeDrift replaces the paragraph an edited migration renders with the values it is built from.
func (f *failure) describeDrift(err error) {
	var moved *engine.DriftError
	if !errors.As(err, &moved) {
		return
	}

	f.Error = engine.ErrDrifted.Error()
	f.Migration = describeFile(moved.Migration)
	f.Drift = &drifted{
		Version:          moved.Version,
		Direction:        moved.Direction,
		RecordedSource:   moved.Recorded.Source.String(),
		CurrentSource:    moved.Source.String(),
		RecordedChecksum: moved.Recorded.String(),
		AppliedAt:        moved.AppliedAt.UTC(),
		Batch:            moved.Batch,
		Ordinal:          moved.Ordinal,
	}
}

// describeDirty replaces the paragraph a half-run migration renders with the values it is built from.
func (f *failure) describeDirty(err error) {
	var stopped *engine.DirtyError
	if !errors.As(err, &stopped) {
		return
	}

	f.Error = engine.ErrDirty.Error()
	f.Migration = describeFile(stopped.Migration)
	f.Dirty = &halted{
		Direction:        stopped.Direction,
		LastStatement:    stopped.LastStatement,
		Statements:       stopped.Statements,
		Batch:            stopped.Batch,
		Ordinal:          stopped.Ordinal,
		TransactionalDDL: stopped.TransactionalDDL,
		Cause:            describeCause(stopped.Err),
	}
}

// kindOf names the operation a failure came from, which one that came from no operation has none of.
func kindOf(op ir.Operation) ir.Kind {
	if op == nil {
		return ""
	}

	return op.Kind()
}

// describeCause states what stopped a run in one line, which a run stopped by nothing has none of.
func describeCause(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}
