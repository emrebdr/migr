package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// RepairMode is what an operator asked be done about a migration left between states.
type RepairMode string

// The three repairs. The zero RepairMode names none of them and asks for nothing.
const (
	// RepairResume runs the statements the dirty run never reached, in the direction it was going.
	RepairResume RepairMode = "resume"
	// RepairApplied records that the migration's changes are in the database, run by hand.
	RepairApplied RepairMode = "applied"
	// RepairRolledBack records that they are not, undone by hand.
	RepairRolledBack RepairMode = "rolled-back"
)

// repairs lists every repair, from the one migr performs to the two an operator asserts.
var repairs = []RepairMode{RepairResume, RepairApplied, RepairRolledBack}

// Repairs returns every way a dirty migration may be resolved, as a copy.
func Repairs() []RepairMode {
	return slices.Clone(repairs)
}

// Valid reports whether a value names one of the repairs.
func (m RepairMode) Valid() bool {
	return slices.Contains(repairs, m)
}

// String returns the repair's wire name, which is the flag an operator typed.
func (m RepairMode) String() string {
	return string(m)
}

// Asserts reports whether this repair records what an operator did rather than running anything.
func (m RepairMode) Asserts() bool {
	return m == RepairApplied || m == RepairRolledBack
}

// direction reports which way an asserted repair leaves the migration, which the fold reads.
func (m RepairMode) direction() ir.Direction {
	if m == RepairRolledBack {
		return ir.Down
	}

	return ir.Up
}

// Repair is what one invocation of a resolution was asked to do.
type Repair struct {
	// Mode is which repair was asked for.
	Mode RepairMode
	// Reason is the operator's account of what they did, which only an asserted repair carries.
	Reason string
	// DryRun stops after the plan, having taken the lock and changed nothing.
	DryRun bool
}

// Check refuses a repair nothing can act on, before a lock is taken that could not be used.
func (r Repair) Check() error {
	if !r.Mode.Valid() {
		return unknownRepairError(r.Mode)
	}

	if r.Mode.Asserts() && strings.TrimSpace(r.Reason) == "" {
		return missingReasonError(r.Mode)
	}

	if !r.Mode.Asserts() && strings.TrimSpace(r.Reason) != "" {
		return unwantedReasonError(r.Mode)
	}

	return nil
}

// Resolution is what a resolution found, what it would do about it, and what it did.
type Resolution struct {
	// Mode is the repair this invocation was asked for.
	Mode RepairMode
	// Migration is the migration that was between states, zero where no file carries the version.
	Migration migration.Migration
	// Version is its UTC timestamp, which names it when no file does.
	Version string
	// Direction is the direction this repair records, which a resume takes from the run that stopped.
	Direction ir.Direction
	// Stopped is the run that left the migration between states, exactly as the history row holds it.
	Stopped history.Record
	// Statements is how many the migration compiles to, zero on a repair that compiled nothing.
	Statements int32
	// Plan is the whole migration compiled, of which a resume runs the statements after LastStatement.
	Plan plan.Plan
	// Batch is the batch this repair's own row is recorded under.
	Batch int64
	// Reason is the operator's account, on an asserted repair.
	Reason string
	// Ran reports whether this invocation changed anything, which a dry run never does.
	Ran bool
	// Run is what a resume executed, absent on a dry run and on an asserted repair.
	Run *Run
}

// Pending reports how many statements a resume has left to run, which is zero where all of them ran.
func (r Resolution) Pending() int32 {
	return max(r.Statements-r.Stopped.LastStatement, 0)
}

// Resolve settles one migration left between states, running what is left or recording what was done.
func Resolve(ctx context.Context, conn driver.Conn, opts Options, ask Repair) (Resolution, error) {
	if err := ask.Check(); err != nil {
		return Resolution{}, err
	}

	e, err := newResolver(conn, opts, ask.Mode)
	if err != nil {
		return Resolution{}, err
	}

	return locked(ctx, e, func(ctx context.Context) (Resolution, error) {
		return e.resolveOne(ctx, ask)
	})
}

