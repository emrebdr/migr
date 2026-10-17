package engine

import (
	"time"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/migration"
)

// Result is what one apply run did, and is populated even when Apply also returns an error.
type Result struct {
	// Batch is the batch every run in this invocation recorded under, zero when nothing ran.
	Batch int64
	// Runs is one entry per migration applied, in the order they were applied.
	Runs []Run
	// Skipped is one entry per migration this invocation did not run, whether by state or by scope.
	Skipped []Skipped
}

// Run is one migration this invocation applied, and what it cost.
type Run struct {
	// Migration is the migration that ran.
	Migration migration.Migration
	// Ordinal is its position within the batch, counting from one.
	Ordinal int32
	// Statements is how many statements it executed.
	Statements int32
	// Started is when the run began, as the injected clock reported it.
	Started time.Time
	// Duration is how long the run took, measured up to the history write rather than the commit.
	Duration time.Duration
	// Chunks is how many chunks it ran a data-only migration in, and zero where it ran statements.
	Chunks int64
	// Written is how many rows those chunks changed or inserted.
	Written int64
	// Resumed reports a run that carried on from the checkpoint an earlier run committed.
	Resumed bool
	// Retries is how many times a statement or a chunk was tried again after giving up on a lock.
	Retries int
}

// Skipped is one migration this invocation did not run, and the state it was left in.
type Skipped struct {
	// Version is the migration's UTC timestamp, which a version only the database carries still has.
	Version string
	// Migration is the file it was found in, zero when only the history names it.
	Migration migration.Migration
	// State is what reconciliation resolved it to, whether that is why it was skipped or not.
	State history.State
}
