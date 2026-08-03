package engine

import (
	"context"
	"fmt"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/migration"
)

// firstBatch is what an invocation records under when the history holds no run at all.
const firstBatch = 1

// queued is one migration this run will apply, and the position it will be recorded under.
type queued struct {
	// migration is the migration to run.
	migration migration.Migration
	// source is the digest its file currently hashes to, which is the drift half of its checksum.
	source checksum.Digest
	// ordinal is its position within the batch, counting from one.
	ordinal int32
}

// classify splits a reconciliation report into what will run and what will not, or refuses the run.
func classify(report history.Report) ([]queued, []Skipped, error) {
	statuses, present := report.Resolved()
	if !present {
		return nil, nil, fmt.Errorf("%w, though creating it reported success -- reading that as "+
			"'no runs yet' would make a fully migrated database look virgin (ADR 0019)",
			history.ErrNoTable)
	}

	var queue []queued

	var skipped []Skipped

	seen := make(map[string]struct{}, len(statuses))

	for _, st := range statuses {
		if _, repeated := seen[st.Version]; repeated {
			return nil, nil, duplicateError(st.Version)
		}

		seen[st.Version] = struct{}{}

		runs, err := runnable(st)
		if err != nil {
			return nil, nil, err
		}

		if !runs {
			skipped = append(skipped, skip(st))
			continue
		}

		queue = append(queue, queued{migration: st.Migration, source: st.Source})
	}

	return number(queue), skipped, nil
}

// runnable reports whether a reconciled migration should run, refusing the states nothing can.
func runnable(st history.Status) (bool, error) {
	switch st.State {
	case history.StatePending:
		return true, nil
	case history.StateApplied, history.StateMissing:
		return false, nil
	case history.StateDrifted:
		return false, driftedError(st)
	case history.StateDirty:
		return false, dirtyError(st)
	}

	return false, unknownStateError(st)
}

// skip records a migration that needed no run, and the state that decided that.
func skip(st history.Status) Skipped {
	return Skipped{Version: st.Version, Migration: st.Migration, State: st.State}
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
