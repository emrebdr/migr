package engine

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Bound names which limit a run ran past, by the migr.yaml key an operator raises.
type Bound string

// The two bounds a run is held to (ADR 0083).
const (
	// BoundStatement is one statement's limit, safety.statement_timeout.
	BoundStatement Bound = "safety.statement_timeout"
	// BoundMigration is one migration's limit, its retries and their backoff included.
	BoundMigration Bound = "safety.migration_timeout"
)

// errStatementTimeout is the cause a statement's own deadline carries, which tells it from a cancel.
var errStatementTimeout = errors.New("the statement ran past its bound")

// errMigrationTimeout is the cause a migration's own deadline carries.
var errMigrationTimeout = errors.New("the migration ran past its bound")

// TimeoutError is a run stopped by one of its own bounds, the server having been told to stop it.
type TimeoutError struct {
	// Bound is which limit was reached.
	Bound Bound
	// Limit is what that limit is set to.
	Limit time.Duration
	// Err is what the stopped statement, or the stopped wait, reported.
	Err error
}

// Error renders the limit that was reached and the key that raises it.
func (e *TimeoutError) Error() string {
	return fmt.Sprintf("ran past %s (%s), so the server was told to stop it and nothing it was doing "+
		"is still holding a lock; raise %s if it needs longer: %v", e.Bound, e.Limit, e.Bound, e.Err)
}

// Unwrap reports what was stopped, so the statement and the migration stay reachable.
func (e *TimeoutError) Unwrap() error {
	return e.Err
}

// bounded derives a context ending after a limit with a cause naming it, or returns ctx where there is none.
func bounded(ctx context.Context, limit time.Duration, cause error) (context.Context, context.CancelFunc) {
	if limit <= 0 {
		return ctx, func() {}
	}

	return context.WithTimeoutCause(ctx, limit, cause)
}

// timedOut wraps a failure in the bound that ended its context, and leaves any other failure as it was.
func (e *executor) timedOut(ctx context.Context, err error) error {
	if err == nil || ctx.Err() == nil {
		return err
	}

	var already *TimeoutError
	if errors.As(err, &already) {
		return err
	}

	cause := context.Cause(ctx)

	switch {
	case errors.Is(cause, errStatementTimeout):
		return &TimeoutError{Bound: BoundStatement, Limit: e.opts.StatementTimeout, Err: err}
	case errors.Is(cause, errMigrationTimeout):
		return &TimeoutError{Bound: BoundMigration, Limit: e.opts.MigrationTimeout, Err: err}
	default:
		return err
	}
}

// statementBound derives the context one statement runs under.
func (e *executor) statementBound(ctx context.Context) (context.Context, context.CancelFunc) {
	return bounded(ctx, e.opts.StatementTimeout, errStatementTimeout)
}

// migrationBound derives the context one migration runs under, its retries included.
func (e *executor) migrationBound(ctx context.Context) (context.Context, context.CancelFunc) {
	return bounded(ctx, e.opts.MigrationTimeout, errMigrationTimeout)
}
