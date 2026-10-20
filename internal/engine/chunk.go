package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
)

// A data-only migration runs one keyed chunk at a time, each chunk its own transaction (ADR 0078).
// The chunk's transaction carries its checkpoint, so how far the run got is committed exactly when
// the rows are -- and the first chunk's carries a dirty row as well, so a run killed between two
// chunks leaves the evidence a failed one does. The last chunk's carries the applied row and clears
// the checkpoint instead. Those are two rows for one migration in one batch, which is why a run that
// marks itself in progress takes the next ordinal for everything it writes afterwards.

// errChunking is what the row a chunked run writes at its first chunk records, until the run finishes.
var errChunking = errors.New("running in chunks: each chunk commits on its own, and the " +
	"checkpoint beside the history records how far it got -- the next migr up resumes from it")

// errNoSnapshot is a session that answered a read of its schema with nothing at all.
var errNoSnapshot = errors.New("the session reported no schema at all")

// chunkKey is the table one chunked operation pages through and the primary key it pages by.
type chunkKey struct {
	// table is the table whose key the chunks page through.
	table string
	// columns name its primary key, in key order.
	columns []string
}

// chunkRun is one chunked migration's run: the row it writes, and how far it has got.
type chunkRun struct {
	// ready is the migration, compiled.
	ready prepared
	// rec is the row this run writes, at the ordinal it started at.
	rec history.Record
	// marked reports that the database holds a dirty row saying this migration is in progress.
	marked bool
	// markedHere reports that this run wrote that row, and so writes everything after it one ordinal on.
	markedHere bool
	// at is the last checkpoint committed, nil before the first.
	at *history.Checkpoint
	// index is the operation in progress, counting from one.
	index int32
	// chunks is how many chunks this run committed.
	chunks int64
	// written is how many rows those chunks changed or inserted.
	written int64
	// tried is how often a chunk was tried again after giving up on a lock, across every chunk.
	tried attempts
	// holds is how often a pacer held a chunk back, every poll counted.
	holds int
	// waited is how long those holds waited together.
	waited time.Duration
}

// performChunked runs a data-only migration a chunk at a time, from a checkpoint where one is given.
func (e *executor) performChunked(ctx context.Context, ready prepared, from *history.Checkpoint) (Run, error) {
	run := &chunkRun{ready: ready, rec: e.newRecord(ready), marked: from != nil, at: from}
	if from != nil {
		run.index = from.Step + 1
	}

	bound, cancel := e.migrationBound(ctx)
	defer cancel()

	if err := e.timedOut(bound, e.runChunked(bound, run)); err != nil {
		return Run{}, e.fail(ready.item, ready.compiled, e.chunkFailure(ctx, run, err))
	}

	return Run{
		Migration: ready.item.migration,
		Ordinal:   run.ordinal(),
		Started:   run.rec.AppliedAt,
		Duration:  elapsed(run.rec.AppliedAt, e.opts.Clock()),
		Chunks:    run.chunks,
		Written:   run.written,
		Retries:   run.tried.retries,
		Resumed:   from != nil,
		Holds:     run.holds,
		Held:      run.waited,
	}, nil
}

// ordinal returns the ordinal the run's next row is written at, one on from a row it wrote itself.
func (r *chunkRun) ordinal() int32 {
	if r.markedHere {
		return r.rec.Ordinal + 1
	}

	return r.rec.Ordinal
}

// row returns the row the run's next write appends, at the ordinal that write takes.
func (r *chunkRun) row() history.Record {
	rec := r.rec
	rec.Ordinal = r.ordinal()

	return rec
}

// runChunked runs every operation from the one the checkpoint names, each through its own chunks.
func (e *executor) runChunked(ctx context.Context, run *chunkRun) error {
	chunker, store, err := e.chunking()
	if err != nil {
		return err
	}

	keys, err := e.chunkKeys(ctx, run.ready.compiled)
	if err != nil {
		return err
	}

	start := 0
	if run.at != nil {
		start = int(run.at.Step)
	}

	for i := start; i < len(run.ready.compiled.Steps); i++ {
		if err := e.runOperation(ctx, run, chunker, store, i, keys[i]); err != nil {
			return err
		}
	}

	return nil
}

