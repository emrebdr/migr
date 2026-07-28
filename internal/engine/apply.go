package engine

import (
	"context"
	"fmt"

	"github.com/emrebdr/migr/internal/driver"
)

// Apply runs one migration's compiled statements against a session, as one all-or-nothing unit.
func Apply(ctx context.Context, conn driver.Conn, stmts []driver.Statement) error {
	groups := Groups(stmts)

	if err := checkRunnable(conn, groups); err != nil {
		return err
	}

	return conn.Transact(ctx, func(ctx context.Context) error {
		return run(ctx, conn, groups[0].Statements)
	})
}

// checkRunnable refuses what this executor cannot run atomically, naming the task that will.
func checkRunnable(conn driver.Conn, groups []Group) error {
	if len(groups) == 0 {
		return fmt.Errorf("%w, so applying it would report success for a schema change that never "+
			"happened", ErrNothingToRun)
	}

	caps := conn.Capabilities()

	if !caps.TransactionalDDL() {
		return fmt.Errorf("%w: %s reports no transactional DDL, so a failure partway would leave "+
			"the schema half changed with nothing recording how far it got -- the dirty-state model "+
			"that makes such a run recoverable is M4-T9's", ErrNotAtomic, caps.Driver())
	}

	if len(groups) > 1 {
		return fmt.Errorf("%w: it compiles to %d runs, because %d of its statements declare they "+
			"cannot run inside a transaction, and splitting a migration around them is M13-T4's",
			ErrNotAtomic, len(groups), countStandalone(groups))
	}

	return nil
}

// countStandalone reports how many statements declared they cannot run inside a transaction.
func countStandalone(groups []Group) int {
	total := 0

	for _, group := range groups {
		if !group.Transactional {
			total += len(group.Statements)
		}
	}

	return total
}

// run executes statements in order, naming the one that failed and how many preceded it.
func run(ctx context.Context, conn driver.Conn, stmts []driver.Statement) error {
	var position int32

	for _, stmt := range stmts {
		position++

		if err := conn.Exec(ctx, stmt); err != nil {
			return &StatementError{Position: position, Statement: stmt, Err: err}
		}
	}

	return nil
}
