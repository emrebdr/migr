package sqlite

import (
	"context"
	"errors"

	"github.com/emrebdr/migr/internal/history"
)

// The half of the contract a later M8 task fills, refusing by name rather than by nil.
//
// A nil history.Store would reach a caller that cannot check for one -- the engine reads
// Conn.History() and calls it -- so it is a value that refuses, and it names the task that makes it
// real. That is M3-T1's shape: the driver ships complete and honestly unimplemented, so the
// conformance suite passes from this commit and every later task arrives as a diff. M8-T5 took the
// migration lock out of here, which leaves this file the history store's alone.

// errHistoryPending marks the migration history store this driver has not implemented yet.
var errHistoryPending = errors.New("the SQLite migration history store is M8-T6: the history table " +
	"is itself created through driver-compiled operations (invariant #3), so it cannot precede the " +
	"compiler in M8-T2")

// pendingHistory is a store that refuses every call, naming the task that implements it.
type pendingHistory struct{}

// Ensure refuses: the history store is M8-T6.
func (pendingHistory) Ensure(context.Context) error { return errHistoryPending }

// Record refuses: the history store is M8-T6.
func (pendingHistory) Record(context.Context, history.Record) error { return errHistoryPending }

// List refuses: the history store is M8-T6.
func (pendingHistory) List(context.Context) ([]history.Record, error) {
	return nil, errHistoryPending
}

// Latest refuses: the history store is M8-T6.
func (pendingHistory) Latest(context.Context) (history.Record, bool, error) {
	return history.Record{}, false, errHistoryPending
}

// MarkFailed refuses: the history store is M8-T6.
func (pendingHistory) MarkFailed(context.Context, history.Record, error) error {
	return errHistoryPending
}

// MarkDirty refuses: the history store is M8-T6.
func (pendingHistory) MarkDirty(context.Context, history.Record, int32, error) error {
	return errHistoryPending
}

// compile-time proof that the pending store is the shape M8-T6 replaces rather than adds to.
var _ history.Store = pendingHistory{}