// newResolver binds a resolution, checking only what the repair it was asked for actually needs.
func newResolver(conn driver.Conn, opts Options, mode RepairMode) (*executor, error) {
	if !mode.Asserts() {
		return newExecutor(conn, opts, ModeApply)
	}

	if err := opts.checkRecording(); err != nil {
		return nil, err
	}

	return bind(conn, opts, ModeApply)
}

// resolveOne folds the history, finds the one migration between states, and acts on it.
func (e *executor) resolveOne(ctx context.Context, ask Repair) (Resolution, error) {
	report, err := e.fold(ctx)
	if err != nil {
		return Resolution{}, err
	}

	st, found, err := soleDirty(report)
	if err != nil || !found {
		return Resolution{}, err
	}

	if e.batch, err = allocateBatch(ctx, e.store); err != nil {
		return Resolution{}, err
	}

	if ask.Mode.Asserts() {
		return e.assert(ctx, st, ask)
	}

	return e.resume(ctx, st, ask)
}

// soleDirty returns the one migration this database left between states, refusing a database with two.
func soleDirty(report history.Report) (history.Status, bool, error) {
	statuses, present := report.Resolved()
	if !present {
		return history.Status{}, false, noHistoryError()
	}

	var dirty []history.Status

	for _, st := range statuses {
		if st.State == history.StateDirty {
			dirty = append(dirty, st)
		}
	}

	if len(dirty) > 1 {
		return history.Status{}, false, severalDirtyError(dirty)
	}

	if len(dirty) == 0 {
		return history.Status{}, false, nil
	}

	return dirty[0], true, nil
}

// noHistoryError refuses a resolution against a database that has never recorded a run at all.
func noHistoryError() error {
	return fmt.Errorf("%w, so nothing here is between states -- and a resolution creates nothing, "+
		"since a command whose whole job is repair may not be the one that invents the record it "+
		"repairs. migr up is what makes a history table", history.ErrNoTable)
}

// assert records what an operator did by hand, compiling nothing and asking no migration anything.
func (e *executor) assert(ctx context.Context, st history.Status, ask Repair) (Resolution, error) {
	settled := Resolution{
		Mode:      ask.Mode,
		Migration: st.Migration,
		Version:   st.Version,
		Direction: ask.Mode.direction(),
		Stopped:   st.Recorded,
		Batch:     e.batch,
		Reason:    ask.Reason,
	}

	if ask.DryRun {
		return settled, nil
	}

	rec := e.assertedRecord(st, settled)
	if err := e.store.MarkResolved(ctx, rec, ask.Reason); err != nil {
		return settled, fmt.Errorf("recording the resolution of %s: %w",
			describeVersion(st), err)
	}

	settled.Ran = true

	return settled, nil
}

// assertedRecord builds the row an asserted repair writes, from the run that went dirty and this session.
func (e *executor) assertedRecord(st history.Status, settled Resolution) history.Record {
	return history.Record{
		Batch:     e.batch,
		Ordinal:   firstOrdinal,
		Version:   st.Version,
		Name:      st.Recorded.Name,
		Direction: settled.Direction,
		Checksum:  st.Recorded.Checksum,
		AppliedAt: e.opts.Clock(),
		Driver:    e.conn.Capabilities().Driver(),
		Runtime:   st.Recorded.Runtime,
	}
}

// resume runs the statements the dirty run never reached, in the direction it was going.
func (e *executor) resume(ctx context.Context, st history.Status, ask Repair) (Resolution, error) {
	dir := st.Recorded.Direction
	if !dir.Valid() {
		return Resolution{}, unknownDirectionError(dir)
	}

	if err := checkResumable(st, dir); err != nil {
		return Resolution{}, err
	}

	item := queued{
		migration: st.Migration, source: st.Source, recorded: st.Recorded,
		state: st.State, ordinal: firstOrdinal,
	}

	ready, err := e.prepareResume(ctx, item, dir, st.Recorded)
	if err != nil {
		return Resolution{}, err
	}

	settled := Resolution{
		Mode:       ask.Mode,
		Migration:  st.Migration,
		Version:    st.Version,
		Direction:  ready.compiled.Direction,
		Stopped:    st.Recorded,
		Statements: ready.compiled.Len(),
		Plan:       ready.compiled,
		Batch:      e.batch,
	}

	if ask.DryRun {
		return settled, nil
	}

	return e.finishResume(ctx, item, settled, ready)
}

