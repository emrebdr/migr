package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/driver"
)

// idle is the transaction status of a session with no transaction open.
const idle byte = 'I'

// aborted is the transaction status of a session whose transaction failed and awaits a rollback.
const aborted byte = 'E'

// runner is what a query is sent through: the open transaction, or the session itself.
type runner interface {
	// Exec runs a statement, returning what the server said it did.
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	// QueryRow runs a query returning at most one row.
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// runner returns the open transaction when there is one, so nothing can bypass it unnoticed.
func (c *pgConn) runner() runner {
	if c.tx != nil {
		return c.tx
	}

	return c.conn
}

// Transact runs work inside one transaction, committing it only if work returns nil.
func (c *pgConn) Transact(ctx context.Context, work driver.Work) (err error) {
	if work == nil {
		return fmt.Errorf("%w: asked to run no work at all", ErrTransaction)
	}

	tx, err := c.begin(ctx)
	if err != nil {
		return err
	}

	returned := false

	defer func() { err = c.end(ctx, tx, returned, err) }()

	err = work(ctx)
	returned = true

	return err
}

// begin opens the transaction, refusing a session that is already inside one.
func (c *pgConn) begin(ctx context.Context) (pgx.Tx, error) {
	if err := c.checkIdle(); err != nil {
		return nil, err
	}

	tx, err := c.conn.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: beginning it: %w", ErrTransaction, err)
	}

	c.tx = tx

	return tx, nil
}

// checkIdle refuses a transaction on a session that has one, however that one was opened.
func (c *pgConn) checkIdle() error {
	if c.tx != nil {
		return fmt.Errorf("%w: %w", ErrTransaction, ErrTransactionOpen)
	}

	if status := c.conn.PgConn().TxStatus(); status != idle {
		return fmt.Errorf("%w: %w, and this driver did not open it: the server reports transaction "+
			"status %q", ErrTransaction, ErrTransactionOpen, string(status))
	}

	return nil
}

// end commits when work returned without error, and otherwise discards everything it did.
func (c *pgConn) end(ctx context.Context, tx pgx.Tx, returned bool, cause error) error {
	c.tx = nil

	if returned && cause == nil {
		return c.commit(ctx, tx)
	}

	return errors.Join(cause, c.rollback(ctx, tx))
}

// commit makes the transaction permanent, treating one the server discarded as the failure it is.
func (c *pgConn) commit(ctx context.Context, tx pgx.Tx) error {
	err := tx.Commit(ctx)
	if err == nil {
		return nil
	}

	if errors.Is(err, pgx.ErrTxCommitRollback) {
		return fmt.Errorf("%w: %w: %w", ErrTransaction, ErrCommitDiscarded, err)
	}

	return fmt.Errorf("%w: committing it: %w", ErrTransaction, err)
}

// rollback discards the transaction on a context that outlives whatever canceled the work.
func (c *pgConn) rollback(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Rollback(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("%w: rolling it back: %w", ErrTransaction, err)
	}

	return nil
}
