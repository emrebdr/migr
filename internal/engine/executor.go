package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	// from is how many of a migration's statements a resume skips, and zero for every other run.
	from int32
	// committed is how many of a migration's statements are durable, which every part it finishes moves on.
	committed int32
	// shift is how many ordinals the rows of chunked runs in progress took beyond their own.
	shift int32
}

// ordinalOf returns the ordinal a queued migration's row is written at, after any chunked run's own.
func (e *executor) ordinalOf(item queued) int32 {
	return item.ordinal + e.shift
}

// newExecutor checks what a run was given and binds it to the session's history, before any lock.
func newExecutor(conn driver.Conn, opts Options, mode Mode) (*executor, error) {
	if err := opts.check(); err != nil {
		return nil, err
	}

	return bind(conn, opts, mode)
}

// bind checks the mode and gives an invocation the history store the session opened at Open.
func bind(conn driver.Conn, opts Options, mode Mode) (*executor, error) {
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
func (e *executor) emit(ctx context.Context, m migration.Migration, dir ir.Direction) (Emitted, error) {
	return e.opts.Source.Emit(ctx, m, dir, e.mode)
}

// ask asks what a migration does as a question, which no invocation turns into a run.
func (e *executor) ask(ctx context.Context, m migration.Migration, dir ir.Direction) (Emitted, error) {
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

// prepared is one migration's operations, compiled for this driver and hashed for its row.
type prepared struct {
	// item is the migration this will run and the ordinal its row records.
	item queued
	// compiled is what the operations became for this engine, in the direction they were compiled in.
	compiled plan.Plan
	// sum is the checksum the run is recorded under, over the source and the emitted IR.
	sum checksum.Checksum
	// direct are the statements the migration runs itself, which only a question ever holds.
	direct []driver.Query
}

// prepare hashes what a migration emitted, compiles it, and refuses what cannot run atomically.
func (e *executor) prepare(ctx context.Context, item queued, dir ir.Direction,
	emitted Emitted,
) (prepared, error) {
	ready, err := e.compileFor(ctx, item, dir, emitted)
	if err != nil {
		return prepared{}, err
	}

	return ready, e.checkRunnable(ready)
}

// compileFor hashes what a migration emitted and compiles it, leaving the refusals to the caller.
func (e *executor) compileFor(_ context.Context, item queued, dir ir.Direction,
	emitted Emitted,
) (prepared, error) {
	sum, err := checksum.OfOperations(emitted.Operations)
	if err != nil {
		return prepared{}, fmt.Errorf("hashing the emitted operations: %w", err)
	}

	compiled, err := e.opts.Planner.Compile(item.migration, dir, emitted.Operations)
	if err != nil {
		return prepared{}, err
	}

	return prepared{
		item:     item,
		compiled: compiled,
		sum:      checksum.Checksum{Source: item.source, IR: sum},
		direct:   emitted.Direct,
	}, nil
}

// perform runs one prepared migration, recording it inside the transaction it runs in.
func (e *executor) perform(ctx context.Context, ready prepared) (Run, error) {
	if ready.compiled.Chunked() > 0 {
		return e.performChunked(ctx, ready, nil)
	}

	rec := e.newRecord(ready)
	start := e.from

	bound, cancel := e.migrationBound(ctx)
	defer cancel()

	survived, retried, err := e.runRetrying(bound, &ready, &rec)
	err = e.timedOut(bound, err)

	if err != nil {
		return Run{}, e.fail(ready.item, ready.compiled, errors.Join(
			e.leftDirty(ready, survived, err), e.recordFailure(ctx, rec, survived, err)))
	}

	return Run{
		Migration:  ready.item.migration,
		Ordinal:    rec.Ordinal,
		Statements: ready.compiled.Len() - start,
		Started:    rec.AppliedAt,
		Duration:   rec.Duration,
		Retries:    retried,
	}, nil
}

// runRetrying runs a migration, trying it again after a lock wait spent for as long as the backoff allows.
func (e *executor) runRetrying(ctx context.Context, ready *prepared, rec *history.Record,
) (int32, int, error) {
	var tried attempts

	original := slices.Clone(ready.compiled.Steps)

	for {
		e.position = 0

		survived, err := e.runMigration(ctx, ready, rec)
		if err == nil {
			return 0, tried.retries, nil
		}

		if ranBare(err) {
			return survived, tried.retries, notRetriedBare(err)
		}

		again, waitErr := e.backoff(ctx, &tried, Retry{Migration: ready.item.migration,
			Direction: ready.compiled.Direction, Statement: statementOf(err), Err: err})
		if waitErr != nil {
			return survived, tried.retries, errors.Join(err, waitErr)
		}

		if !again {
			return survived, tried.retries, tried.exhausted(err)
		}

		e.rewind(ready, original, survived)
	}
}

// rewind sets up the next attempt past what survived, compiling again every step the rollback undid.
func (e *executor) rewind(ready *prepared, original []plan.Step, survived int32) {
	e.from = survived

	if e.conn.Capabilities().TransactionalDDL() {
		ready.compiled.Steps = restoredAfter(ready.compiled.Steps, original, survived)
	}
}

// restoredAfter keeps every step whose statements all survived and puts the rest back as they were compiled.
func restoredAfter(current, original []plan.Step, survived int32) []plan.Step {
	restored := slices.Clone(original)

	var end int32

	for i, step := range current {
		for range step.Statements {
			end++
		}

		if len(step.Statements) > 0 && end <= survived {
			restored[i] = step
		}
	}

	return restored
}

// checkRunnable refuses a migration that would report success for a change that never happened.
func (e *executor) checkRunnable(ready prepared) error {
	if nothingToUndo(ready.compiled) {
		return nil
	}

	if int(e.from) >= len(ready.compiled.Statements()) && ready.compiled.Deferred() == 0 {
		return fmt.Errorf("%w, so running it would report success for a schema change that never "+
			"happened", ErrNothingToRun)
	}

	return nil
}

// nothingToUndo reports a down that emitted no operations, which only an author can say (ADR 0079).
func nothingToUndo(compiled plan.Plan) bool {
	return compiled.Direction == ir.Down && len(compiled.Steps) == 0
}

// runStep compiles one operation where it was deferred, and runs the statements it is.
func (e *executor) runStep(ctx context.Context, ready *prepared, at int) error {
	step := ready.compiled.Steps[at]

	if step.Deferred {
		expanded, err := e.expand(ctx, ready.compiled.Migration, step)
		if err != nil {
			return err
		}

		ready.compiled.Steps[at] = expanded
		step = expanded
	}

	return e.exec(ctx, step.Statements)
}

// expand compiles one deferred operation, holding it to statements this migration's transaction takes.
func (e *executor) expand(ctx context.Context, m migration.Migration, step plan.Step) (plan.Step, error) {
	expanded, err := e.opts.Planner.Expand(ctx, m, step)
	if err != nil {
		return plan.Step{}, err
	}

	if err := checkExpansion(expanded.Statements); err != nil {
		return plan.Step{}, &plan.CompileError{Migration: m, Index: step.Index,
			Kind: step.Op.Kind(), Err: err}
	}

	return expanded, nil
}

// durable reports how many of a migration's statements survive a failure in the part it stopped in.
func (e *executor) durable(err error) int32 {
	if e.conn.Capabilities().TransactionalDDL() {
		return e.committed
	}

	return max(e.committed, e.reached(err))
}

// reached reports the position the failing run got to, which is one before the statement that failed.
func (e *executor) reached(err error) int32 {
	var failure *StatementError
	if errors.As(err, &failure) {
		return failure.Position - 1
	}

	return e.position
}

// exec runs statements in order, skipping what has already run and naming the one that failed.
func (e *executor) exec(ctx context.Context, stmts []driver.Statement) error {
	for _, stmt := range stmts {
		e.position++

		if e.resumed() {
			continue
		}

		if err := e.execBounded(ctx, stmt); err != nil {
			return &StatementError{Position: e.position, Statement: stmt, Err: err}
		}
	}

	return nil
}

// execBounded runs one statement under safety.statement_timeout, naming the bound where it was reached.
func (e *executor) execBounded(ctx context.Context, stmt driver.Statement) error {
	bound, cancel := e.statementBound(ctx)
	defer cancel()

	return e.timedOut(bound, e.conn.Exec(bound, stmt))
}

// resumed reports a statement a stopped run already succeeded at, which this run counts but skips.
func (e *executor) resumed() bool {
	return e.position <= e.from
}

// newRecord builds the row this run will be written as, leaving the store to stamp its outcome.
func (e *executor) newRecord(ready prepared) history.Record {
	return history.Record{
		Batch:     e.batch,
		Ordinal:   e.ordinalOf(ready.item),
		Version:   ready.item.migration.Version,
		Name:      ready.item.migration.Name,
		Direction: ready.compiled.Direction,
		Checksum:  ready.sum,
		AppliedAt: e.opts.Clock(),
		Driver:    e.conn.Capabilities().Driver(),
		Runtime:   e.opts.Runtime(ready.item.migration),
	}
}

// recorder returns the write that closes a migration's last transaction, timing the run as it goes.
func (e *executor) recorder(rec *history.Record) driver.Work {
	return func(ctx context.Context) error {
		rec.Duration = elapsed(rec.AppliedAt, e.opts.Clock())

		return e.store.Record(ctx, *rec)
	}
}

// recordFailure appends the run that failed with what stopped it, on a context outliving the cancel.
func (e *executor) recordFailure(ctx context.Context, rec history.Record, survived int32, cause error) error {
	rec.Duration = elapsed(rec.AppliedAt, e.opts.Clock())
	outliving := context.WithoutCancel(ctx)

	if survived < 1 {
		return e.store.MarkFailed(outliving, rec, cause)
	}

	return e.store.MarkDirty(outliving, rec, survived, cause)
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
		Deferred:         ready.compiled.Deferred(),
		Batch:            e.batch,
		Ordinal:          e.ordinalOf(ready.item),
		TransactionalDDL: e.conn.Capabilities().TransactionalDDL(),
		Split:            splits(ready.compiled),
		Err:              cause,
	}
}

// fail names the migration a failure happened in, and the operation it came from where there is one.
func (e *executor) fail(item queued, compiled plan.Plan, err error) error {
	return &MigrationError{
		Migration: item.migration,
		Batch:     e.batch,
		Ordinal:   e.ordinalOf(item),
		Operation: operationOf(compiled, err),
		Err:       err,
	}
}

// operationOf resolves the operation a statement failure came from, when the failure names one.
func operationOf(compiled plan.Plan, err error) ir.Operation {
	var chunk *ChunkError
	if errors.As(err, &chunk) {
		return chunk.Op
	}

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
