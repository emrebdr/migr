package engine

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// Mode is whether the engine is running a migration or asking it a question.
type Mode string

// The two modes a migration is asked in, spelled as docs/protocol.md §6 spells them.
const (
	ModeApply Mode = "apply"
	ModePlan  Mode = "plan"
)

// modes lists every mode the engine asks in.
var modes = []Mode{ModeApply, ModePlan}

// Modes returns every mode the engine asks a migration in, as a copy.
func Modes() []Mode {
	return slices.Clone(modes)
}

// Valid reports whether a mode is one of the two the engine asks in.
func (m Mode) Valid() bool {
	return slices.Contains(modes, m)
}

// Emitted is what one migration did when it was asked, whether that asking was a run or a question.
type Emitted struct {
	// Operations are the schema changes it emitted, in the order it emitted them.
	Operations ir.Operations
	// Direct are the statements it runs itself, which a question holds rather than runs.
	Direct []driver.Query
}

// Source yields the operations one migration emits, and is asked only about migrations that run.
type Source interface {
	// Emit returns what one migration performs, or ErrNoDownAuthored for a down it has none of.
	Emit(ctx context.Context, m migration.Migration, dir ir.Direction, mode Mode) (Emitted, error)
}

// Clock reports the current instant, since forbidigo leaves this package no clock of its own.
type Clock func() time.Time

// Options are what an apply run needs beyond the session it runs against.
type Options struct {
	// Planner compiles each migration's operations to statements, and needs no database.
	Planner plan.Planner
	// Source yields those operations, and is asked only about migrations that will run.
	Source Source
	// Migrations are the discovered migrations with their source digests, in the order to run them.
	Migrations []history.File
	// Clock supplies the instant each run started, which this package may not read for itself.
	Clock Clock
	// Runtime names the language one migration is written in, since a run may hold several.
	Runtime func(m migration.Migration) string
	// Scope narrows what this run acts on, and an unset one acts on everything the direction can.
	Scope Scope
	// Allow is the destruction this run may perform, and an unset one permits none of it.
	Allow Allowance
	// Chunker is the session a data-only migration runs through a chunk at a time, nil where none is.
	Chunker driver.Chunker
	// Pacer sizes and times every chunk, and an unset one runs them back to back at the planner's size.
	Pacer Pacer
	// Sleep is how a pacer's wait is waited, and an unset one waits on a timer.
	Sleep Sleeper
	// Progress is told about every chunk that commits, and an unset one tells nobody.
	Progress func(Progress)
	// Backoff decides whether a statement that gave up on a lock is tried again, and an unset one never does.
	Backoff Backoff
	// Retried is told about every retry before its wait, and an unset one tells nobody.
	Retried func(Retry)
	// Rewrote is told about every operation run as a safer sequence before it runs, and an unset one tells nobody.
	Rewrote func(Rewrote)
	// StatementTimeout bounds each statement and each chunk, and zero bounds none.
	StatementTimeout time.Duration
	// ConcurrentTimeout bounds each statement that blocks no write, in place of StatementTimeout, and zero bounds none.
	ConcurrentTimeout time.Duration
	// Repairer cleans up what a statement outside any transaction left behind, nil where nothing can.
	Repairer driver.Repairer
	// Repaired is told about every repair a run made, and an unset one tells nobody.
	Repaired func(Cleanup)
	// MigrationTimeout bounds each migration, its retries and their backoff included, and zero none.
	MigrationTimeout time.Duration
	// Phases is what each migration declares itself a step of, keyed by version, and nil where none does.
	Phases migration.Phases
	// Gates is every step that waits on another, resolved over the order this run uses.
	Gates map[string]migration.Gate
	// Release names the deploy this run belongs to, which a step waiting on an earlier one is judged by.
	Release string
	// Online is how each migration declares it runs against a live table, keyed by version, nil where none does.
	Online migration.Online
	// Shadower is the session a shadow-table migration changes its table through, nil where none is.
	Shadower driver.Shadower
	// Capture opens the change stream a shadow-table migration keeps its copy current from, nil where none can.
	Capture Capturer
	// CaptureLag is the most a change stream may trail before the copy waits for it, and zero never waits.
	CaptureLag time.Duration
	// Shadowed is told about every moment of a shadow-table migration, and an unset one tells nobody.
	Shadowed func(ShadowProgress)
	// Unattended cuts a shadow-table migration over as soon as its copy is caught up, waiting for no confirmation.
	Unattended bool
	// Released is told about every orphaned change-stream resource a run released, and an unset one tells nobody.
	Released func(ReleasedCapture)
}

// check reports what an apply run was not given, before it takes a lock it could not use.
func (o Options) check() error {
	if o.Planner.Compiler == nil {
		return plan.ErrNoCompiler
	}

	if o.Source == nil {
		return ErrNoSource
	}

	if err := o.checkRecording(); err != nil {
		return err
	}

	if o.Runtime == nil {
		return ErrNoRuntime
	}

	if o.Release != "" {
		if err := history.ValidateRelease(o.Release); err != nil {
			return fmt.Errorf("%w: %w", ErrNoRelease, err)
		}
	}

	return o.Scope.check()
}

// checkRecording reports what an invocation that only writes a history row was not given.
func (o Options) checkRecording() error {
	if o.Clock == nil {
		return ErrNoClock
	}

	return nil
}