// chunking returns the session that runs chunks and the store that remembers them, refusing either absent.
func (e *executor) chunking() (driver.Chunker, history.Checkpointer, error) {
	if e.opts.Chunker == nil {
		return nil, nil, fmt.Errorf("%w: this session cannot run a data operation a chunk at a "+
			"time; set safety.chunk_rows to 0 to run each as one statement", ErrCannotChunk)
	}

	store, remembers := e.store.(history.Checkpointer)
	if !remembers {
		return nil, nil, fmt.Errorf("%w: this session's history cannot record how far a chunked "+
			"operation got; set safety.chunk_rows to 0 to run each as one statement", ErrCannotChunk)
	}

	return e.opts.Chunker, store, nil
}

// chunkKeys reads the primary key every chunked operation pages by, before any chunk runs.
func (e *executor) chunkKeys(ctx context.Context, compiled plan.Plan) ([]chunkKey, error) {
	snapshot, err := e.conn.Introspect(ctx)
	if err == nil && snapshot == nil {
		err = errNoSnapshot
	}

	if err != nil {
		return nil, fmt.Errorf("reading the primary keys the chunks page by: %w", err)
	}

	keys := make([]chunkKey, len(compiled.Steps))

	for i, step := range compiled.Steps {
		table := ChunkTable(step.Op)

		found, present := snapshot.Table(table)
		if !present {
			return nil, &ChunkError{Index: step.Index, Op: step.Op, Err: fmt.Errorf(
				"%w: %q is not in the database, so it has no key to page through", ErrNoChunkKey,
				table)}
		}

		if found.PrimaryKey == nil || len(found.PrimaryKey.Columns) == 0 {
			return nil, &ChunkError{Index: step.Index, Op: step.Op, Err: noKeyError(table)}
		}

		keys[i] = chunkKey{table: table, columns: found.PrimaryKey.Columns}
	}

	return keys, nil
}

// noKeyError refuses an operation over a table no chunk can page through, naming the two ways on.
func noKeyError(table string) error {
	return fmt.Errorf("%w: %q has no primary key, and a chunk is a range of one -- give the table "+
		"a primary key, or set safety.chunk_rows to 0 to run the operation as one statement over "+
		"the whole table", ErrNoChunkKey, table)
}

// ChunkTable names the table a data operation's chunks page through, which for a copy is its source.
func ChunkTable(op ir.Operation) string {
	switch o := op.(type) {
	case ir.DataBackfill:
		return o.Table
	case ir.DataTransform:
		return o.Table
	case ir.DataCopy:
		return o.From
	default:
		return ""
	}
}

// runOperation runs one operation chunk by chunk until a chunk finds nothing beyond it.
func (e *executor) runOperation(ctx context.Context, run *chunkRun, chunker driver.Chunker,
	store history.Checkpointer, at int, key chunkKey,
) error {
	step := run.ready.compiled.Steps[at]
	run.index = step.Index
	done := run.progressFrom(at, step, key)

	for {
		pace, err := e.paced(ctx, done, run)
		if err != nil {
			return &ChunkError{Index: step.Index, Op: step.Op, Chunk: done.Chunks + 1, Err: err}
		}

		sliced, err := e.chunkOnce(ctx, run, chunker, store, at, done, pace.Rows)
		if err != nil {
			return err
		}

		done.Chunks++
		done.Written += sliced.Written
		done.Cursor = sliced.Last
		done.Done = sliced.Done
		done.Throttle = pace.Throttle
		run.chunks++
		run.written += sliced.Written

		e.report(done)

		if sliced.Done {
			return nil
		}
	}
}

