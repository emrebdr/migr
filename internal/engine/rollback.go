package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
)

// Rollback undoes every applied migration, newest first, under the migration lock.
func Rollback(ctx context.Context, conn driver.Conn, opts Options) (Result, error) {
	e, err := newExecutor(conn, opts)
	if err != nil {
		return Result{}, err
	}

	return locked(ctx, e, e.rollbackAll)
}

// rollbackAll undoes what the database carries, inverting all of it before running any of it.
func (e *executor) rollbackAll(ctx context.Context) (Result, error) {
	report, err := e.reconcile(ctx)
	if err != nil {
		return Result{}, err
	}

	queue, skipped, err := classifyDown(report, e.opts.Scope)
	if err != nil {
		return Result{}, err
	}

	if len(queue) == 0 {
		return Result{Skipped: skipped}, nil
	}

	inverted, err := e.invert(ctx, queue)
	if err != nil {
		return Result{Skipped: skipped}, err
	}

	e.batch, err = allocateBatch(ctx, e.store)
	if err != nil {
		return Result{Skipped: skipped}, err
	}

	return e.rollbackQueue(ctx, inverted, skipped)
}

// invert asks how to undo every queued migration and compiles all of it, before any of it runs.
func (e *executor) invert(ctx context.Context, queue []queued) ([]prepared, error) {
	inverted := make([]prepared, 0, len(queue))

	for _, item := range queue {
		ready, err := e.reverse(ctx, item)
		if err != nil {
			return nil, err
		}

		inverted = append(inverted, ready)
	}

	return inverted, nil
}

// reverse asks how to undo one migration and compiles that, which is everything before it runs.
func (e *executor) reverse(ctx context.Context, item queued) (prepared, error) {
	ops, err := e.undo(ctx, item)
	if err != nil {
		return prepared{}, e.fail(item, plan.Plan{}, err)
	}

	ready, err := e.prepare(item, ir.Down, ops)
	if err != nil {
		return prepared{}, e.fail(item, plan.Plan{}, err)
	}

	return ready, nil
}

// undo asks the source what a migration undoes, falling back to deriving it from what it does.
func (e *executor) undo(ctx context.Context, item queued) (ir.Operations, error) {
	ops, err := e.opts.Source.Emit(ctx, item.migration, ir.Down)
	if err == nil {
		return ops, nil
	}

	if errors.Is(err, ErrDeclaredIrreversible) {
		return nil, err
	}

	if !errors.Is(err, ErrNoDownAuthored) {
		return nil, fmt.Errorf("asking what this migration undoes: %w", err)
	}

	return e.derive(ctx, item)
}

// derive inverts what a migration does, for one whose source authored no down to run instead.
func (e *executor) derive(ctx context.Context, item queued) (ir.Operations, error) {
	forward, err := e.opts.Source.Emit(ctx, item.migration, ir.Up)
	if err != nil {
		return nil, fmt.Errorf("asking what this migration does, to derive the undo it authored "+
			"none of: %w", err)
	}

	inverse, err := ir.ReverseAll(forward)
	if err != nil {
		return nil, irreversibleError(err)
	}

	return inverse, nil
}

// rollbackQueue runs the prepared inverses in order, abandoning the rest at the first that fails.
func (e *executor) rollbackQueue(ctx context.Context, queue []prepared, skipped []Skipped) (Result, error) {
	result := Result{Batch: e.batch, Skipped: skipped}

	for _, ready := range queue {
		reverted, err := e.perform(ctx, ready)
		if err != nil {
			return result, err
		}

		result.Runs = append(result.Runs, reverted)
	}

	return result, nil
}
