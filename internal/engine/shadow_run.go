package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// A shadow-table migration is four stages, and each is recorded in the transaction of the work it
// reports, so a run that stops anywhere leaves exactly what committed (ADR 0089). The stream is
// opened before the copy reads a row, so every write the copy misses is in the log after the
// position the start recorded. Copying interleaves a chunk with a read of the log; syncing is the
// same read with nothing left to copy, and it does not end by itself: it is a state that is held
// until a confirmation is read, which is why migration_timeout bounds the copy and not the hold.

// performShadow runs a shadow-table migration from the stage it stands at, through to its cutover.
func (e *executor) performShadow(ctx context.Context, ready prepared, from *history.ShadowState) (Run, error) {
	job, err := e.prepareShadow(ctx, ready, from)
	if err != nil {
		return Run{}, e.fail(ready.item, ready.compiled, err)
	}

	if err := e.runShadow(ctx, job); err != nil {
		return Run{}, e.fail(ready.item, job.ready.compiled, e.shadowFailure(ctx, job, err))
	}

	return Run{
		Migration: ready.item.migration,
		Ordinal:   job.row().Ordinal,
		Started:   job.rec.AppliedAt,
		Duration:  elapsed(job.rec.AppliedAt, e.opts.Clock()),
		Retries:   job.tried.retries,
		Resumed:   job.resumed,
		Holds:     job.paused.holds,
		Held:      job.paused.waited,
		Shadow: &ShadowRun{Table: job.shadow.Table, Retired: job.shadow.Retired,
			Copied: job.state.Copied, Applied: job.state.Applied},
	}, nil
}

// runShadow opens the stream and carries the migration through every stage it has not yet passed.
func (e *executor) runShadow(ctx context.Context, job *shadowJob) (err error) {
	if job.state.Stage == history.StageSyncing {
		if done, err := e.cutOverAlready(ctx, job); done || err != nil {
			return err
		}
	}

	stream, err := e.opts.Capture(ctx, job.request)
	if err != nil {
		return fmt.Errorf("opening the change stream %s: %w", job.request.Name, err)
	}

	defer func() { err = errors.Join(err, e.endStream(ctx, job, stream)) }()

	if !job.marked {
		if err := e.startShadow(ctx, job, stream); err != nil {
			return err
		}
	}

	if job.state.Stage == history.StageCopying {
		if err := e.copyBounded(ctx, job, stream); err != nil {
			return err
		}
	}

	if err := e.holdShadow(ctx, job, stream); err != nil {
		return err
	}

	return e.cutOver(ctx, job, stream)
}

// endStream releases the capture where nothing is left to resume, and keeps it where something is.
func (e *executor) endStream(ctx context.Context, job *shadowJob, stream capture.Stream) error {
	outliving := context.WithoutCancel(ctx)

	if job.cut || !job.marked {
		if err := stream.Drop(outliving); err != nil {
			return fmt.Errorf("releasing the change stream %s: %w", job.request.Name, err)
		}

		return nil
	}

	if err := stream.Close(outliving); err != nil {
		return fmt.Errorf("detaching from the change stream %s: %w", job.request.Name, err)
	}

	return nil
}

// startShadow creates the empty, changed copy and records the start, in one transaction where DDL allows.
func (e *executor) startShadow(ctx context.Context, job *shadowJob, stream capture.Stream) error {
	leftover, err := job.shadower.DropShadow(job.shadow)
	if err != nil {
		return err
	}

	create, err := job.shadower.CreateShadow(ctx, job.shadow)
	if err != nil {
		return err
	}

	job.state = history.ShadowState{
		Version: job.ready.item.migration.Version, Table: job.shadow.Table, Shadow: job.shadow.Shadow,
		Retired: job.shadow.Retired, Capture: job.request.Name, Stage: history.StageCopying,
		Position: string(stream.Start()), UpdatedAt: e.opts.Clock(),
	}

	if err := e.execAll(ctx, leftover); err != nil {
		return fmt.Errorf("dropping a copy an earlier start of this migration left: %w", err)
	}

	if err := e.recordStart(ctx, job, create); err != nil {
		return err
	}

	job.marked, job.markedHere = true, true
	e.shift++
	e.reportShadow(job.shadowProgress())

	return nil
}