// progressFrom is where one operation starts: from its checkpoint where it has one, else from nothing.
func (r *chunkRun) progressFrom(at int, step plan.Step, key chunkKey) Progress {
	done := Progress{
		Migration: r.ready.compiled.Migration,
		Direction: r.ready.compiled.Direction,
		Index:     step.Index,
		Op:        step.Op,
		Table:     key.table,
		Key:       key.columns,
		Resumed:   r.at != nil,
	}

	if r.at != nil && int(r.at.Step) == at {
		done.Chunks = r.at.Chunks
		done.Written = r.at.Written
		done.Cursor = r.at.Cursor
	}

	return done
}

// chunkOnce runs one chunk and everything recording it inside one transaction, and notes what committed.
func (e *executor) chunkOnce(ctx context.Context, run *chunkRun, chunker driver.Chunker,
	store history.Checkpointer, at int, done Progress, rows int,
) (driver.Sliced, error) {
	var sliced driver.Sliced

	last := at == len(run.ready.compiled.Steps)-1
	marking := false
	next := history.Checkpoint{}

	err := e.retryChunk(ctx, run, done, func(ctx context.Context) error {
		out, err := e.chunkBounded(ctx, chunker, driver.Slice{Op: done.Op, Key: done.Key,
			After: done.Cursor, Rows: rows})
		if err != nil {
			return &ChunkError{Index: done.Index, Op: done.Op, Chunk: done.Chunks + 1, Err: err}
		}

		sliced = out

		if out.Done && last {
			return e.finishChunks(ctx, run, store)
		}

		next = run.checkpointAfter(done, out)
		marking = !run.marked

		return e.recordChunk(ctx, run, store, next, marking)
	})
	if err != nil {
		return driver.Sliced{}, err
	}

	if marking {
		run.marked, run.markedHere = true, true
		e.shift++
	}

	if !sliced.Done || !last {
		run.at = &next
	}

	return sliced, nil
}

// chunkBounded runs one chunk's statements under safety.statement_timeout, naming the bound where reached.
func (e *executor) chunkBounded(ctx context.Context, chunker driver.Chunker, s driver.Slice,
) (driver.Sliced, error) {
	bound, cancel := e.statementBound(ctx)
	defer cancel()

	out, err := chunker.Chunk(bound, s)

	return out, e.timedOut(bound, err)
}

// retryChunk runs one chunk's transaction, running it again after a lock wait spent while retries remain.
func (e *executor) retryChunk(ctx context.Context, run *chunkRun, done Progress, work driver.Work) error {
	for {
		err := e.conn.Transact(ctx, work)
		if err == nil {
			return nil
		}

		again, waitErr := e.backoff(ctx, &run.tried, Retry{Migration: run.ready.item.migration,
			Direction: run.ready.compiled.Direction, Chunk: done.Chunks + 1, Err: err})
		if waitErr != nil {
			return errors.Join(err, waitErr)
		}

		if !again {
			return run.tried.exhausted(err)
		}
	}
}

// checkpointAfter is what a committed chunk leaves: the cursor it reached, or the next operation's start.
func (r *chunkRun) checkpointAfter(done Progress, out driver.Sliced) history.Checkpoint {
	cp := history.Checkpoint{
		Version:   r.ready.item.migration.Version,
		Direction: r.ready.compiled.Direction,
		Step:      done.Index - 1,
		Cursor:    out.Last,
		Chunks:    done.Chunks + 1,
		Written:   done.Written + out.Written,
	}

	if out.Done {
		cp.Step, cp.Cursor, cp.Chunks, cp.Written = done.Index, nil, 0, 0
	}

	return cp
}

// recordChunk writes a chunk's checkpoint, and the row marking the run in progress where none says so yet.
func (e *executor) recordChunk(ctx context.Context, run *chunkRun, store history.Checkpointer,
	next history.Checkpoint, marking bool,
) error {
	if marking {
		marker := run.row()
		marker.Duration = elapsed(marker.AppliedAt, e.opts.Clock())

		if err := e.store.MarkDirty(ctx, marker, run.index, errChunking); err != nil {
			return fmt.Errorf("recording that %s is running in chunks: %w",
				run.ready.item.migration.Filename(), err)
		}
	}

	if err := store.SaveCheckpoint(ctx, next); err != nil {
		return fmt.Errorf("recording how far %s got: %w", run.ready.item.migration.Filename(), err)
	}

	return nil
}

