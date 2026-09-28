package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	// registers the SQLite driver this package opens through database/sql.
	_ "modernc.org/sqlite"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// Name is the name this driver is registered under, and the name every refusal it gives states.
const Name = "sqlite"

// library is the name modernc.org/sqlite registers itself with database/sql under.
const library = "sqlite"

// strategy is the one migration lock this driver will implement, and lock.strategy's file half.
const strategy = "file"

// maxBusyTimeout is the longest wait busy_timeout holds, being a C int of milliseconds.
const maxBusyTimeout = time.Duration(math.MaxInt32) * time.Millisecond

// New builds the SQLite driver from the settings resolved out of migr.yaml.
func New(opts driver.Options) (driver.Driver, error) {
	if err := checkSchema(opts.Schema); err != nil {
		return nil, err
	}

	_, table := opts.Namespace()
	if err := checkHistoryTable(table); err != nil {
		return nil, err
	}

	if err := checkStrategy(opts.LockStrategy); err != nil {
		return nil, err
	}

	lockWait, statementLockWait := opts.Waits()
	if err := checkWaits(lockWait, statementLockWait); err != nil {
		return nil, err
	}

	return &sqliteDriver{table: table, lockWait: lockWait,
		statementLockWait: statementLockWait}, nil
}

// checkSchema refuses a configured namespace, since SQLite has none to resolve one against.
func checkSchema(schema string) error {
	if strings.TrimSpace(schema) == "" {
		return nil
	}

	return &SchemaError{Schema: schema}
}

// checkHistoryTable refuses a history table name this driver cannot compile its own schema for.
func checkHistoryTable(table string) error {
	if strings.TrimSpace(table) == "" {
		return &HistoryError{Table: table, Detail: "a table name is required"}
	}

	for _, op := range history.Schema(table) {
		if err := op.Validate(); err != nil {
			return &HistoryError{Table: table, Detail: err.Error(), Err: err}
		}

		if _, err := compile(op); err != nil {
			return &HistoryError{Table: table, Detail: describeDerived(table, err), Err: err}
		}
	}

	return nil
}

// describeDerived names the history schema as what could not be compiled, rather than the name alone.
func describeDerived(table string, err error) string {
	return fmt.Sprintf("the history schema cannot be compiled for a table named %q: %v. The schema "+
		"is itself a set of operations (invariant #3), so compiling it is the whole check: there "+
		"is nothing about a name this driver refuses that the operations carrying it do not",
		table, err)
}

// checkStrategy refuses a lock strategy this engine has no implementation of, rather than ignoring it.
func checkStrategy(requested string) error {
	if requested == "" || requested == strategy {
		return nil
	}

	return &LockStrategyError{Requested: requested, Implemented: strategy}
}

// checkWaits refuses either configured bound before a connection exists, naming the key that set it.
func checkWaits(lockWait, statementLockWait time.Duration) error {
	if err := checkWait("lock.timeout", lockWait); err != nil {
		return err
	}

	return checkWait("safety.lock_timeout", statementLockWait)
}

// checkWait refuses one configured bound longer than SQLite can be asked to wait.
func checkWait(key string, wait time.Duration) error {
	if wait <= maxBusyTimeout {
		return nil
	}

	return &WaitError{Key: key, Requested: wait.String(), Limit: maxBusyTimeout.String()}
}

// sqliteDriver is the SQLite driver: what it is called, what it compiles, how to reach a database.
type sqliteDriver struct {
	// table is where migrations are recorded, resolved by Options.Namespace rather than assumed.
	table string
	// lockWait bounds the wait for the migration lock, and is migr.yaml's lock.timeout.
	lockWait time.Duration
	// statementLockWait bounds each statement, and is migr.yaml's safety.lock_timeout.
	statementLockWait time.Duration
}

// Name returns the driver's registered name.
func (d *sqliteDriver) Name() string {
	return Name
}

// Compile turns an IR operation into the SQLite statements that perform it.
func (d *sqliteDriver) Compile(op ir.Operation) ([]driver.Statement, error) {
	return compile(op)
}

// Open connects to a database and returns a session bound to what that library can do.
func (d *sqliteDriver) Open(ctx context.Context, dsn string) (driver.Conn, error) {
	source, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(library, source)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnect, err)
	}

	db.SetMaxOpenConns(1)

	session, err := d.establish(ctx, db)
	if err != nil {
		return nil, errors.Join(err, closeDB(db))
	}

	return session, nil
}

// establish pins one connection and turns it into a session, or reports why it cannot be one.
func (d *sqliteDriver) establish(ctx context.Context, db *sql.DB) (*sqliteConn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: pinning a connection: %w", ErrConnect, err)
	}

	session := &sqliteConn{db: db, conn: conn, lockWait: d.lockWait}

	if err := applyPragmas(ctx, session, d.statementLockWait); err != nil {
		return nil, errors.Join(err, closeConn(conn))
	}

	path, err := detectPath(ctx, session)
	if err != nil {
		return nil, errors.Join(err, closeConn(conn))
	}

	version, err := detectVersion(ctx, session)
	if err != nil {
		return nil, errors.Join(err, closeConn(conn))
	}

	caps, err := capability.NewSet(facts(version), rules(version))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%w: declaring capabilities: %w", ErrConnect, err),
			closeConn(conn))
	}

	session.path, session.version, session.caps = path, version, caps

	return session, nil
}

// closeDB gives the pool back, since Open leaves nothing behind for a session it could not build.
func closeDB(db *sql.DB) error {
	if err := db.Close(); err != nil {
		return fmt.Errorf("closing the %s pool: %w", Name, err)
	}

	return nil
}

// closeConn gives one pinned connection back, for a session that will not be returned to a caller.
func closeConn(conn *sql.Conn) error {
	if err := conn.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		return fmt.Errorf("closing the %s connection: %w", Name, err)
	}

	return nil
}

// compile-time proof that this driver satisfies the contract every driver is held to.
var _ driver.Driver = (*sqliteDriver)(nil)
