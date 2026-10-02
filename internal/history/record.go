package history

import (
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/version"
)

// Record is one run of one migration, as a row of the history table holds it.
type Record struct {
	// Batch is the migr invocation this run belonged to; down rolls back a whole batch.
	Batch int64
	// Ordinal is the run's position within its batch, so order survives a clock that ties.
	Ordinal int32
	// Version is the migration's UTC timestamp, the version its filename carries.
	Version string
	// Name is the migration's snake_case name, the rest of its filename.
	Name string
	// Direction is whether the run applied the migration or rolled it back.
	Direction ir.Direction
	// Outcome is how the run ended, set by the store method that wrote the row.
	Outcome Outcome
	// Checksum is what the migration hashed to, and its source half is what decides drift.
	Checksum checksum.Checksum
	// AppliedAt is when the run started, supplied by the caller since this package reads no clock.
	AppliedAt time.Time
	// Duration is how long the run took.
	Duration time.Duration
	// LastStatement is the 1-based position of the last statement that succeeded, on a dirty run.
	LastStatement int32
	// Error is why the run did not complete, or an operator's reason for resolving one by hand.
	Error string
	// Driver is the engine the run targeted, spelled as that driver is registered.
	Driver string
	// Runtime is the language the migration was written in, spelled as migr.yaml spells it.
	Runtime string
	// EngineVersion is the migr build that performed the run, filled in by Stamp.
	EngineVersion string
	// IRVersion is the operation IR version that build emits, filled in by Stamp.
	IRVersion int32
}

// Stamp fills in the two versions describing the build rather than the run.
func (r Record) Stamp() Record {
	r.EngineVersion = version.Version
	r.IRVersion = ir.Version

	return r
}

// Validate reports whether a record carries what a run must record, needing no database.
func (r Record) Validate() error {
	if err := r.checkIdentity(); err != nil {
		return err
	}

	if err := r.checkRun(); err != nil {
		return err
	}

	return r.checkOutcome()
}

// checkIdentity reports whether the record names the migration and the run it belongs to.
func (r Record) checkIdentity() error {
	if strings.TrimSpace(r.Version) == "" {
		return r.incomplete("version", "is required, and is the migration's UTC timestamp")
	}

	if strings.TrimSpace(r.Name) == "" {
		return r.incomplete("name", "is required")
	}

	if !r.Direction.Valid() {
		return r.incomplete("direction", "must be "+string(ir.Up)+" or "+string(ir.Down))
	}

	if r.Batch < 1 {
		return r.incomplete("batch", "must be positive, since batches are numbered from one")
	}

	return r.checkOrdinal()
}

// checkOrdinal reports whether the record says where in its batch the run sat.
func (r Record) checkOrdinal() error {
	if r.Ordinal < 1 {
		return r.incomplete("ordinal", "must be positive, since a batch numbers its runs from one")
	}

	return nil
}

// checkRun reports whether the record says when the run happened and what performed it.
func (r Record) checkRun() error {
	if r.AppliedAt.IsZero() {
		return r.incomplete("applied_at", "is required; this package never reads a clock")
	}

	if r.Duration < 0 {
		return r.incomplete("duration_ms", "cannot be negative")
	}

	if r.Checksum.Zero() {
		return r.incomplete("checksum", "is required, and its source half is what decides drift")
	}

	return r.checkProvenance()
}

// checkProvenance reports whether the record says which engine, language, and build ran it.
func (r Record) checkProvenance() error {
	if strings.TrimSpace(r.Driver) == "" {
		return r.incomplete("driver", "is required")
	}

	if strings.TrimSpace(r.Runtime) == "" {
		return r.incomplete("runtime", "is required")
	}

	if strings.TrimSpace(r.EngineVersion) == "" {
		return r.incomplete("engine_version", "is required; call Stamp before recording")
	}

	if r.IRVersion < 1 {
		return r.incomplete("ir_version", "is required; call Stamp before recording")
	}

	return nil
}

// checkOutcome reports whether the outcome agrees with what the record does and does not carry.
func (r Record) checkOutcome() error {
	if !r.Outcome.Valid() {
		return r.incomplete("outcome", "must be one of "+joinOutcomes())
	}

	if r.Outcome == Applied && strings.TrimSpace(r.Error) != "" {
		return r.incomplete("error", "belongs to a run that did not complete")
	}

	if r.Outcome != Applied && strings.TrimSpace(r.Error) == "" {
		return r.incomplete("error", "is required for a run that did not complete, and is the "+
			"operator's reason on one they resolved by hand")
	}

	return r.checkLastStatement()
}

// checkLastStatement reports whether a dirty run says where it stopped, and others that they did not.
func (r Record) checkLastStatement() error {
	if r.Outcome == Dirty && r.LastStatement < 1 {
		return r.incomplete("last_statement", "must name the statement a dirty run reached; a run "+
			"where none succeeded left the schema unchanged and is failed rather than dirty")
	}

	if r.Outcome != Dirty && r.LastStatement != 0 {
		return r.incomplete("last_statement", "belongs only to a dirty run")
	}

	return nil
}

// incomplete reports a field this record does not carry, naming the migration it describes.
func (r Record) incomplete(field, detail string) error {
	return &IncompleteError{Version: r.Version, Field: field, Detail: detail}
}

// joinOutcomes renders the outcome set for an error message.
func joinOutcomes() string {
	names := make([]string, len(outcomes))
	for i, outcome := range outcomes {
		names[i] = outcome.String()
	}

	return strings.Join(names, ", ")
}
