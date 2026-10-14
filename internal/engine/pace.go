package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// Progress is how far a chunked data operation has got, reported after every chunk commits.
type Progress struct {
	// Migration is the migration the operation belongs to.
	Migration migration.Migration
	// Direction is the direction that migration is running in.
	Direction ir.Direction
	// Index is the operation's place in the migration, counting from one.
	Index int32
	// Op is the operation running in chunks.
	Op ir.Operation
	// Table is the table whose key the chunks page through.
	Table string
	// Key names that table's primary key, in key order.
	Key []string
	// Chunks is how many chunks of this operation have committed, a resumed run's included.
	Chunks int64
	// Written is how many rows those chunks changed or inserted.
	Written int64
	// Cursor is the key of the last row the last committed chunk covered, empty before the first.
	Cursor []string
	// Done reports the operation's last chunk, after which no row lay beyond the cursor.
	Done bool
	// Throttle is why the pacer held the chunk that just committed back, and empty where it did not.
	Throttle string
	// Resumed reports a run that carried on from a checkpoint an earlier run committed.
	Resumed bool
}

// Pace is what a pacer allows the next chunk: how many rows, and how long to wait before it.
type Pace struct {
	// Rows is the most rows the next chunk covers.
	Rows int
	// Wait is how long to wait before the next chunk starts.
	Wait time.Duration
	// Throttle says why the wait is longer than the pacer's own pause, and is empty where it is not.
	Throttle string
}

// Pacer sizes and times every chunk, the first included, and is the seam M13-T8 drives from replica lag.
type Pacer interface {
	// Pace returns what the next chunk is allowed, given what has committed so far.
	Pace(ctx context.Context, done Progress) (Pace, error)
}

// Steady is the pacer every chunk gets the same size and the same pause from.
type Steady struct {
	// Rows is how many rows each chunk covers.
	Rows int
	// Pause is how long to wait between two chunks, and not before the first.
	Pause time.Duration
}

// Pace returns the same size for every chunk, waiting the pause before every chunk but the first.
func (s Steady) Pace(_ context.Context, done Progress) (Pace, error) {
	if done.Chunks == 0 {
		return Pace{Rows: s.Rows}, nil
	}

	return Pace{Rows: s.Rows, Wait: s.Pause}, nil
}

// Sleeper waits a duration, returning early with the context's error when it is canceled.
type Sleeper func(ctx context.Context, d time.Duration) error

// sleep waits a duration on a timer, returning early with the context's error when it is canceled.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// pacer returns the pacer this run was given, or a steady one sized by the planner's chunk.
func (e *executor) pacer() Pacer {
	if e.opts.Pacer != nil {
		return e.opts.Pacer
	}

	return Steady{Rows: e.opts.Planner.ChunkRows}
}

// sleeper returns the wait this run was given, or a timer.
func (e *executor) sleeper() Sleeper {
	if e.opts.Sleep != nil {
		return e.opts.Sleep
	}

	return sleep
}

// paced asks the pacer for the next chunk and waits as long as it says, refusing a chunk of no rows.
func (e *executor) paced(ctx context.Context, done Progress) (Pace, error) {
	next, err := e.pacer().Pace(ctx, done)
	if err != nil {
		return Pace{}, fmt.Errorf("pacing chunk %d of operation %d (%s): %w", done.Chunks+1,
			done.Index, done.Op.Kind(), err)
	}

	if next.Rows < 1 {
		return Pace{}, fmt.Errorf("pacing chunk %d of operation %d (%s): the pacer allowed %d "+
			"rows, and a chunk covers at least one", done.Chunks+1, done.Index, done.Op.Kind(),
			next.Rows)
	}

	if err := e.sleeper()(ctx, next.Wait); err != nil {
		return Pace{}, fmt.Errorf("waiting %s before chunk %d of operation %d (%s): %w", next.Wait,
			done.Chunks+1, done.Index, done.Op.Kind(), err)
	}

	return next, nil
}

// report hands one committed chunk's progress to whoever this run was told to tell.
func (e *executor) report(p Progress) {
	if e.opts.Progress != nil {
		e.opts.Progress(p)
	}
}
