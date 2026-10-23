package history

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
)

// Log is the runs a history table held, whose zero value is a database that has no such table.
type Log struct {
	// runs are every recorded run, sorted at construction into the order the runs happened.
	runs []Record
	// present distinguishes a table holding no runs from a database that has no table at all.
	present bool
	// syncing are the versions whose shadow table is held in sync, waiting for a cutover confirmation.
	syncing []string
}

// Read collects a store's runs, reporting a table nothing created as an absent log rather than a failure.
func Read(ctx context.Context, store Reader) (Log, error) {
	runs, err := store.List(ctx)
	if errors.Is(err, ErrNoTable) {
		return Log{}, nil
	}

	if err != nil {
		return Log{}, err
	}

	shadows, err := ReadShadows(ctx, store)
	if err != nil {
		return Log{}, err
	}

	return NewLog(runs).WithShadows(shadows), nil
}

// ReadShadows reads where every shadow-table migration stands, and nothing where the store remembers none.
func ReadShadows(ctx context.Context, store Reader) ([]ShadowState, error) {
	remembering, remembers := store.(Shadows)
	if !remembers {
		return nil, nil
	}

	states, err := remembering.ShadowStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading where a shadow-table migration stands: %w", err)
	}

	return states, nil
}

// NewLog returns the log a history table holding these runs would produce, ordered by run.
func NewLog(runs []Record) Log {
	ordered := slices.Clone(runs)
	slices.SortStableFunc(ordered, byRun)

	return Log{runs: ordered, present: true}
}

// byRun orders runs by (batch, ordinal), since a clock ties and a rollback reverses this order.
func byRun(a, b Record) int {
	return cmp.Or(cmp.Compare(a.Batch, b.Batch), cmp.Compare(a.Ordinal, b.Ordinal))
}

// WithShadows returns this log knowing which versions are held in sync, waiting for a cutover confirmation.
func (l Log) WithShadows(states []ShadowState) Log {
	l.syncing = nil

	for _, s := range states {
		if s.Stage == StageSyncing {
			l.syncing = append(l.syncing, s.Version)
		}
	}

	return l
}

// waiting reports whether a version's shadow table is held in sync, waiting for a cutover confirmation.
func (l Log) waiting(version string) bool {
	return slices.Contains(l.syncing, version)
}

// Present reports whether this database has a history table at all, which holding no runs is not.
func (l Log) Present() bool {
	return l.present
}

// Runs returns every recorded run in the order the runs happened, as a copy.
func (l Log) Runs() []Record {
	return slices.Clone(l.runs)
}
