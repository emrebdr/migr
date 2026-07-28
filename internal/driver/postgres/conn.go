package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
)

// pgConn is one pinned PostgreSQL session, never a pool: an advisory lock belongs to a backend.
type pgConn struct {
	conn     *pgx.Conn
	version  serverVersion
	caps     capability.Set
	key      lockKey
	lockWait string
	// locked is session state, not shared state: a Conn is used by one goroutine, as pgx is.
	locked bool
	// tx is the transaction Transact opened, and is nil whenever this session is not inside one.
	tx pgx.Tx
}

// Exec runs one compiled statement, inside this session's transaction when it has one open.
func (c *pgConn) Exec(ctx context.Context, stmt driver.Statement) error {
	if err := c.checkStatementFits(stmt); err != nil {
		return err
	}

	if _, err := c.runner().Exec(ctx, stmt.SQL); err != nil {
		return fmt.Errorf("executing a statement on %s: %w", Name, err)
	}

	return nil
}

// checkStatementFits refuses a statement declaring it cannot run where this session would run it.
func (c *pgConn) checkStatementFits(stmt driver.Statement) error {
	if stmt.NoTransaction && c.tx != nil {
		return fmt.Errorf("%w: %w, so the caller must split the migration around it",
			ErrTransaction, ErrStatementNeedsNoTransaction)
	}

	return nil
}

// Capabilities reports what this server can do, resolved against the version detected at Open.
func (c *pgConn) Capabilities() capability.Set {
	return c.caps
}

// History returns this session's view of the migration history.
func (c *pgConn) History() history.Store {
	return pendingHistory{}
}

// ServerVersion returns the connected server's release, derived from the version detected at Open.
func (c *pgConn) ServerVersion() string {
	return c.version.String()
}

// Close ends the session, and does not inherit a context whose cancellation caused the close.
func (c *pgConn) Close(ctx context.Context) error {
	if err := c.conn.Close(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("closing the %s session: %w", Name, err)
	}

	return nil
}

// compile-time proof that this session satisfies the contract every driver's session is held to.
var _ driver.Conn = (*pgConn)(nil)
