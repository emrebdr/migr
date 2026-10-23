package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// Backoff decides whether a statement that gave up waiting for a lock is tried again, and after how long.
type Backoff interface {
	// Delay returns how long to wait before a retry, counting from one, and false once none are left.
	Delay(retry int) (time.Duration, bool)
}

// Exponential is the backoff safety.lock_backoff and safety.lock_retries describe.
type Exponential struct {
	// Base is the first retry's delay, which each further retry doubles.
	Base time.Duration
	// Cap is the longest a delay may grow to, and an unset one lets it grow without bound.
	Cap time.Duration
	// Retries is how many times a statement is tried again after its first attempt.
	Retries int
	// Jitter returns a random duration in [0, span], and an unset one always returns the span.
	Jitter func(span time.Duration) time.Duration
}

// Delay doubles the base per retry and randomizes the upper half, so no retry fires at once.
func (x Exponential) Delay(retry int) (time.Duration, bool) {
	if retry < 1 || retry > x.Retries || x.Base <= 0 {
		return 0, false
	}

	delay := x.doubled(retry)
	fixed := delay / 2

	return fixed + x.jitter(delay-fixed), true
}

// doubled returns the base doubled once per retry after the first, stopping at the cap.
func (x Exponential) doubled(retry int) time.Duration {
	delay := x.Base

	for range retry - 1 {
		if x.Cap > 0 && delay >= x.Cap/2 {
			return x.Cap
		}

		delay *= 2
	}

	if x.Cap > 0 && delay > x.Cap {
		return x.Cap
	}

	return delay
}

// jitter returns the random part of a delay, held to the span it was asked for.
func (x Exponential) jitter(span time.Duration) time.Duration {
	if x.Jitter == nil {
		return span
	}

	return min(max(x.Jitter(span), 0), span)
}

// Retry is one statement lock timeout a run is about to try again, reported before it waits.
type Retry struct {
	// Migration is the migration whose statement gave up waiting, zero where History is set.
	Migration migration.Migration
	// History is set where the wait was spent creating the history tables, before any migration ran.
	History bool
	// Direction is the direction that migration is running in.
	Direction ir.Direction
	// Retry is which retry is about to run, counting from one.
	Retry int
	// Wait is how long the run backs off before it.
	Wait time.Duration
	// Statement is the statement that gave up, counting from one, and zero where it was the commit.
	Statement int32
	// Chunk is the chunk that gave up, counting from one, and zero outside a chunked run.
	Chunk int64
	// Err is the lock timeout itself.
	Err error
}

// LockRetryError is a statement still waiting for a lock after every retry the backoff allowed.
type LockRetryError struct {
	// Attempts is how many times it was tried, the first included.
	Attempts int
	// Waited is how long the run backed off between them in all.
	Waited time.Duration
	// Err is the last attempt's lock timeout.
	Err error
}

// Error renders how often the statement was tried and how long the run backed off between attempts.
func (e *LockRetryError) Error() string {
	return fmt.Sprintf("still waiting for a lock after %d attempts and %s of backoff between them, "+
		"so the run stopped rather than queue behind whoever holds it: %v", e.Attempts, e.Waited, e.Err)
}

// Next says to clear what holds the table, or to wait longer for it, which is two settings.
func (e *LockRetryError) Next() string {
	return "find the session holding the table and let it finish, then run the command again; " +
		"safety.lock_retries and safety.lock_timeout in migr.yaml set how long a run waits"
}

// Unwrap reports the last attempt's failure, so the statement and the lock timeout stay reachable.
func (e *LockRetryError) Unwrap() error {
	return e.Err
}

// attempts is one run's retry bookkeeping, shared by a migration's attempts or by one chunk's.
type attempts struct {
	// retries is how many retries have run.
	retries int
	// waited is how long the run has backed off in all.
	waited time.Duration
}

// retryable reports whether a failure is a lock wait spent, which is the one failure worth repeating.
func retryable(err error) bool {
	return errors.Is(err, driver.ErrStatementLockTimeout)
}

// backoff waits out the next retry's delay, or reports why the failure is final.
func (e *executor) backoff(ctx context.Context, a *attempts, notice Retry) (bool, error) {
	if e.opts.Backoff == nil || (!retryable(notice.Err) && !rerunnable(notice.Err)) {
		return false, nil
	}

	wait, again := e.opts.Backoff.Delay(a.retries + 1)
	if !again {
		return false, nil
	}

	a.retries++
	notice.Retry, notice.Wait = a.retries, wait

	if e.opts.Retried != nil {
		e.opts.Retried(notice)
	}

	if err := e.sleeper()(ctx, wait); err != nil {
		return false, fmt.Errorf("backing off before retry %d: %w", a.retries, err)
	}

	a.waited += wait

	return true, nil
}

// exhausted renders a failure that ends the retries, naming how many there were where there were any.
func (a *attempts) exhausted(err error) error {
	if a.retries == 0 || !retryable(err) {
		return err
	}

	return &LockRetryError{Attempts: a.retries + 1, Waited: a.waited, Err: err}
}

// statementOf names the statement a failure came from, and zero where it came from no statement.
func statementOf(err error) int32 {
	var failure *StatementError
	if errors.As(err, &failure) {
		return failure.Position
	}

	return 0
}