// finishChunks clears the checkpoint and appends the applied row, in the last chunk's transaction.
func (e *executor) finishChunks(ctx context.Context, run *chunkRun, store history.Checkpointer) error {
	if err := store.ClearCheckpoint(ctx, run.ready.item.migration.Version,
		run.ready.compiled.Direction); err != nil {
		return fmt.Errorf("clearing the checkpoint of %s: %w", run.ready.item.migration.Filename(), err)
	}

	rec := run.row()

	return e.recorder(&rec)(ctx)
}

// chunkFailure records a chunked run that stopped: failed where nothing committed, dirty where a chunk did.
func (e *executor) chunkFailure(ctx context.Context, run *chunkRun, cause error) error {
	rec := run.row()
	rec.Duration = elapsed(rec.AppliedAt, e.opts.Clock())
	outliving := context.WithoutCancel(ctx)

	if !run.marked {
		return errors.Join(cause, e.store.MarkFailed(outliving, rec, cause))
	}

	stopped := &DirtyError{
		Migration:        run.ready.item.migration,
		Direction:        run.ready.compiled.Direction,
		LastStatement:    max(run.index, 1),
		Statements:       run.ready.compiled.Len(),
		Batch:            e.batch,
		Ordinal:          rec.Ordinal,
		TransactionalDDL: e.conn.Capabilities().TransactionalDDL(),
		Checkpoint:       run.at,
		Err:              cause,
	}

	return errors.Join(stopped, e.store.MarkDirty(outliving, rec, max(run.index, 1), cause))
}

// checkpoints reads every checkpoint this session's history remembers, and none where it remembers none.
func (e *executor) checkpoints(ctx context.Context) ([]history.Checkpoint, error) {
	store, remembers := e.store.(history.Checkpointer)
	if !remembers {
		return nil, nil
	}

	cps, err := store.Checkpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading how far a chunked migration got: %w", err)
	}

	return cps, nil
}

// resumeChunks carries a chunked migration on from the checkpoint its stopped run committed.
func (e *executor) resumeChunks(ctx context.Context, item queued) (Run, error) {
	if item.source != item.recorded.Checksum.Source {
		return Run{}, driftedError(statusOf(item), ir.Up, true)
	}

	ready, err := e.forward(ctx, item)
	if err != nil {
		return Run{}, err
	}

	if err := checkChunkResume(ready, item.recorded, *item.resume); err != nil {
		return Run{}, e.fail(item, plan.Plan{}, err)
	}

	return e.performChunked(ctx, ready, item.resume)
}

// statusOf is the reconciled status a queued migration was taken from, for the errors that name one.
func statusOf(item queued) history.Status {
	return history.Status{
		Version: item.migration.Version, Migration: item.migration, Source: item.source,
		Recorded: item.recorded, State: item.state,
	}
}

// checkChunkResume refuses a resume whose checkpoint no longer names a chunk of what this run would do.
func checkChunkResume(ready prepared, recorded history.Record, cp history.Checkpoint) error {
	if ready.sum.IR != recorded.Checksum.IR {
		return emittedDifferentlyError(recorded.Checksum.IR, ready.sum.IR)
	}

	if ready.compiled.Chunked() == 0 {
		return fmt.Errorf("%w: it stopped partway through running in chunks, and this run would "+
			"not run it in chunks -- safety.chunk_rows is 0 here. Set it again to carry on from "+
			"the checkpoint, or finish the change yourself and record what you did with migr "+
			"resolve --applied or --rolled-back", ErrNotResumable)
	}

	if int(cp.Step) >= len(ready.compiled.Steps) {
		return fmt.Errorf("%w: its checkpoint names operation %d and it has %d", ErrNotResumable,
			cp.Step+1, len(ready.compiled.Steps))
	}

	return nil
}
