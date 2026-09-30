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

// The two key refusals a pure Compile cannot prevent, because they are about a column's type.
//
// Both are reported identically by MySQL and MariaDB, though the first is one MariaDB only applies to
// a primary key -- it indexes a longtext without a prefix where MySQL refuses. Measured on both.
const (
	// errBlobKeyWithoutLength is ER_BLOB_KEY_WITHOUT_LENGTH, a text or blob column put in a key.
	errBlobKeyWithoutLength = 1170
	// errKeyTooLong is ER_TOO_LONG_KEY, a key wider than InnoDB indexes.
	errKeyTooLong = 1071
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
	// folding is that setting itself, so any table name a catalog lookup needs can be folded too.
	folding int
	// lockWait bounds the wait for the migration lock, and is migr.yaml's lock.timeout.
	lockWait time.Duration
}

// Exec runs one compiled statement, inside this session's transaction when it has one open.
func (c *mysqlConn) Exec(ctx context.Context, stmt driver.Statement) error {
	if err := c.checkStatementFits(stmt); err != nil {
		return err
	}

	if _, err := c.runner().ExecContext(ctx, stmt.SQL); err != nil {
		return fmt.Errorf("executing a statement on %s: %w", Name, explainExec(err))
	}

	return nil
}

// explainExec states the rule behind the two refusals a pure Compile could not have prevented.
func explainExec(err error) error {
	switch {
	case hasNumber(err, errBlobKeyWithoutLength):
		return &IndexKeyError{Number: errBlobKeyWithoutLength, Err: err, Detail: "MySQL will not put " +
			"a text or blob column in a key without a prefix length, and no v1 operation carries one: " +
			"add_index, add_unique and add_primary_key name columns and not types, so this driver " +
			"cannot refuse it before the statement runs. MariaDB indexes such a column without a " +
			"prefix, so the same migration succeeds there. Key a string(n) column instead"}
	case hasNumber(err, errKeyTooLong):
		return &IndexKeyError{Number: errKeyTooLong, Err: err, Detail: fmt.Sprintf("InnoDB indexes a "+
			"key of at most %d bytes and every character costs %d under %s, so a key spans at most %d "+
			"characters. A v1 operation names the key's columns and not their widths, so this driver "+
			"cannot add up the bytes before the statement runs", maxKeyBytes, bytesPerCharacter,
			characterSet, maxKeyBytes/bytesPerCharacter)}
	default:
		return err
	}
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

// compile-time proof that this session satisfies the contract every driver's session is held to,
// and that it can produce the statements its declaration promises for what it emulates.
var (
	_ driver.Conn     = (*mysqlConn)(nil)
	_ driver.Expander = (*mysqlConn)(nil)
)
