package engine

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// firstBatch is what an invocation records under when the history holds no run at all.
const firstBatch = 1

// firstOrdinal is the position within a batch of a run that is the only one in it.
const firstOrdinal = 1

// queued is one migration this run will act on, and the position it will be recorded under.
type queued struct {
	// migration is the migration to run.
	migration migration.Migration
	// source is the digest its file currently hashes to, which is the drift half of its checksum.
	source checksum.Digest
	// recorded is the run that put it in the state this queue acts on, and the order a rollback reverses.
	recorded history.Record
	// state is what reconciliation resolved it to, which is what a narrowed run reports it still in.
	state history.State
	// ordinal is its position within the batch, counting from one.
	ordinal int32
}

// resolved reads a report's statuses, refusing one over a database that turned out to have no history.
func resolved(report history.Report) ([]history.Status, error) {
	statuses, present := report.Resolved()
	if !present {
		return nil, fmt.Errorf("%w, though creating it reported success -- reading that as "+
			"'no runs yet' would make a fully migrated database look virgin (ADR 0019)",
			history.ErrNoTable)
	}

	return statuses, nil
}

// selected walks a report once, refusing a version it sees twice and asking a rule what each does.
func selected(statuses []history.Status, queues func(history.Status) (bool, error)) ([]queued, []Skipped, error) {
	var queue []queued

	var skipped []Skipped

	seen := make(map[string]struct{}, len(statuses))

	for _, st := range statuses {
		if _, repeated := seen[st.Version]; repeated {
			return nil, nil, duplicateError(st.Version)
		}

		seen[st.Version] = struct{}{}

		runs, err := queues(st)
		if err != nil {
			return nil, nil, err
		}

		if !runs {
			skipped = append(skipped, skip(st))
			continue
		}

		queue = append(queue, queued{
			migration: st.Migration,
			source:    st.Source,
			recorded:  st.Recorded,
			state:     st.State,
		})
	}

	return queue, skipped, nil
}

// classify splits a reconciliation report into what an apply run will apply and what it will not.
func classify(report history.Report, scope Scope) ([]queued, []Skipped, error) {
	statuses, err := resolved(report)
	if err != nil {
		return nil, nil, err
	}

	queue, skipped, err := selected(statuses, runnable)
	if err != nil {
		return nil, nil, err
	}

	queue, dropped, err := scope.narrowUp(queue, statuses)
	if err != nil {
		return nil, nil, err
	}

	return number(queue), append(skipped, dropped...), nil
}

// classifyDown splits it into what a rollback will undo, newest first, or refuses the whole run.
func classifyDown(report history.Report, scope Scope) ([]queued, []Skipped, error) {
	statuses, err := resolved(report)
	if err != nil {
		return nil, nil, err
	}

	queue, skipped, err := selected(statuses, reversible)
	if err != nil {
		return nil, nil, err
	}

	queue, dropped, err := scope.narrowDown(newestFirst(queue))
	if err != nil {
		return nil, nil, err
	}

	return number(queue), append(skipped, dropped...), nil
}

// runnable reports whether a reconciled migration should run, refusing the states nothing can.
func runnable(st history.Status) (bool, error) {
	switch st.State {
	case history.StatePending:
		return true, nil
	case history.StateApplied, history.StateMissing:
		return false, nil
	case history.StateDrifted:
		return false, driftedError(st, ir.Up, false)
	case history.StateDirty:
		return false, dirtyError(st)
	}

	return false, unknownStateError(st)
}

// reversible reports whether a reconciled migration should be undone, refusing the states nothing can.
func reversible(st history.Status) (bool, error) {
	switch st.State {
	case history.StateApplied:
		return true, nil
	case history.StatePending:
		return false, nil
	case history.StateMissing:
		return false, missingError(st)
	case history.StateDrifted:
		return false, driftedError(st, ir.Down, false)
	case history.StateDirty:
		return false, dirtyError(st)
	}

	return false, unknownStateError(st)
}

// skip records a migration that needed no run, and the state that decided that.
func skip(st history.Status) Skipped {
	return Skipped{Version: st.Version, Migration: st.Migration, State: st.State}
}

// newestFirst orders a rollback queue by the runs that applied it, most recent first.
func newestFirst(queue []queued) []queued {
	slices.SortStableFunc(queue, func(a, b queued) int {
		return cmp.Or(
			cmp.Compare(b.recorded.Batch, a.recorded.Batch),
			cmp.Compare(b.recorded.Ordinal, a.recorded.Ordinal),
		)
	})

	return queue
}

// number gives each queued migration its position within the batch, counting from one.
func number(queue []queued) []queued {
	var ordinal int32

	for i := range queue {
		ordinal++
		queue[i].ordinal = ordinal
	}

	return queue
}

// allocateBatch returns the batch this invocation records under, one past the last recorded run.
func allocateBatch(ctx context.Context, store history.Store) (int64, error) {
	last, recorded, err := store.Latest(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading the last recorded run: %w", err)
	}

	if !recorded {
		return firstBatch, nil
	}

	return max(last.Batch+1, firstBatch), nil
}
