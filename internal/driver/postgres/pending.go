package postgres

import (
	"context"
	"errors"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// The tasks that replace each part of this file. Everything here is scaffolding that refuses
// honestly rather than a stub that returns a plausible empty value.
const (
	compileTask = "M3-T2"
	lockTask    = "M3-T4"
	historyTask = "M3-T6"
)

// compilePending is the reason every operation is refused until the compiler is written.
const compilePending = "the PostgreSQL compiler is not written yet (" + compileTask + ")"

// ErrPending marks a driver method whose implementing task has not landed, for errors.Is.
var ErrPending = errors.New("not implemented in this driver yet")

// errPendingLock refuses to take a migration lock this driver cannot yet release.
var errPendingLock = pending("advisory locking", lockTask)

// errPendingHistory refuses every history call until the store is written.
var errPendingHistory = pending("the history store", historyTask)

// pending builds the refusal a not-yet-written part of this driver gives.
func pending(what, task string) error {
	return errors.New(what + " is not written yet (" + task + "): " + ErrPending.Error())
}

// pendingRules declares every operation unsupported, which is complete and currently true.
func pendingRules() map[ir.Kind]capability.Rule {
	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))

	refusal := capability.Fixed(capability.Unsupported(compilePending))
	for _, kind := range ir.Kinds() {
		rules[kind] = refusal
	}

	return rules
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
