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

	start, original := e.from, slices.Clone(ready.compiled.Steps)

	for {
		e.position = 0

		survived, err := e.runMigration(ctx, ready, e.recorder(rec))
		if err == nil {
			return 0, tried.retries, nil
		}

		again, waitErr := e.backoff(ctx, &tried, Retry{Migration: ready.item.migration,
			Direction: ready.compiled.Direction, Statement: statementOf(err), Err: err})
		if waitErr != nil {
			return survived, tried.retries, errors.Join(err, waitErr)
		}

		if !again {
			return survived, tried.retries, tried.exhausted(err)
		}

		e.rewind(ready, start, original, survived)
	}
}

// rewind sets up the next attempt: from the start where the rollback undid everything, else past what held.
func (e *executor) rewind(ready *prepared, start int32, original []plan.Step, survived int32) {
	if e.conn.Capabilities().TransactionalDDL() {
		e.from = start
		ready.compiled.Steps = slices.Clone(original)

		return
	}

	e.from = survived
}

// checkRunnable refuses a migration this executor cannot run atomically, naming the task that will.
func (e *executor) checkRunnable(ready prepared) error {
	groups := Groups(ready.compiled.Statements()[e.from:])

	if nothingToUndo(ready.compiled) {
		return nil
	}

	if len(groups) == 0 && ready.compiled.Deferred() == 0 {
		return fmt.Errorf("%w, so running it would report success for a schema change that never "+
			"happened", ErrNothingToRun)
	}

	return checkAtomic(groups)
}

// nothingToUndo reports a down that emitted no operations, which only an author can say (ADR 0079).
func nothingToUndo(compiled plan.Plan) bool {
	return compiled.Direction == ir.Down && len(compiled.Steps) == 0
}

// checkAtomic refuses statements that cannot be the one transaction a migration runs inside.
func checkAtomic(groups []Group) error {
	if len(groups) > 1 {
		return fmt.Errorf("%w: it compiles to %d runs, because %d of its statements declare they "+
			"cannot run inside a transaction, and nothing here splits a migration around one -- "+
			"that is M13-T4's, which writes the loop that brackets each run: a failure partway "+
			"through such a split already records how far it got, and an operation compiled "+
			"against the database can no longer read the statements before it from inside one",
			ErrNotAtomic, len(groups), countStandalone(groups))
	}

	if len(groups) == 1 && lastTransactional(groups) < 0 {
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

// The transaction brackets the whole step loop rather than one statement list, and that is what
// this task bought: an emulated operation is compiled against the database, so the only schema it
// can be rendered from correctly is the one the steps before it left -- which, inside a
// transaction, means the statements those steps have already sent and not yet committed. Both
// expanders read through the session's open transaction already, so this needed no driver change.
//
// It is also why M13-T4 writes a multi-run loop rather than deleting a precondition: a migration
// split around a standalone statement has more than one transaction, and an expansion after the
// split can no longer read what the statements before it did from inside one.

// runMigration runs a migration's steps in order, reporting how many statements a failure left behind.
func (e *executor) runMigration(ctx context.Context, ready *prepared, record driver.Work) (int32, error) {
	err := e.conn.Transact(ctx, func(ctx context.Context) error {
		if err := e.runSteps(ctx, ready); err != nil {
			return err
		}

		return record(ctx)
	})
	if err != nil {
		return e.durable(err), err
	}

	return 0, nil
}

// runSteps runs each operation's statements, compiling an emulated one against what the last left.
func (e *executor) runSteps(ctx context.Context, ready *prepared) error {
	for i := range ready.compiled.Steps {
		if err := e.runStep(ctx, ready, i); err != nil {
			return err
		}
	}

	return nil
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

	if err := checkAtomic(Groups(expanded.Statements)); err != nil {
		return plan.Step{}, &plan.CompileError{Migration: m, Index: step.Index,
			Kind: step.Op.Kind(), Err: err}
	}

	return expanded, nil
}

// durable reports how many of a migration's statements survive a failure in the transaction it ran in.
func (e *executor) durable(err error) int32 {
	if e.conn.Capabilities().TransactionalDDL() {
		return e.from
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
