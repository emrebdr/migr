package engine

import (
	"context"
	"fmt"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
)

// Apply runs every pending migration in the order it was given, under the migration lock.
func Apply(ctx context.Context, conn driver.Conn, opts Options) (Result, error) {
	e, err := newExecutor(conn, opts)
	if err != nil {
		return Result{}, err
	}

	if err := opts.Scope.checkUp(); err != nil {
		return Result{}, err
	}

	return e.locked(ctx, e.applyAll)
}

// applyAll reconciles against the history and applies whatever is pending, one migration at a time.
func (e *executor) applyAll(ctx context.Context) (Result, error) {
	report, err := e.reconcile(ctx)
	if err != nil {
		return Result{}, err
	}

	queue, skipped, err := classify(report, e.opts.Scope)
	if err != nil {
		return Result{}, err
	}

	if len(queue) == 0 {
		return Result{Skipped: skipped}, nil
	}

	e.batch, err = allocateBatch(ctx, e.store)
	if err != nil {
		return Result{Skipped: skipped}, err
	}

	return e.applyQueue(ctx, queue, skipped)
}

// applyQueue runs the queue in order, abandoning the rest at the first migration that fails.
func (e *executor) applyQueue(ctx context.Context, queue []queued, skipped []Skipped) (Result, error) {
	result := Result{Batch: e.batch, Skipped: skipped}

	for _, item := range queue {
		applied, err := e.applyOne(ctx, item)
		if err != nil {
			return result, err
		}

		result.Runs = append(result.Runs, applied)
	}

	return result, nil
}

// applyOne asks what one migration does, prepares it, and runs it, recording it before it commits.
func (e *executor) applyOne(ctx context.Context, item queued) (Run, error) {
	ops, err := e.opts.Source.Emit(ctx, item.migration, ir.Up)
	if err != nil {
		return Run{}, e.fail(item, plan.Plan{},
			fmt.Errorf("asking what this migration does: %w", err))
	}

	ready, err := e.prepare(item, ir.Up, ops)
	if err != nil {
		return Run{}, e.fail(item, plan.Plan{}, err)
	}

	return e.perform(ctx, ready)
}
