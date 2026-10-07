package engine

import (
	"context"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
)

// Previewed is what one direction would do against a database, having done none of it.
type Previewed struct {
	// Direction is the direction that was previewed.
	Direction ir.Direction
	// Batch is the batch these runs would be recorded under, had they run.
	Batch int64
	// Recorded reports whether this database has a migration history table at all.
	Recorded bool
	// Steps is one entry per migration that would run, in the order it would run them.
	Steps []Planned
	// Skipped is one entry per migration this direction would not run, whether by state or by scope.
	Skipped []Skipped
}

// Planned is one migration a preview would run, compiled for this engine but never executed.
type Planned struct {
	// Ordinal is the position within the batch it would be recorded under, counting from one.
	Ordinal int32
	// Plan is what it compiled to: the migration, the direction, and each operation's statements.
	Plan plan.Plan
	// Direct are the statements it runs itself, which this preview held rather than ran (ADR 0036).
	Direct []driver.Query
	// Unrestorable is what this migration's record and the live schema disagree about, where judged.
	Unrestorable []Disagreement
}

// Preview reports what a direction would run against this database, and executes none of it.
func Preview(ctx context.Context, conn driver.Conn, opts Options, dir ir.Direction) (Previewed, error) {
	e, err := newExecutor(conn, opts, ModePlan)
	if err != nil {
		return Previewed{}, err
	}

	if err := checkPreviewable(opts.Scope, dir); err != nil {
		return Previewed{}, err
	}

	return locked(ctx, e, func(ctx context.Context) (Previewed, error) {
		return e.previewAll(ctx, dir)
	})
}

// checkPreviewable refuses a direction nothing runs, and a scope the direction could not act with.
func checkPreviewable(scope Scope, dir ir.Direction) error {
	if !dir.Valid() {
		return unknownDirectionError(dir)
	}

	if dir == ir.Down {
		return scope.check()
	}

	return scope.checkUp()
}

// previewAll folds the history without creating it, then compiles the queue and stops there.
func (e *executor) previewAll(ctx context.Context, dir ir.Direction) (Previewed, error) {
	report, recorded, err := e.inspect(ctx)
	if err != nil {
		return Previewed{}, err
	}

	queue, skipped, err := classifyFor(dir, report, e.opts.Scope)
	if err != nil {
		return Previewed{}, err
	}

	previewed := Previewed{Direction: dir, Recorded: recorded, Skipped: skipped}
	if len(queue) == 0 {
		return previewed, nil
	}

	if previewed.Batch, err = e.plannedBatch(ctx, recorded); err != nil {
		return previewed, err
	}

	previewed.Steps, err = e.compileAll(ctx, queue, dir)

	return previewed, err
}

// The first and no other, because a preview applies nothing: the schema every later migration will
// run against is the one its predecessors leave, so judging one against the schema as it is now
// would mark a correct migration for naming a table its predecessor creates. The one about to run
// is the one an operator is deciding about, and it is the only one a preview can judge exactly.
//
// It goes through the same permission the run does, so what is marked here is what migr up will
// refuse rather than everything that could be refused -- a migration that authors its own down
// keeps the implied half, exactly as it does on the run path. A failure to read the schema marks
// nothing: a preview refuses nothing and must not fail over a question it only asked to annotate.

// judgedFirst reports what the first queued migration's record and the live schema disagree about.
func (e *executor) judgedFirst(ctx context.Context, at int, ready prepared) []Disagreement {
	if at > 0 {
		return nil
	}

	found, err := e.disagreements(ctx, operationsOf(ready.compiled))
	if err != nil {
		return nil
	}

	kept, err := e.permitted(ctx, ready.item.migration, ready.compiled.Direction, found)
	if err != nil {
		return nil
	}

	return kept
}

// An emulated operation is rendered from the catalog, so a preview can only print the statements of
// one whose catalog nothing in the preview would have changed: the first operation of the first
// queued migration, and only where that migration sends nothing through the escape hatch first. That
// is judgedFirst's own rule one level finer, and for the same reason -- a preview applies nothing, so
// the schema every later operation would run against is the one its predecessors leave.
//
// Everything else is left deferred and marked, because printing what it compiles to now would print
// SQL that is not the SQL that runs -- the reason migr plan counts a bound argument rather than
// splicing it in. Expanding by executing into a transaction the preview discards was rejected: it
// would take the rebuild's own exclusive lock and copy the table's rows for a report (invariants #12
// and #13), and on an engine whose DDL commits it would not be discardable at all.
//
// A failed expansion is dropped rather than reported, as judgedFirst drops a failed read: a preview
// refuses nothing, and must not fail over a question it only asked in order to print more.

// expandExact fills in the one operation a preview can compile exactly, where the first one is deferred.
func (e *executor) expandExact(ctx context.Context, at int, ready *prepared) {
	if at > 0 || len(ready.compiled.Steps) == 0 || len(ready.direct) > 0 {
		return
	}

	if !ready.compiled.Steps[0].Deferred {
		return
	}

	expanded, err := e.opts.Planner.Expand(ctx, ready.compiled.Migration, ready.compiled.Steps[0])
	if err != nil {
		return
	}

	ready.compiled.Steps[0] = expanded
}

// inspect reads the history and folds it, reporting a database with no history table as one.
func (e *executor) inspect(ctx context.Context) (history.Report, bool, error) {
	report, err := e.fold(ctx)
	if err != nil {
		return history.Report{}, false, err
	}

	if _, present := report.Resolved(); present {
		return report, true, nil
	}

	return history.Reconcile(e.opts.Migrations, history.NewLog(nil)), false, nil
}

// plannedBatch returns the batch these runs would record under, which is one on a database with none.
func (e *executor) plannedBatch(ctx context.Context, recorded bool) (int64, error) {
	if !recorded {
		return firstBatch, nil
	}

	return allocateBatch(ctx, e.store)
}

// classifyFor splits a reconciliation report the way the direction being previewed needs it split.
func classifyFor(dir ir.Direction, report history.Report, scope Scope) ([]queued, []Skipped, error) {
	switch dir {
	case ir.Up:
		return classify(report, scope)
	case ir.Down:
		return classifyDown(report, scope)
	}

	return nil, nil, unknownDirectionError(dir)
}

// compileAll asks every queued migration what it does and compiles it, abandoning the rest at the first refusal.
func (e *executor) compileAll(ctx context.Context, queue []queued, dir ir.Direction) ([]Planned, error) {
	steps := make([]Planned, 0, len(queue))

	for _, item := range queue {
		ready, err := e.compileOne(ctx, item, dir)
		if err != nil {
			return steps, err
		}

		e.expandExact(ctx, len(steps), &ready)

		steps = append(steps, Planned{
			Ordinal: item.ordinal, Plan: ready.compiled, Direct: ready.direct,
			Unrestorable: e.judgedFirst(ctx, len(steps), ready),
		})
	}

	return steps, nil
}

// compileOne prepares one migration the way the direction being previewed would have prepared it.
func (e *executor) compileOne(ctx context.Context, item queued, dir ir.Direction) (prepared, error) {
	if dir == ir.Down {
		return e.reverse(ctx, item)
	}

	return e.forward(ctx, item)
}
