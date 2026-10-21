package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
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
	// Destructive is what a refused migration would have destroyed, present only on that refusal.
	Destructive *destroys `json:"destructive,omitempty"`
	// Capability is what this server will not perform, present only on that refusal.
	Capability *refused `json:"capability,omitempty"`
	// Unrestorable is what this database's schema contradicts, present only on that refusal.
	Unrestorable *contradicted `json:"unrestorable,omitempty"`
	// Lock is every operation over safety.max_lock_impact, present only where --require-safe failed on one.
	Lock *overLimit `json:"lock,omitempty"`
	// Waiting is where a shadow-table migration a run stopped holding stands, present only on exit 6.
	Waiting *shadowDoc `json:"waiting,omitempty"`
}

// overLimit is the ceiling a gate held a plan to, and every operation that exceeded it.
type overLimit struct {
	// Ceiling is safety.max_lock_impact as migr.yaml sets it.
	Ceiling capability.LockImpact `json:"max_lock_impact"`
	// Operations is every operation over it, in the order they would run.
	Operations []exceeding `json:"operations"`
}

// exceeding is one operation over the ceiling, and what it costs.
type exceeding struct {
	// File is the migration it is in.
	File string `json:"file"`
	// Direction is the half it is in.
	Direction ir.Direction `json:"direction"`
	// Index is its place in that half, counting from one.
	Index int32 `json:"index"`
	// Kind is the operation.
	Kind ir.Kind `json:"kind"`
	// Subject is the object it acts on.
	Subject string `json:"subject"`
	// Lock is what it costs a live database as it would run.
	Lock capability.LockImpact `json:"lock"`
	// Rewrite is the safer sequence it would run as, absent where it runs as declared.
	Rewrite driver.RewriteName `json:"rewrite,omitempty"`
	// Assessed reports a verdict a session read the table for.
	Assessed bool `json:"assessed,omitempty"`
}

// contradicted is what a migration records about the schema that the live schema denies.
type contradicted struct {
	// Version is the migration's UTC timestamp, which names it when no file carries one.
	Version string `json:"version"`
	// Direction is the half that was asked for, since a rollback is checked for the same reason.
	Direction ir.Direction `json:"direction"`
	// Operations is every disagreement, in the order the migration emitted the operations.
	Operations []disagreed `json:"operations"`
}

// refused is what the connected server declares it will not perform, and what asked it.
type refused struct {
	// Version is the migration's UTC timestamp, which names it when no file carries one.
	Version string `json:"version"`
	// Direction is the half that was asked for, since a rollback is refused for the same reason.
	Direction ir.Direction `json:"direction"`
	// Driver is the driver whose declaration refused.
	Driver string `json:"driver"`
	// Server is the version that declaration was resolved against, absent where none was detected.
	Server string `json:"server,omitempty"`
	// Operations is every operation this server refuses, in the order the migration emitted them.
	Operations []unperformable `json:"operations"`
}

// unperformable is one operation the server declares it will not perform, as fields.
type unperformable struct {
	// Operation is its 1-based place in the half the migration emitted.
	Operation int32 `json:"operation"`
	// Kind is the operation at fault.
	Kind ir.Kind `json:"kind"`
	// Subject is the object it acts on, named the way the operation names it.
	Subject string `json:"subject"`
	// Reason is the driver's own stated reason, carried as the driver wrote it.
	Reason string `json:"reason"`
}

// destroys is what a refused migration would have lost, and what already permits what.
type destroys struct {
	// Version is the migration's UTC timestamp, which names it when no file carries one.
	Version string `json:"version"`
	// Allowed is the kinds this project permits, which is empty where it permits none.
	Allowed []ir.Kind `json:"allowed"`
	// Needed is the kinds a run would have to permit for this migration, each named once.
	Needed []ir.Kind `json:"needed"`
	// Operations is every operation that destroys something, in the order the migration emitted them.
	Operations []destroyed `json:"operations"`
}

