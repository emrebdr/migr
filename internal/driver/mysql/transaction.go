package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/driver"
)

// runner is what a statement is sent through: the open transaction, or the pinned connection.
type runner interface {
	// ExecContext runs a statement, returning what the server said it did.
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	// QueryContext runs a query returning any number of rows.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	// QueryRowContext runs a query returning at most one row.
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// runner returns the open transaction when there is one, so nothing can bypass it unnoticed.
func (c *mysqlConn) runner() runner {
	if c.tx != nil {
		return c.tx
	}

	return c.conn
}

// Transact runs work inside one transaction, committing it only if work returns nil.
func (c *mysqlConn) Transact(ctx context.Context, work driver.Work) (err error) {
	if work == nil {
		return fmt.Errorf("%w: asked to run no work at all", ErrTransaction)
	}

	tx, err := c.begin(ctx)
	if err != nil {
		return err
	}

	returned := false

	defer func() { err = c.end(tx, returned, err) }()

	err = work(ctx)
	returned = true

	return err
}

// begin opens the transaction, refusing a session that is already inside one.
func (c *mysqlConn) begin(ctx context.Context) (*sql.Tx, error) {
	if err := c.checkIdle(); err != nil {
		return nil, err
	}

	tx, err := c.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: beginning it: %w", ErrTransaction, err)
	}

	c.tx = tx

	return tx, nil
}

// checkIdle refuses a transaction on a session this driver has already opened one on.
func (c *mysqlConn) checkIdle() error {
	if c.tx != nil {
		return fmt.Errorf("%w: %w", ErrTransaction, ErrTransactionOpen)
	}

	return nil
}

// end commits when work returned without error, and otherwise discards everything it did.
func (c *mysqlConn) end(tx *sql.Tx, returned bool, cause error) error {
	c.tx = nil

	if returned && cause == nil {
		return c.commit(tx)
	}

	return errors.Join(cause, c.rollback(tx))
}

// commit makes the transaction permanent, treating one the server discarded as the failure it is.
func (c *mysqlConn) commit(tx *sql.Tx) error {
	err := tx.Commit()
	if err == nil {
		return nil
	}

	if errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("%w: %w: %w", ErrTransaction, ErrCommitDiscarded, err)
	}

	return fmt.Errorf("%w: committing it: %w", ErrTransaction, err)
}

// rollback discards the transaction, and one the server already discarded is not a second failure.
func (c *mysqlConn) rollback(tx *sql.Tx) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("%w: rolling it back: %w", ErrTransaction, err)
	}

	return nil
}