// finishResume runs what the dirty run never reached, or records a run that had reached all of it.
func (e *executor) finishResume(ctx context.Context, item queued, settled Resolution,
	ready prepared,
) (Resolution, error) {
	if settled.Pending() == 0 {
		return e.recordComplete(ctx, settled, ready)
	}

	ready, err := e.grouped(ready)
	if err != nil {
		return settled, e.fail(item, plan.Plan{}, err)
	}

	run, err := e.perform(ctx, ready)
	if err != nil {
		return settled, err
	}

	settled.Ran = true
	settled.Run = &run

	return settled, nil
}

// recordComplete records a run whose every statement had already taken effect, running none of them.
func (e *executor) recordComplete(ctx context.Context, settled Resolution,
	ready prepared,
) (Resolution, error) {
	rec := e.newRecord(ready)
	rec.Duration = elapsed(rec.AppliedAt, e.opts.Clock())

	if err := e.store.Record(ctx, rec); err != nil {
		return settled, fmt.Errorf("recording %s, whose every statement had already run: %w",
			describeMigration(settled.Migration, settled.Version), err)
	}

	settled.Ran = true
	settled.Run = &Run{
		Migration: settled.Migration, Ordinal: rec.Ordinal,
		Started: rec.AppliedAt, Duration: rec.Duration,
	}

	return settled, nil
}

// checkResumable refuses a resume whose index would land in a statement list that has moved.
func checkResumable(st history.Status, dir ir.Direction) error {
	if st.Source != st.Recorded.Checksum.Source {
		return driftedError(st, dir, true)
	}

	return nil
}

// prepareResume asks the migration what it does and holds it to what the run that stopped emitted.
func (e *executor) prepareResume(ctx context.Context, item queued, dir ir.Direction,
	rec history.Record,
) (prepared, error) {
	emitted, err := e.asked(ctx, item, dir)
	if err != nil {
		return prepared{}, e.fail(item, plan.Plan{}, err)
	}

	e.from = rec.LastStatement

	ready, err := e.compileFor(ctx, item, dir, emitted)
	if err != nil {
		return prepared{}, e.fail(item, plan.Plan{}, err)
	}

	if ready.sum.IR != rec.Checksum.IR {
		return prepared{}, e.fail(item, plan.Plan{},
			emittedDifferentlyError(rec.Checksum.IR, ready.sum.IR))
	}

	return ready, nil
}

// asked gets what the migration performs in one direction, by the path that direction's run uses.
func (e *executor) asked(ctx context.Context, item queued, dir ir.Direction) (Emitted, error) {
	if dir == ir.Down {
		return e.undo(ctx, item)
	}

	emitted, err := e.emit(ctx, item.migration, dir)
	if err != nil {
		return Emitted{}, fmt.Errorf("asking what this migration does: %w", err)
	}

	return emitted, nil
}

// emittedDifferentlyError refuses a resume whose index no longer names the statement it named.
func emittedDifferentlyError(recorded, now checksum.Digest) error {
	return fmt.Errorf("%w: it emitted a different list of operations this time, so the statement "+
		"the recorded index names is not the statement that would run:\n\n  recorded  %s\n  now       "+
		"%s\n\nA migration may branch on live query results (ADR 0009), so this is permitted rather "+
		"than corrupt -- what it rules out is resuming by index. Finish or undo the change yourself "+
		"and record what you did with --applied or --rolled-back",
		ErrNotResumable, recorded, now)
}
