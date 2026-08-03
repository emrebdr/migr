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
	// batch is what every run in this invocation is recorded under.
	batch int64
	// position counts statements within one migration, continuing across its runs.
	position int32
}

// newExecutor checks what a run was given and binds it to the session's history, before any lock.
func newExecutor(conn driver.Conn, opts Options) (*executor, error) {
	if err := opts.check(); err != nil {
		return nil, err
	}

	store := conn.History()
	if store == nil {
		return nil, ErrNoHistory
	}

	return &executor{conn: conn, store: store, opts: opts}, nil
}

// invocation is what one run does under the migration lock, which is an apply or a rollback.
type invocation func(ctx context.Context) (Result, error)

// locked takes the migration lock, runs the invocation, and gives the lock back after it returns.
func (e *executor) locked(ctx context.Context, work invocation) (result Result, err error) {
	unlock, err := e.conn.Lock(ctx)
	if err != nil {
		return Result{}, err
	}

	defer func() { err = errors.Join(err, unlock(context.WithoutCancel(ctx))) }()

	return work(ctx)
}

// reconcile ensures the history table, reads it, and folds it against the migrations it was given.
func (e *executor) reconcile(ctx context.Context) (history.Report, error) {
	if err := e.store.Ensure(ctx); err != nil {
		return history.Report{}, fmt.Errorf("preparing the migration history: %w", err)
	}

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

	if err := e.runMigration(ctx, ready.groups, e.recorder(&rec)); err != nil {
		return Run{}, e.fail(ready.item, ready.compiled, e.recordFailure(ctx, rec, err))
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

	caps := e.conn.Capabilities()

	if !caps.TransactionalDDL() {
		return fmt.Errorf("%w: %s reports no transactional DDL, so a failure partway would leave "+
			"the schema half changed with nothing recording how far it got -- the dirty-state model "+
			"that makes such a run recoverable is M4-T9's", ErrNotAtomic, caps.Driver())
	}

	if len(groups) > 1 {
		return fmt.Errorf("%w: it compiles to %d runs, because %d of its statements declare they "+
			"cannot run inside a transaction, and nothing here can record partial progress across "+
			"two commits -- that dirty-state model is M4-T9's, and splitting a migration around "+
			"such a statement is M13-T4's",
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

// runMigration runs a migration's runs in order, recording it inside the last transactional one.
func (e *executor) runMigration(ctx context.Context, groups []Group, record driver.Work) error {
	records := lastTransactional(groups)

	for i, group := range groups {
		if err := e.runGroup(ctx, group, record, i == records); err != nil {
			return err
		}
	}

	return nil
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
func (e *executor) recordFailure(ctx context.Context, rec history.Record, cause error) error {
	rec.Duration = elapsed(rec.AppliedAt, e.opts.Clock())

	return errors.Join(cause, e.store.MarkFailed(context.WithoutCancel(ctx), rec, cause))
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
