package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// executor is one run's session and the counters it numbers its own work with.
type executor struct {
	// conn is the pinned session every statement and every history write goes through.
	conn driver.Conn
	// store is that session's history, bound at Open.
	store history.Store
	// opts is what this run was given beyond the session.
	opts Options
	// mode is how this invocation asks a migration what it does: a run, or a question.
	mode Mode
	// batch is what every run in this invocation is recorded under.
	batch int64
	// position counts statements within one migration, continuing across its runs.
	position int32
}

// newExecutor checks what a run was given and binds it to the session's history, before any lock.
func newExecutor(conn driver.Conn, opts Options, mode Mode) (*executor, error) {
	if err := opts.check(); err != nil {
		return nil, err
	}

	if !mode.Valid() {
		return nil, unknownModeError(mode)
	}

	store := conn.History()
	if store == nil {
		return nil, ErrNoHistory
	}

	return &executor{conn: conn, store: store, opts: opts, mode: mode}, nil
}

// emit asks what a migration does, in the mode this invocation runs its migrations in.
func (e *executor) emit(ctx context.Context, m migration.Migration, dir ir.Direction) (ir.Operations, error) {
	return e.opts.Source.Emit(ctx, m, dir, e.mode)
}

// ask asks what a migration does as a question, which no invocation turns into a run.
func (e *executor) ask(ctx context.Context, m migration.Migration, dir ir.Direction) (ir.Operations, error) {
	return e.opts.Source.Emit(ctx, m, dir, ModePlan)
}

// invocation is what one run does under the migration lock: an apply, a rollback, or a preview.
type invocation[T any] func(ctx context.Context) (T, error)

// locked takes the migration lock, runs the invocation, and gives the lock back after it returns.
func locked[T any](ctx context.Context, e *executor, work invocation[T]) (result T, err error) {
	var none T

	unlock, err := e.conn.Lock(ctx)
	if err != nil {
		return none, err
	}

	defer func() { err = errors.Join(err, unlock(context.WithoutCancel(ctx))) }()

	return work(ctx)
}

// reconcile ensures the history table, reads it, and folds it against the migrations it was given.
func (e *executor) reconcile(ctx context.Context) (history.Report, error) {
	if err := e.store.Ensure(ctx); err != nil {
		return history.Report{}, fmt.Errorf("preparing the migration history: %w", err)
	}

	return e.fold(ctx)
}

// fold reads the history and folds it against the migrations it was given, creating nothing.
func (e *executor) fold(ctx context.Context) (history.Report, error) {
	log, err := history.Read(ctx, e.store)
	if err != nil {
		return history.Report{}, fmt.Errorf("reading the migration history: %w", err)
	}

	return history.Reconcile(e.opts.Migrations, log), nil
}

// prepared is one migration's operations, compiled for this driver, grouped, and hashed for its row.
type prepared struct {
	// item is the migration this will run and the ordinal its row records.
	item queued
	// compiled is what the operations became for this engine, in the direction they were compiled in.
	compiled plan.Plan
	// groups are those statements split into the runs that execute them.
	groups []Group
	// sum is the checksum the run is recorded under, over the source and the emitted IR.
	sum checksum.Checksum
}

// prepare hashes what a migration emitted, compiles it, and refuses what cannot run atomically.
func (e *executor) prepare(item queued, dir ir.Direction, ops ir.Operations) (prepared, error) {
	emitted, err := checksum.OfOperations(ops)
	if err != nil {
		return prepared{}, fmt.Errorf("hashing the emitted operations: %w", err)
	}

	compiled, err := e.opts.Planner.Compile(item.migration, dir, ops)
	if err != nil {
		return prepared{}, err
	}

	groups := Groups(compiled.Statements())
	if err := e.checkRunnable(groups); err != nil {
		return prepared{}, err
	}

	return prepared{
		item:     item,
		compiled: compiled,
		groups:   groups,
		sum:      checksum.Checksum{Source: item.source, IR: emitted},
	}, nil
}

// perform runs one prepared migration, recording it inside the last transaction it runs in.
func (e *executor) perform(ctx context.Context, ready prepared) (Run, error) {
	rec := e.newRecord(ready)
	e.position = 0

	survived, err := e.runMigration(ctx, ready.groups, e.recorder(&rec))
	if err != nil {
		return Run{}, e.fail(ready.item, ready.compiled,
			e.recordFailure(ctx, rec, survived, e.leftDirty(ready, survived, err)))
	}

	return Run{
		Migration:  ready.item.migration,
		Ordinal:    ready.item.ordinal,
		Statements: ready.compiled.Len(),
		Started:    rec.AppliedAt,
		Duration:   rec.Duration,
	}, nil
}

// checkRunnable refuses a migration this executor cannot run atomically, naming the task that will.
func (e *executor) checkRunnable(groups []Group) error {
	if len(groups) == 0 {
		return fmt.Errorf("%w, so running it would report success for a schema change that never "+
			"happened", ErrNothingToRun)
	}

	if len(groups) > 1 {
		return fmt.Errorf("%w: it compiles to %d runs, because %d of its statements declare they "+
			"cannot run inside a transaction, and nothing here splits a migration around one -- "+
			"that is M13-T4's, which needs only to delete this refusal, since a failure partway "+
			"through such a split already records how far it got",
			ErrNotAtomic, len(groups), countStandalone(groups))
	}

	if lastTransactional(groups) < 0 {
		return fmt.Errorf("%w: every statement in it declares it cannot run inside a transaction, "+
			"so there is no transaction for its history row to be written inside -- running a "+
			"migration outside one is M13-T4's", ErrNotAtomic)
	}

	return nil
}

