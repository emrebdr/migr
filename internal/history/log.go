package history

import (
	"cmp"
	"context"
	"errors"
	"slices"
)

// Log is the runs a history table held, whose zero value is a database that has no such table.
type Log struct {
	// runs are every recorded run, sorted at construction into the order the runs happened.
	runs []Record
	// present distinguishes a table holding no runs from a database that has no table at all.
	present bool
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

	return NewLog(runs), nil
}

// NewLog returns the log a history table holding these runs would produce, ordered by run.
func NewLog(runs []Record) Log {
	ordered := slices.Clone(runs)
	slices.SortStableFunc(ordered, byRun)

	return Log{runs: ordered, present: true}
}

// byRun orders runs by the key ADR 0013 chose, since a clock ties and a rollback reverses this order.
func byRun(a, b Record) int {
	return cmp.Or(cmp.Compare(a.Batch, b.Batch), cmp.Compare(a.Ordinal, b.Ordinal))
}

// Present reports whether this database has a history table at all, which holding no runs is not.
func (l Log) Present() bool {
	return l.present
}

// Runs returns every recorded run in the order the runs happened, as a copy.
func (l Log) Runs() []Record {
	return slices.Clone(l.runs)
}