// recordStart creates the copy and writes its state and the row marking the run, atomically where it can.
func (e *executor) recordStart(ctx context.Context, job *shadowJob, create []driver.Statement) error {
	record := func(ctx context.Context) error {
		if err := job.store.BeginShadow(ctx, job.state); err != nil {
			return err
		}

		marker := job.rec
		marker.Duration = elapsed(marker.AppliedAt, e.opts.Clock())

		return e.store.MarkDirty(ctx, marker, 1, errShadowing)
	}

	if e.conn.Capabilities().TransactionalDDL() {
		return e.conn.Transact(ctx, func(ctx context.Context) error {
			if err := e.execAll(ctx, create); err != nil {
				return err
			}

			return record(ctx)
		})
	}

	if err := e.execAll(ctx, create); err != nil {
		return err
	}

	return e.conn.Transact(ctx, record)
}

// execAll runs statements in order, stopping at the first that fails.
func (e *executor) execAll(ctx context.Context, stmts []driver.Statement) error {
	for _, stmt := range stmts {
		if err := e.conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}

	return nil
}

// copyBounded fills the copy under safety.migration_timeout, which bounds the copy and never the hold.
func (e *executor) copyBounded(ctx context.Context, job *shadowJob, stream capture.Stream) error {
	bound, cancel := e.migrationBound(ctx)
	defer cancel()

	return e.timedOut(bound, e.copyShadow(bound, job, stream))
}

// copyShadow commits one chunk of the copy and then one read of the log, until the copy is done.
func (e *executor) copyShadow(ctx context.Context, job *shadowJob, stream capture.Stream) error {
	done := Progress{Migration: job.ready.item.migration, Direction: job.ready.compiled.Direction,
		Index: 1, Op: job.shadow.Ops[0], Table: job.shadow.Table, Key: job.shadow.Key,
		Resumed: job.resumed}

	for job.state.Stage == history.StageCopying {
		pace, err := e.paced(ctx, done, &job.paused)
		if err != nil {
			return err
		}

		if err := e.copyOnce(ctx, job, pace.Rows); err != nil {
			return err
		}

		done.Chunks++
		e.reportShadow(job.shadowProgress())

		if err := e.replayCaughtUp(ctx, job, stream, done); err != nil {
			return err
		}
	}

	return nil
}

// replayCaughtUp reads the log once, and again for as long as the stream trails by more than CaptureLag.
func (e *executor) replayCaughtUp(ctx context.Context, job *shadowJob, stream capture.Stream, done Progress) error {
	for {
		batch, err := e.replayOnce(ctx, job, stream)
		if err != nil || e.opts.CaptureLag <= 0 || batch.Behind <= e.opts.CaptureLag {
			return err
		}

		waiting := done
		waiting.Waiting, waiting.Throttle = true, fmt.Sprintf("the change stream %s trails the log by "+
			"%s, above safety.replica_lag_threshold (%s) -- reading it before copying more",
			job.request.Name, batch.Behind.Round(time.Millisecond), e.opts.CaptureLag)
		e.report(waiting)

		job.paused.holds++
	}
}

// copyOnce copies one chunk and records how far the copy got, in one transaction.
func (e *executor) copyOnce(ctx context.Context, job *shadowJob, rows int) error {
	next := job.state

	err := e.shadowTransact(ctx, job, func(ctx context.Context) error {
		bound, cancel := e.statementBound(ctx)
		defer cancel()

		sliced, err := job.shadower.CopyChunk(bound, job.shadow, job.state.Cursor, rows)
		if err = e.timedOut(bound, err); err != nil {
			return fmt.Errorf("copying rows into %s: %w", job.shadow.Shadow, err)
		}

		next = job.state
		next.Cursor, next.Copied, next.UpdatedAt = sliced.Last, job.state.Copied+sliced.Written, e.opts.Clock()

		if sliced.Done {
			next.Stage = history.StageSyncing
		}

		return job.store.SaveShadow(ctx, next)
	})
	if err != nil {
		return err
	}

	job.state = next

	return nil
}

// replayOnce reads the log once and applies what it held to the copy with its position, in one transaction.
func (e *executor) replayOnce(ctx context.Context, job *shadowJob, stream capture.Stream) (capture.Batch, error) {
	batch, err := stream.Next(ctx)
	if err != nil {
		return capture.Batch{}, fmt.Errorf("reading the change stream %s: %w", job.request.Name, err)
	}

	next := job.advanced(batch, e.opts.Clock())

	if len(batch.Changes) > 0 || next.Position != job.state.Position {
		err = e.shadowTransact(ctx, job, func(ctx context.Context) error {
			return e.applyBatch(ctx, job, batch, next)
		})
		if err != nil {
			return capture.Batch{}, err
		}
	}

	job.state = next

	progress := job.shadowProgress()
	progress.Batch, progress.Behind = len(batch.Changes), batch.Behind
	progress.Holding = job.state.Stage == history.StageSyncing
	e.reportShadow(progress)

	return batch, nil
}

