package engine

import (
	"context"
	"fmt"
	"slices"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// Apply runs every pending migration in the order it was given, under the migration lock.
func Apply(ctx context.Context, conn driver.Conn, opts Options) (Result, error) {
	e, err := newExecutor(conn, opts, ModeApply)
	if err != nil {
		return Result{}, err
	}

	if err := opts.Scope.checkUp(); err != nil {
		return Result{}, err
	}

	return locked(ctx, e, func(ctx context.Context) (Result, error) {
		result, err := e.applyAll(ctx)
		result.Released = e.freed

		return result, err
	})
}

// applyAll reconciles against the history and applies whatever is pending, one migration at a time.
func (e *executor) applyAll(ctx context.Context) (Result, error) {
	report, err := e.reconcile(ctx)
	if err != nil {
		return Result{}, err
	}

	cps, err := e.checkpoints(ctx)
	if err != nil {
		return Result{}, err
	}

	shadows, err := e.shadowStates(ctx)
	if err != nil {
		return Result{}, err
	}

	if e.freed, err = e.releaseOrphans(ctx, report, shadows); err != nil {
		return Result{}, err
	}

	queue, skipped, err := classify(report, e.opts.Scope, leftover{cps: cps, shadows: shadows})
	if err != nil {
		return Result{}, err
	}

	if len(queue) == 0 {
		return Result{Skipped: skipped}, nil
	}

	if err := e.needsRelease(queue); err != nil {
		return Result{Skipped: skipped}, err
	}

	releases, err := e.releases(ctx, queue)
	if err != nil {
		return Result{Skipped: skipped}, err
	}

	e.batch, err = allocateBatch(ctx, e.store)
	if err != nil {
		return Result{Skipped: skipped}, err
	}

	statuses, _ := report.Resolved()

	return e.applyQueue(ctx, queue, skipped, gating{statuses: statuses, releases: releases})
}

// gating is what a run judges a waiting step by: the history before it ran, and the releases recorded.
type gating struct {
	// statuses is every migration's state before this run.
	statuses []history.Status
	// releases is which deploy each batch ran under.
	releases []history.Release
}

// ran returns the view a later step is judged by once this run has applied one migration in its batch.
func (g gating) ran(m migration.Migration, batch int64, release string) gating {
	statuses := make([]history.Status, 0, len(g.statuses))
	for _, s := range g.statuses {
		if s.Version == m.Version {
			s.State, s.Recorded = history.StateApplied, history.Record{Batch: batch, Version: m.Version}
		}

		statuses = append(statuses, s)
	}

	releases := g.releases
	if _, named := history.ReleaseOf(releases, batch); !named && release != "" {
		releases = append(slices.Clone(releases), history.Release{Batch: batch, Name: release})
	}

	return gating{statuses: statuses, releases: releases}
}

// applyQueue runs the queue in order, abandoning the rest at the first migration that fails.
func (e *executor) applyQueue(ctx context.Context, queue []queued, skipped []Skipped,
	g gating) (Result, error) {
	result := Result{Batch: e.batch, Skipped: skipped}

	for _, item := range queue {
		if held, waits := e.held(item, g.statuses, g.releases); waits {
			result.Held = held

			return result, nil
		}

		applied, err := e.applyOne(ctx, item)
		if err != nil {
			return result, err
		}

		result.Runs = append(result.Runs, applied)
		g = g.ran(item.migration, e.batch, e.opts.Release)
	}

	return result, nil
}

// applyOne asks what one migration does, prepares it, and runs it, recording it before it commits.
func (e *executor) applyOne(ctx context.Context, item queued) (Run, error) {
	if item.resume != nil {
		return e.resumeChunks(ctx, item)
	}

	if item.shadow != nil {
		return e.resumeShadow(ctx, item)
	}

	ready, err := e.forward(ctx, item)
	if err != nil {
		return Run{}, err
	}

	return e.perform(ctx, ready)
}

// forward asks what one migration does and compiles it, which is everything an apply does before it runs.
func (e *executor) forward(ctx context.Context, item queued) (prepared, error) {
	emitted, err := e.emit(ctx, item.migration, ir.Up)
	if err != nil {
		return prepared{}, e.fail(item, plan.Plan{},
			fmt.Errorf("asking what this migration does: %w", err))
	}

	if err := e.capable(item.migration, ir.Up, emitted.Operations); err != nil {
		return prepared{}, err
	}

	if err := e.permit(item.migration, emitted.Operations); err != nil {
		return prepared{}, err
	}

	if err := e.restorable(ctx, item.migration, ir.Up, emitted.Operations); err != nil {
		return prepared{}, err
	}

	ready, err := e.prepare(ctx, item, ir.Up, emitted)
	if err != nil {
		return prepared{}, e.fail(item, plan.Plan{}, err)
	}

	return ready, nil
}
