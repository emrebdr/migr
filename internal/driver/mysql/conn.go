package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
)

// mysqlConn is one pinned MySQL session, never a pool: the migration lock belongs to a connection.
type mysqlConn struct {
	// db is the pool this session drew its one connection from, and closes at the end.
	db *sql.DB
	// conn is that connection, held for the session's life so nothing is recycled underneath it.
	conn *sql.Conn
	// version is what the server reported at Open, variant and release together.
	version serverVersion
	// caps is what this server can do, declared against that version.
	caps capability.Set
	// tx is the transaction Transact opened, and is nil whenever this session is not inside one.
	tx *sql.Tx
	// database is the schema every unqualified name resolves against, read from the connection string.
	database string
	// table is where runs are recorded, resolved by Options.Namespace rather than assumed.
	table string
	// stored is the history table's name as the server's catalog holds it, per lower_case_table_names.
	stored string
	// lockWait bounds the wait for the migration lock, and is migr.yaml's lock.timeout.
	lockWait time.Duration
}

// Exec runs one compiled statement, inside this session's transaction when it has one open.
func (c *mysqlConn) Exec(ctx context.Context, stmt driver.Statement) error {
	if err := c.checkStatementFits(stmt); err != nil {
		return err
	}

	if _, err := c.runner().ExecContext(ctx, stmt.SQL); err != nil {
		return fmt.Errorf("executing a statement on %s: %w", Name, err)
	}

	return nil
}

// checkStatementFits refuses a statement declaring it cannot run where this session would run it.
func (c *mysqlConn) checkStatementFits(stmt driver.Statement) error {
	if stmt.NoTransaction && c.tx != nil {
		return fmt.Errorf("%w: %w, so the caller must split the migration around it",
			ErrTransaction, ErrStatementNeedsNoTransaction)
	}

	return nil
}

// Capabilities reports what this server can do, resolved against the version detected at Open.
func (c *mysqlConn) Capabilities() capability.Set {
	return c.caps
}

// History returns this session's view of the migration history, bound to this session.
func (c *mysqlConn) History() history.Store {
	return mysqlHistory{session: c, table: c.table, stored: c.stored}
}

// ServerVersion returns the variant and release the server reported at Open.
func (c *mysqlConn) ServerVersion() string {
	return c.version.String()
}

// Close gives the connection and the pool behind it back, and closing twice is not a failure.
func (c *mysqlConn) Close(context.Context) error {
	return errors.Join(closeConn(c.conn), closeDB(c.db))
}

// compile-time proof that this session satisfies the contract every driver's session is held to.
var _ driver.Conn = (*mysqlConn)(nil)