// countStandalone reports how many statements declared they cannot run inside a transaction.
func countStandalone(groups []Group) int {
	total := 0

	for _, group := range groups {
		if !group.Transactional {
			total += len(group.Statements)
		}
	}

	return total
}

// lastTransactional returns the run the history row is written inside, or -1 when no run is one.
func lastTransactional(groups []Group) int {
	last := -1

	for i, group := range groups {
		if group.Transactional {
			last = i
		}
	}

	return last
}

// runMigration runs a migration's runs in order, reporting how many statements a failure left behind.
func (e *executor) runMigration(ctx context.Context, groups []Group, record driver.Work) (int32, error) {
	records := lastTransactional(groups)

	var committed int32

	for i, group := range groups {
		if err := e.runGroup(ctx, group, record, i == records); err != nil {
			return e.durable(group, committed, err), err
		}

		committed = e.position
	}

	return 0, nil
}

// durable reports how many of a migration's statements survive a failure in the run that failed.
func (e *executor) durable(group Group, committed int32, err error) int32 {
	if group.Transactional && e.conn.Capabilities().TransactionalDDL() {
		return committed
	}

	return e.reached(err)
}

// reached reports the position the failing run got to, which is one before the statement that failed.
func (e *executor) reached(err error) int32 {
	var failure *StatementError
	if errors.As(err, &failure) {
		return failure.Position - 1
	}

	return e.position
}

// runGroup executes one run, bracketing it in a transaction when its statements can share one.
func (e *executor) runGroup(ctx context.Context, group Group, record driver.Work, records bool) error {
	if !group.Transactional {
		return e.exec(ctx, group.Statements)
	}

	return e.conn.Transact(ctx, func(ctx context.Context) error {
		if err := e.exec(ctx, group.Statements); err != nil {
			return err
		}

		if !records {
			return nil
		}

		return record(ctx)
	})
}

// exec runs statements in order, naming the one that failed and how many preceded it.
func (e *executor) exec(ctx context.Context, stmts []driver.Statement) error {
	for _, stmt := range stmts {
		e.position++

		if err := e.conn.Exec(ctx, stmt); err != nil {
			return &StatementError{Position: e.position, Statement: stmt, Err: err}
		}
	}

	return nil
}

// newRecord builds the row this run will be written as, leaving the store to stamp its outcome.
func (e *executor) newRecord(ready prepared) history.Record {
	return history.Record{
		Batch:     e.batch,
		Ordinal:   ready.item.ordinal,
		Version:   ready.item.migration.Version,
		Name:      ready.item.migration.Name,
		Direction: ready.compiled.Direction,
		Checksum:  ready.sum,
		AppliedAt: e.opts.Clock(),
		Driver:    e.conn.Capabilities().Driver(),
		Runtime:   e.opts.Runtime,
	}
}

// recorder returns the write that closes a migration's last transaction, timing the run as it goes.
func (e *executor) recorder(rec *history.Record) driver.Work {
	return func(ctx context.Context) error {
		rec.Duration = elapsed(rec.AppliedAt, e.opts.Clock())

		return e.store.Record(ctx, *rec)
	}
}

// recordFailure appends the run that failed, on a context that outlives whatever canceled it.
func (e *executor) recordFailure(ctx context.Context, rec history.Record, survived int32, cause error) error {
	rec.Duration = elapsed(rec.AppliedAt, e.opts.Clock())
	outliving := context.WithoutCancel(ctx)

	if survived < 1 {
		return errors.Join(cause, e.store.MarkFailed(outliving, rec, cause))
	}

	return errors.Join(cause, e.store.MarkDirty(outliving, rec, survived, cause))
}

// leftDirty describes the window a failure opened, for a run whose statements outlived it.
func (e *executor) leftDirty(ready prepared, survived int32, cause error) error {
	if survived < 1 {
		return cause
	}

	return &DirtyError{
		Migration:        ready.item.migration,
		Direction:        ready.compiled.Direction,
		LastStatement:    survived,
		Statements:       ready.compiled.Len(),
		Batch:            e.batch,
		Ordinal:          ready.item.ordinal,
		TransactionalDDL: e.conn.Capabilities().TransactionalDDL(),
		Err:              cause,
	}
}

// fail names the migration a failure happened in, and the operation it came from where there is one.
func (e *executor) fail(item queued, compiled plan.Plan, err error) error {
	return &MigrationError{
		Migration: item.migration,
		Batch:     e.batch,
		Ordinal:   item.ordinal,
		Operation: operationOf(compiled, err),
		Err:       err,
	}
}

// operationOf resolves the operation a statement failure came from, when the failure names one.
func operationOf(compiled plan.Plan, err error) ir.Operation {
	var failure *StatementError
	if !errors.As(err, &failure) {
		return nil
	}

	op, found := compiled.At(failure.Position)
	if !found {
		return nil
	}

	return op
}

// elapsed reports how long a run took, never negative, since a clock is not this package's to trust.
func elapsed(started, finished time.Time) time.Duration {
	if finished.Before(started) {
		return 0
	}

	return finished.Sub(started)
}
