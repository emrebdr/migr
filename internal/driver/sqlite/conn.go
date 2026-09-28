package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
)

// sqliteConn is one pinned SQLite connection, never a pool: an in-memory database is per connection.
type sqliteConn struct {
	// db is the pool this session drew its one connection from, and closes at the end.
	db *sql.DB
	// conn is that connection, held for the session's life so nothing is recycled underneath it.
	conn *sql.Conn
	// version is what sqlite_version() reported, which is a fact about this build.
	version serverVersion
	// caps is what this library can do, declared against that version.
	caps capability.Set
	// tx is the transaction Transact opened, and is nil whenever this session is not inside one.
	tx *sql.Tx
}

// Exec runs one compiled statement, inside this session's transaction when it has one open.
func (c *sqliteConn) Exec(ctx context.Context, stmt driver.Statement) error {
	if err := c.checkStatementFits(stmt); err != nil {
		return err
	}

	if _, err := c.runner().ExecContext(ctx, stmt.SQL); err != nil {
		return fmt.Errorf("executing a statement on %s: %w", Name, err)
	}

	return nil
}

// checkStatementFits refuses a statement declaring it cannot run where this session would run it.
func (c *sqliteConn) checkStatementFits(stmt driver.Statement) error {
	if stmt.NoTransaction && c.tx != nil {
		return fmt.Errorf("%w: %w, so the caller must split the migration around it",
			ErrTransaction, ErrStatementNeedsNoTransaction)
	}

	return nil
}

// Capabilities reports what this library can do, resolved against the version detected at Open.
func (c *sqliteConn) Capabilities() capability.Set {
	return c.caps
}

// History returns this session's view of the migration history, which M8-T6 implements.
func (c *sqliteConn) History() history.Store {
	return pendingHistory{}
}

// ServerVersion returns the SQLite release this binary links, as it reported itself at Open.
func (c *sqliteConn) ServerVersion() string {
	return c.version.String()
}

// Close gives the connection and the pool behind it back, and closing twice is not a failure.
func (c *sqliteConn) Close(context.Context) error {
	return errors.Join(closeConn(c.conn), closeDB(c.db))
}

// compile-time proof that this session satisfies the contract every driver's session is held to.
var _ driver.Conn = (*sqliteConn)(nil)
