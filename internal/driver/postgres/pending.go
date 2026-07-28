package postgres

import (
	"context"
	"errors"

	"github.com/emrebdr/migr/internal/history"
)

// historyTask is what replaces this file. Everything here is scaffolding that refuses honestly
// rather than a stub that returns a plausible empty value.
const historyTask = "M3-T6"

// ErrPending marks a driver method whose implementing task has not landed, for errors.Is.
var ErrPending = errors.New("not implemented in this driver yet")

// errPendingHistory refuses every history call until the store is written.
var errPendingHistory = pending("the history store", historyTask)

// pending builds the refusal a not-yet-written part of this driver gives.
func pending(what, task string) error {
	return errors.New(what + " is not written yet (" + task + "): " + ErrPending.Error())
}

// pendingHistory is a store that refuses, rather than a nil no caller could have checked.
type pendingHistory struct{}

// Ensure refuses to create the history table until the store is written.
func (pendingHistory) Ensure(context.Context) error {
	return errPendingHistory
}

// Record refuses to append a run until the store is written.
func (pendingHistory) Record(context.Context, history.Record) error {
	return errPendingHistory
}

// List refuses to read the history until the store is written.
func (pendingHistory) List(context.Context) ([]history.Record, error) {
	return nil, errPendingHistory
}

// Latest refuses to report the most recent run until the store is written.
func (pendingHistory) Latest(context.Context) (history.Record, bool, error) {
	return history.Record{}, false, errPendingHistory
}

// MarkFailed refuses to record a failure until the store is written.
func (pendingHistory) MarkFailed(context.Context, history.Record, error) error {
	return errPendingHistory
}

// MarkDirty refuses to record a dirty run until the store is written.
func (pendingHistory) MarkDirty(context.Context, history.Record, int32, error) error {
	return errPendingHistory
}

// compile-time proof that the scaffolding satisfies the contract it stands in for.
var _ history.Store = pendingHistory{}