// destroyed is one operation that destroys something, as fields rather than as a line of prose.
type destroyed struct {
	// Operation is its 1-based place in the half the migration emitted.
	Operation int32 `json:"operation"`
	// Kind is the operation, which is also the name that permits it.
	Kind ir.Kind `json:"kind"`
	// Subject is what it destroys, named the way the operation names it.
	Subject string `json:"subject"`
	// Detail states what would be lost, in one line.
	Detail string `json:"detail"`
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

// The two senses ErrDirty covers, which engine distinguishes by type and this document by name.
const (
	senseBecame  = "became"
	senseAlready = "already"
)

// halted is how far a run got before it left the schema between states, as fields.
type halted struct {
	// Sense is whether this run became dirty or the database already was, which ADR 0024 separates.
	Sense string `json:"sense"`
	// Version is the migration's UTC timestamp, which names it when no file carries one.
	Version string `json:"version"`
	// Direction is what the run was performing, since a rollback can leave this state too.
	Direction ir.Direction `json:"direction"`
	// LastStatement is the 1-based position of the last statement that succeeded.
	LastStatement int32 `json:"last_statement"`
	// Statements is how many the migration compiled to, which only the run that just failed knows.
	Statements int32 `json:"statements,omitempty"`
	// Batch is the batch the run belonged to.
	Batch int64 `json:"batch"`
	// Ordinal is its position within that batch.
	Ordinal int32 `json:"ordinal"`
	// RecordedAt is when the run started, which only a row read back from the history carries.
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
	// TransactionalDDL is what the session declared, and only the run that just failed was on it.
	TransactionalDDL *bool `json:"transactional_ddl,omitempty"`
	// Cause is what stopped it, in one line.
	Cause string `json:"cause"`
	// Chunk is how far a migration running in chunks got, absent for one that ran statements.
	Chunk *checkpoint `json:"chunk,omitempty"`
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
	found.describeDirtyState(err)
	found.describeDestructive(err)
	found.describeCapability(err)
	found.describeUnrestorable(err)
	found.describeLock(err)
	found.describeWaiting(err)

	return found
}

// describeWaiting replaces the paragraph a detached hold renders with where its shadow table stands.
func (f *failure) describeWaiting(err error) {
	var waiting *engine.WaitingError
	if !errors.As(err, &waiting) {
		return
	}

	f.Error = engine.ErrAwaitingCutover.Error()
	f.Migration = describeFile(waiting.Migration)
	f.Waiting = waitingOf(waiting.State, waiting.State.UpdatedAt)
}

// describeLock replaces the paragraph a gate failure renders with every operation over the ceiling.
func (f *failure) describeLock(err error) {
	var over *engine.LockImpactError
	if !errors.As(err, &over) || len(over.Exceeded) == 0 {
		return
	}

	f.Error = engine.ErrLockImpact.Error()
	f.Migration = describeFile(over.Exceeded[0].Migration)
	f.Lock = &overLimit{Ceiling: over.Ceiling}

	for _, m := range over.Exceeded {
		for _, b := range m.Found {
			if f.Operation == "" {
				f.Operation = b.Kind
			}

			f.Lock.Operations = append(f.Lock.Operations, exceeding{File: describeFile(m.Migration),
				Direction: m.Direction, Index: b.Operation, Kind: b.Kind, Subject: b.Subject, Lock: b.Lock,
				Rewrite: b.Rewrite, Assessed: b.Assessed})
		}
	}
}

// describeUnrestorable replaces the paragraph a contradicted record renders with what disagrees.
func (f *failure) describeUnrestorable(err error) {
	var unrestorable *engine.UnrestorableError
	if !errors.As(err, &unrestorable) {
		return
	}

	f.Error = engine.ErrUnrestorable.Error()
	f.Migration = describeFile(unrestorable.Migration)
	f.Operation = firstDisagreement(unrestorable.Found)
	f.Unrestorable = &contradicted{
		Version:    unrestorable.Version,
		Direction:  unrestorable.Direction,
		Operations: disagreementsOf(unrestorable.Found),
	}
}

// firstDisagreement names the operation a refusal reached first, which invariant #9 asks for.
func firstDisagreement(found []engine.Disagreement) ir.Kind {
	if len(found) == 0 {
		return ""
	}

	return found[0].Kind
}

// describeCapability replaces the paragraph a server's refusal renders with what it will not perform.
func (f *failure) describeCapability(err error) {
	var unsupported *engine.UnsupportedError
	if !errors.As(err, &unsupported) {
		return
	}

	f.Error = engine.ErrUnsupported.Error()
	f.Migration = describeFile(unsupported.Migration)
	f.Operation = firstRefused(unsupported.Found)
	f.Capability = &refused{
		Version:    unsupported.Version,
		Direction:  unsupported.Direction,
		Driver:     unsupported.Driver,
		Server:     unsupported.Server,
		Operations: unperformableIn(unsupported.Found),
	}
}

// firstRefused names the operation a refusal reached first, which invariant #9 asks every failure for.
func firstRefused(found []engine.Refusal) ir.Kind {
	if len(found) == 0 {
		return ""
	}

	return found[0].Kind
}

// unperformableIn renders every operation a server refuses, in the order the migration emitted them.
func unperformableIn(found []engine.Refusal) []unperformable {
	out := make([]unperformable, 0, len(found))
	for _, r := range found {
		out = append(out, unperformable{
			Operation: r.Operation,
			Kind:      r.Kind,
			Subject:   r.Subject,
			Reason:    r.Reason,
		})
	}

	return out
}

// describeDestructive replaces the paragraph a refused migration renders with what it would destroy.
func (f *failure) describeDestructive(err error) {
	var refused *engine.DestructiveError
	if !errors.As(err, &refused) {
		return
	}

	f.Error = engine.ErrDestructive.Error()
	f.Migration = describeFile(refused.Migration)
	f.Destructive = &destroys{
		Version:    refused.Version,
		Allowed:    permitted(refused.Allowed),
		Needed:     engine.Needed(refused.Found),
		Operations: destroyedBy(refused.Found),
	}
}

// permitted renders what a project allows, as a list rather than as the null an empty one would be.
func permitted(allowed []ir.Kind) []ir.Kind {
	if allowed == nil {
		return []ir.Kind{}
	}

	return allowed
}

// destroyedBy renders every operation a refusal found, in the order the migration emitted them.
func destroyedBy(found []engine.Destruction) []destroyed {
	out := make([]destroyed, 0, len(found))
	for _, d := range found {
		out = append(out, destroyed{
			Operation: d.Operation,
			Kind:      d.Kind,
			Subject:   d.Subject,
			Detail:    d.Detail,
		})
	}

	return out
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
		Sense:            senseBecame,
		Version:          stopped.Migration.Version,
		Direction:        stopped.Direction,
		LastStatement:    stopped.LastStatement,
		Statements:       stopped.Statements,
		Batch:            stopped.Batch,
		Ordinal:          stopped.Ordinal,
		TransactionalDDL: &stopped.TransactionalDDL,
		Cause:            describeCause(stopped.Err),
		Chunk:            checkpointOf(stopped.Checkpoint),
	}
}

// describeDirtyState replaces the paragraph a refused run renders with the row it was built from.
func (f *failure) describeDirtyState(err error) {
	var between *engine.DirtyStateError
	if !errors.As(err, &between) {
		return
	}

	recorded := between.RecordedAt.UTC()
	f.Error = engine.ErrDirty.Error()
	f.Migration = describeFile(between.Migration)
	f.Dirty = &halted{
		Sense:         senseAlready,
		Version:       between.Version,
		Direction:     between.Direction,
		LastStatement: between.LastStatement,
		Batch:         between.Batch,
		Ordinal:       between.Ordinal,
		RecordedAt:    &recorded,
		Cause:         between.Cause,
		Chunk:         checkpointOf(between.Checkpoint),
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
