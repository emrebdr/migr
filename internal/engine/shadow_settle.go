package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
)

// A stopped shadow-table migration is settled by the repairs every dirty migration is, each meaning
// what it means for a copy kept current from a log (ADR 0089, F). Rolling it back is the abort: the
// capture is released and the copy dropped before the row is appended, both by the names the state
// recorded, so nothing is left holding WAL or disk. Asserting it applied is refused, since only a
// cutover applies one, and a resume is migr up's -- it is the same run, carried on.

// errShadowApplied refuses asserting a shadow-table migration applied, which only its cutover can make true.
var errShadowApplied = errors.New("a shadow-table migration is applied by its cutover and nothing else: " +
	"migr up carries it on to the cutover, and migr resolve --rolled-back abandons it")

// errShadowResume refuses resuming a shadow-table migration here, which is migr up's to carry on.
var errShadowResume = errors.New("a stopped shadow-table migration is carried on by migr up, from the " +
	"stage and log position it recorded; migr resolve --rolled-back abandons it instead")

// stoppedShadow returns where a dirty migration stood in a shadow-table migration, where it was in one.
func (e *executor) stoppedShadow(ctx context.Context, st history.Status) (*history.ShadowState, error) {
	states, err := e.shadowStates(ctx)
	if err != nil {
		return nil, err
	}

	state, found := history.FindShadow(states, st.Version)
	if !found || state.Stage == history.StageCutOver {
		return nil, nil
	}

	return &state, nil
}

// checkShadowRepair refuses the repairs a stopped shadow-table migration cannot take.
func checkShadowRepair(mode RepairMode, state *history.ShadowState) error {
	switch {
	case state == nil:
		return nil
	case mode == RepairApplied:
		return fmt.Errorf("%w: %w", ErrNotResumable, errShadowApplied)
	case mode == RepairResume:
		return fmt.Errorf("%w: %w", ErrNotResumable, errShadowResume)
	}

	return nil
}

// abandonShadow releases a stopped migration's capture and drops its copy, by the names its state recorded.
func (e *executor) abandonShadow(ctx context.Context, state history.ShadowState) error {
	if e.opts.Shadower == nil || e.opts.Capture == nil {
		return fmt.Errorf("%w: this session cannot drop %s or release the change stream %s, so "+
			"abandoning it would leave both behind -- drop the table and release the stream by hand, "+
			"then run this again", ErrCannotShadow, state.Shadow, state.Capture)
	}

	stream, err := e.opts.Capture(ctx, capture.Request{Name: state.Capture, Tables: []string{state.Table},
		From: capture.Position(state.Position)})
	if err != nil {
		return fmt.Errorf("opening the change stream %s to release it: %w", state.Capture, err)
	}

	if err := stream.Drop(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("releasing the change stream %s: %w", state.Capture, err)
	}

	stmts, err := e.opts.Shadower.DropShadow(shadowOf(state))
	if err != nil {
		return err
	}

	if err := e.execAll(ctx, stmts); err != nil {
		return fmt.Errorf("dropping the copy %s: %w", state.Shadow, err)
	}

	return nil
}

// shadowOf is the table, copy and retired name a recorded state names.
func shadowOf(state history.ShadowState) driver.Shadow {
	return driver.Shadow{Table: state.Table, Shadow: state.Shadow, Retired: state.Retired}
}

// clearShadow removes a migration's shadow state inside the open transaction, where this history keeps any.
func (e *executor) clearShadow(ctx context.Context, version string) error {
	store, remembers := e.store.(history.Shadows)
	if !remembers {
		return nil
	}

	return store.ClearShadow(ctx, version)
}

// Retired is what a retirement dropped: the original a shadow-table migration's cutover kept.
type Retired struct {
	// Version is the migration whose original was dropped.
	Version string
	// Table is the table it changed.
	Table string
	// Dropped is the retired original's name.
	Dropped string
}

// Retire drops the original a shadow-table migration's cutover kept, under the migration lock.
func Retire(ctx context.Context, conn driver.Conn, opts Options, version string) (Retired, error) {
	e, err := bind(conn, opts, ModeApply)
	if err != nil {
		return Retired{}, err
	}

	return locked(ctx, e, func(ctx context.Context) (Retired, error) { return e.retire(ctx, version) })
}

// retire drops one retired original and its state, refusing a migration that has not cut over.
func (e *executor) retire(ctx context.Context, version string) (Retired, error) {
	states, err := e.shadowStates(ctx)
	if err != nil {
		return Retired{}, err
	}

	state, found := history.FindShadow(states, version)
	if !found || state.Stage != history.StageCutOver {
		return Retired{}, fmt.Errorf("%w: %s has no retired original to drop", ErrNotCutOver, version)
	}

	if e.opts.Shadower == nil {
		return Retired{}, fmt.Errorf("%w: this session cannot drop %s", ErrCannotShadow, state.Retired)
	}

	stmts, err := e.opts.Shadower.DropRetired(shadowOf(state))
	if err != nil {
		return Retired{}, err
	}

	if err := e.execAll(ctx, stmts); err != nil {
		return Retired{}, fmt.Errorf("dropping the retired original %s: %w", state.Retired, err)
	}

	if err := e.conn.Transact(ctx, func(ctx context.Context) error {
		return e.clearShadow(ctx, version)
	}); err != nil {
		return Retired{}, fmt.Errorf("clearing the state of %s: %w", version, err)
	}

	return Retired{Version: version, Table: state.Table, Dropped: state.Retired}, nil
}
