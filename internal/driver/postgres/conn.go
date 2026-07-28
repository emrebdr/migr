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
	conn    *pgx.Conn
	version serverVersion
	caps    capability.Set
}

// Exec runs one compiled statement against this session.
func (c *pgConn) Exec(ctx context.Context, stmt driver.Statement) error {
	if _, err := c.conn.Exec(ctx, stmt.SQL); err != nil {
		return fmt.Errorf("executing a statement on %s: %w", Name, err)
	}

	return nil
}

// Lock refuses to take the migration lock until advisory locking is written.
func (c *pgConn) Lock(context.Context) (driver.Unlock, error) {
	return nil, errPendingLock
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