// advanced is the state a job stands at once a batch is applied.
func (j *shadowJob) advanced(batch capture.Batch, at time.Time) history.ShadowState {
	next := j.state
	next.Applied += int64(len(batch.Changes))
	next.UpdatedAt = at

	if batch.Through != "" {
		next.Position = string(batch.Through)
	}

	return next
}

// applyBatch writes one batch to the copy and records the position after it, inside the open transaction.
func (e *executor) applyBatch(ctx context.Context, job *shadowJob, batch capture.Batch,
	next history.ShadowState) error {
	if len(batch.Changes) > 0 {
		bound, cancel := e.statementBound(ctx)
		defer cancel()

		if err := e.timedOut(bound, job.shadower.ApplyChanges(bound, job.shadow, batch.Changes)); err != nil {
			return fmt.Errorf("applying %d captured changes to %s: %w", len(batch.Changes),
				job.shadow.Shadow, err)
		}
	}

	return job.store.SaveShadow(ctx, next)
}

// shadowTransact runs one transaction of a shadow-table migration, again after a lock wait while retries remain.
func (e *executor) shadowTransact(ctx context.Context, job *shadowJob, work driver.Work) error {
	for {
		err := e.conn.Transact(ctx, work)
		if err == nil {
			return nil
		}

		again, waitErr := e.backoff(ctx, &job.tried, Retry{Migration: job.ready.item.migration,
			Direction: job.ready.compiled.Direction, Err: err})
		if waitErr != nil {
			return errors.Join(err, waitErr)
		}

		if !again {
			return job.tried.exhausted(err)
		}
	}
}

// holdShadow keeps the filled copy current from the log until a cutover is confirmed, however long that is.
func (e *executor) holdShadow(ctx context.Context, job *shadowJob, stream capture.Stream) error {
	for {
		batch, err := e.replayOnce(ctx, job, stream)
		if err != nil {
			return err
		}

		confirmed, err := e.confirmed(ctx, job)
		if err != nil || confirmed {
			return err
		}

		if len(batch.Changes) == 0 {
			if err := e.sleeper()(ctx, shadowPoll); err != nil {
				return fmt.Errorf("holding %s in sync until its cutover is confirmed: %w",
					job.shadow.Shadow, err)
			}
		}
	}
}

// confirmed reports whether someone has confirmed this migration's cutover, which only its state row says.
func (e *executor) confirmed(ctx context.Context, job *shadowJob) (bool, error) {
	states, err := job.store.ShadowStates(ctx)
	if err != nil {
		return false, fmt.Errorf("reading whether the cutover of %s is confirmed: %w",
			job.ready.item.migration.Filename(), err)
	}

	state, found := history.FindShadow(states, job.state.Version)

	return found && state.Confirmed, nil
}

// cutOver swaps the copy in under the driver's brief lock, catching up on the log first, and records it.
func (e *executor) cutOver(ctx context.Context, job *shadowJob, stream capture.Stream) error {
	err := e.shadowTransact(ctx, job, func(ctx context.Context) error {
		if err := job.shadower.Cutover(ctx, job.shadow, e.catchUp(job, stream)); err != nil {
			return fmt.Errorf("swapping %s in for %s: %w", job.shadow.Shadow, job.shadow.Table, err)
		}

		return e.recordCutover(ctx, job)
	})
	if err != nil {
		return err
	}

	job.cut = true
	e.reportShadow(job.shadowProgress())

	return nil
}

// catchUp applies every change the log holds up to now, inside the cutover's own transaction.
func (e *executor) catchUp(job *shadowJob, stream capture.Stream) driver.CatchUp {
	return func(ctx context.Context) error {
		for {
			batch, err := stream.Next(ctx)
			if err != nil {
				return fmt.Errorf("reading the change stream %s: %w", job.request.Name, err)
			}

			next := job.advanced(batch, e.opts.Clock())
			if err := e.applyBatch(ctx, job, batch, next); err != nil {
				return err
			}

			job.state = next

			if len(batch.Changes) == 0 {
				return nil
			}
		}
	}
}

// recordCutover records the stage and appends the applied row, inside the cutover's transaction.
func (e *executor) recordCutover(ctx context.Context, job *shadowJob) error {
	next := job.state
	next.Stage, next.UpdatedAt = history.StageCutOver, e.opts.Clock()

	if err := job.store.SaveShadow(ctx, next); err != nil {
		return err
	}

	rec := job.row()
	if err := e.recorder(&rec)(ctx); err != nil {
		return err
	}

	job.state = next

	return nil
}

