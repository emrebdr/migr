package engine

import (
	"context"
	"time"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// Source yields the operations one migration emits, and is asked only about migrations that run.
type Source interface {
	// Emit returns the operations one migration performs, or ErrNoDownAuthored for a down it has none of.
	Emit(ctx context.Context, m migration.Migration, dir ir.Direction) (ir.Operations, error)
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
	// Runtime is the language migrations are written in, spelled as migr.yaml spells it.
	Runtime string
}

// check reports what an apply run was not given, before it takes a lock it could not use.
func (o Options) check() error {
	if o.Planner.Compiler == nil {
		return plan.ErrNoCompiler
	}

	if o.Source == nil {
		return ErrNoSource
	}

	if o.Clock == nil {
		return ErrNoClock
	}

	if o.Runtime == "" {
		return ErrNoRuntime
	}

	return nil
}
