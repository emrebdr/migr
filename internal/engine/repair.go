package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// Cleanup is one repair a run made of what a statement outside any transaction left behind.
type Cleanup struct {
	// Migration is the migration the statement belongs to.
	Migration migration.Migration
	// Position is the statement's place in the migration, counting from one.
	Position int32
	// Statement is the statement whose leftovers were repaired.
	Statement driver.Statement
	// When is whether it followed a failure or preceded a resume.
	When driver.RepairTime
	// Action is what the repair ran.
	Action string
	// Done reports a resume that found the statement's work already in place, and skipped it.
	Done bool
	// Err is why the repair failed, nil where it did not.
	Err error
}

// repairedError is a failed statement whose leftovers were repaired, carrying whether it may be sent again.
type repairedError struct {
	// err is what the statement failed with.
	err error
	// repaired is what the repair did.
	repaired driver.Repaired
}

// Error says what failed and what the repair did about it.
func (e *repairedError) Error() string {
	if e.repaired.Action == "" {
		return e.err.Error()
	}

	return fmt.Sprintf("%v -- migr cleaned up what it left behind: %s", e.err, e.repaired.Action)
}

// Unwrap reports what the statement failed with.
func (e *repairedError) Unwrap() error {
	return e.err
}

// rerunnable reports a failure whose leftovers were repaired so that the statement may be sent again.
func rerunnable(err error) bool {
	var repaired *repairedError

	return errors.As(err, &repaired) && repaired.repaired.Rerunnable && (retryable(err) || repaired.repaired.Retry)
}

// repairAfter asks the session to clean up after a statement outside any transaction failed, on a context Ctrl-C has not ended.
func (e *executor) repairAfter(ctx context.Context, op ir.Operation, stmt driver.Statement, err error) error {
	if e.opts.Repairer == nil || !stmt.NoTransaction {
		return err
	}

	bound, cancel := bounded(context.WithoutCancel(ctx), e.opts.ConcurrentTimeout, errConcurrentTimeout)
	defer cancel()

	repaired, repairErr := e.opts.Repairer.Repair(bound, op, stmt, driver.AfterFailure, err)
	e.reportCleanup(stmt, driver.AfterFailure, repaired, repairErr)

	switch {
	case repairErr != nil:
		return errors.Join(err, fmt.Errorf("cleaning up after it failed too: %w", repairErr))
	case repaired.Done:
		return nil
	default:
		return &repairedError{err: err, repaired: repaired}
	}
}

// repairBefore asks the session to repair what a stopped run left before a resume reruns its statement, reporting work already done.
func (e *executor) repairBefore(ctx context.Context, op ir.Operation, stmt driver.Statement) (bool, error) {
	if e.recheck == 0 || e.position != e.recheck {
		return false, nil
	}

	e.recheck = 0

	if e.opts.Repairer == nil || !stmt.NoTransaction {
		return false, nil
	}

	bound, cancel := bounded(ctx, e.opts.ConcurrentTimeout, errConcurrentTimeout)
	defer cancel()

	repaired, err := e.opts.Repairer.Repair(bound, op, stmt, driver.BeforeResume, nil)
	e.reportCleanup(stmt, driver.BeforeResume, repaired, err)

	if err != nil {
		return false, fmt.Errorf("repairing what the stopped run left before carrying it on: %w", e.timedOut(bound, err))
	}

	return repaired.Done, nil
}

// reportCleanup keeps and tells whoever is listening about a repair that ran something, or that failed.
func (e *executor) reportCleanup(stmt driver.Statement, when driver.RepairTime, repaired driver.Repaired, err error) {
	if repaired.Action == "" && err == nil {
		return
	}

	cleanup := Cleanup{Migration: e.current, Position: e.position, Statement: stmt, When: when,
		Action: repaired.Action, Done: repaired.Done, Err: err}
	e.repairs = append(e.repairs, cleanup)

	if e.opts.Repaired != nil {
		e.opts.Repaired(cleanup)
	}
}

// takeRepairs returns the running migration's repairs and starts the next one's list empty.
func (e *executor) takeRepairs() []Cleanup {
	taken := e.repairs
	e.repairs = nil

	return taken
}