// cutOverAlready finishes recording a cutover whose swap committed and whose record did not.
func (e *executor) cutOverAlready(ctx context.Context, job *shadowJob) (bool, error) {
	snapshot, err := e.conn.Introspect(ctx)
	if err != nil {
		return false, fmt.Errorf("reading whether %s was already swapped in: %w", job.shadow.Shadow, err)
	}

	_, shadowLeft := snapshot.Table(job.shadow.Shadow)
	_, retired := snapshot.Table(job.shadow.Retired)

	if shadowLeft || !retired {
		return false, nil
	}

	if err := e.conn.Transact(ctx, func(ctx context.Context) error {
		return e.recordCutover(ctx, job)
	}); err != nil {
		return false, err
	}

	job.cut = true

	return true, e.releaseOnly(ctx, job)
}

// releaseOnly releases the capture of a migration that has cut over, opening the stream only to drop it.
func (e *executor) releaseOnly(ctx context.Context, job *shadowJob) error {
	stream, err := e.opts.Capture(ctx, job.request)
	if err != nil {
		return fmt.Errorf("opening the change stream %s to release it: %w", job.request.Name, err)
	}

	return e.endStream(ctx, job, stream)
}

// shadowFailure records a stopped run: failed and cleaned where nothing was recorded, else dirty and kept.
func (e *executor) shadowFailure(ctx context.Context, job *shadowJob, cause error) error {
	outliving := context.WithoutCancel(ctx)
	rec := job.row()
	rec.Duration = elapsed(rec.AppliedAt, e.opts.Clock())

	if !job.marked {
		return errors.Join(cause, e.dropCopy(outliving, job), e.store.MarkFailed(outliving, rec, cause))
	}

	stopped := &ShadowError{Migration: job.ready.item.migration, State: job.state, Err: cause}

	return errors.Join(stopped, e.store.MarkDirty(outliving, rec, 1, cause))
}

// dropCopy drops the copy a start that recorded nothing may have left behind.
func (e *executor) dropCopy(ctx context.Context, job *shadowJob) error {
	stmts, err := job.shadower.DropShadow(job.shadow)
	if err != nil {
		return err
	}

	if err := e.execAll(ctx, stmts); err != nil {
		return fmt.Errorf("dropping %s, which a start that recorded nothing left: %w", job.shadow.Shadow, err)
	}

	return nil
}

// ShadowError is a shadow-table migration that stopped with its copy and its capture kept for the next run.
type ShadowError struct {
	// Migration is the migration that stopped.
	Migration migration.Migration
	// State is where it stood when it stopped, as recorded.
	State history.ShadowState
	// Err is what stopped it.
	Err error
}

// Error says where the migration stopped, what was kept, and the two ways on from there.
func (e *ShadowError) Error() string {
	return fmt.Sprintf("%s stopped while %s through the shadow table %s: %v. The copy and the change "+
		"stream %s are kept, and every change before log position %q is applied to it -- the next "+
		"migr up carries it on from there, and migr resolve --rolled-back abandons it, dropping the "+
		"copy and releasing the stream", e.Migration.Filename(), describeStage(e.State.Stage),
		e.State.Shadow, e.Err, e.State.Capture, e.State.Position)
}

// Unwrap returns what stopped the migration.
func (e *ShadowError) Unwrap() error {
	return e.Err
}

// describeStage says what a migration at a stage was doing, as a sentence reads it.
func describeStage(stage history.Stage) string {
	switch stage {
	case history.StageCopying:
		return "copying"
	case history.StageSyncing:
		return "holding in sync"
	case history.StageCutOver:
		return "cutting over"
	}

	return "starting"
}

// resumeShadow carries a stopped shadow-table migration on from the stage and position it recorded.
func (e *executor) resumeShadow(ctx context.Context, item queued) (Run, error) {
	if item.source != item.recorded.Checksum.Source {
		return Run{}, driftedError(statusOf(item), ir.Up, true)
	}

	ready, err := e.forward(ctx, item)
	if err != nil {
		return Run{}, err
	}

	if ready.sum.IR != item.recorded.Checksum.IR {
		return Run{}, e.fail(item, ready.compiled, emittedDifferentlyError(item.recorded.Checksum.IR,
			ready.sum.IR))
	}

	if !e.shadows(item.migration) {
		return Run{}, e.fail(item, ready.compiled, fmt.Errorf("%w: it stopped partway through a "+
			"shadow-table migration, and it no longer declares one -- declare it again to carry it on, "+
			"or abandon it with migr resolve --rolled-back", ErrNotResumable))
	}

	return e.performShadow(ctx, ready, item.shadow)
}
