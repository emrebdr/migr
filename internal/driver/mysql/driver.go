package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// Name is the name this driver is registered under, and the name every refusal it gives states.
const Name = "mysql"

// strategy is the one migration lock this driver will implement, and lock.strategy's advisory half.
const strategy = "advisory"

// New builds the MySQL driver from the settings resolved out of migr.yaml.
func New(opts driver.Options) (driver.Driver, error) {
	_, table := opts.Namespace()
	if err := checkHistoryTable(table); err != nil {
		return nil, err
	}

	if err := checkHistoryTable(opts.Seeding()); err != nil {
		return nil, err
	}

	if err := checkStrategy(opts.LockStrategy); err != nil {
		return nil, err
	}

	lockWait, statementLockWait := opts.Waits()
	if err := checkStatementWait(statementLockWait); err != nil {
		return nil, err
	}

	return &mysqlDriver{schema: opts.Schema, table: table, seeds: opts.Seeding(),
		lockWait: lockWait, statementLockWait: statementLockWait}, nil
}

// checkHistoryTable refuses a history table name this driver cannot compile its own schema for.
func checkHistoryTable(table string) error {
	if strings.TrimSpace(table) == "" {
		return &HistoryError{Table: table, Detail: "history.table is required"}
	}

	for _, op := range append(append(history.Schema(table), history.ProgressSchema(table)...),
		history.ReleaseSchema(table)...) {
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
	return fmt.Sprintf("history.table names %q and the history schema cannot be compiled for it: "+
		"%v. The schema "+
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

// checkStatementWait refuses a per-statement lock wait MySQL would silently turn into another number.
func checkStatementWait(wait time.Duration) error {
	if wait < time.Second {
		return &WaitError{Key: "safety.lock_timeout", Requested: wait.String(), Detail: fmt.Sprintf(
			"lock_wait_timeout is a whole number of seconds: MySQL refuses a fraction outright and "+
				"turns 0 into 1 while reporting success, so a bound under 1s would be a bound nobody "+
				"asked for. The shortest this engine can be given is %s", time.Second)}
	}

	if seconds := int64(wait / time.Second); seconds > maxWaitSeconds {
		return &WaitError{Key: "safety.lock_timeout", Requested: wait.String(), Detail: fmt.Sprintf(
			"lock_wait_timeout holds at most %d seconds and clamps anything longer while reporting "+
				"success, so a bound of %d seconds would silently become %d",
			maxWaitSeconds, seconds, maxWaitSeconds)}
	}

	return nil
}

// mysqlDriver is the MySQL driver: what it is called, what it compiles, how to reach a server.
type mysqlDriver struct {
	// schema is what migr.yaml named, which for this engine must be the connection string's database.
	schema string
	// table is where migrations are recorded, resolved by Options.Namespace rather than assumed.
	table string
	// seeds is where seeds are recorded, a second table that names no lock of its own.
	seeds string
	// lockWait bounds the wait for the migration lock, and is migr.yaml's lock.timeout.
	lockWait time.Duration
	// statementLockWait bounds each statement, and is migr.yaml's safety.lock_timeout.
	statementLockWait time.Duration
}

// Name returns the driver's registered name.
func (d *mysqlDriver) Name() string {
	return Name
}

// Compile turns an IR operation into the MySQL statements that perform it.
func (d *mysqlDriver) Compile(op ir.Operation) ([]driver.Statement, error) {
	return compile(op)
}

// Open connects to a server and returns a session bound to what that server can do.
func (d *mysqlDriver) Open(ctx context.Context, dsn string) (driver.Conn, error) {
	source, err := parseDSN(dsn, d.schema)
	if err != nil {
		return nil, err
	}

	connector, err := source.connector()
	if err != nil {
		return nil, err
	}

	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)

	session, err := d.establish(ctx, db, source.database)
	if err != nil {
		return nil, errors.Join(err, closeDB(db))
	}

	session.killer = newKiller(connector)

	return session, nil
}

// establish pins one connection and turns it into a session, or reports why it cannot be one.
func (d *mysqlDriver) establish(ctx context.Context, db *sql.DB, database string) (*mysqlConn,
	error,
) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, wrapConnect("pinning a connection", err)
	}

	session := &mysqlConn{db: db, conn: conn, database: database, table: d.table,
		stored: d.table, seeds: d.seeds, seedStored: d.seeds, lockWait: d.lockWait}

	if err := d.prepare(ctx, session); err != nil {
		return nil, errors.Join(err, closeConn(conn))
	}

	return session, nil
}

// prepare puts a pinned connection into the state this driver runs against, or says why it cannot.
func (d *mysqlDriver) prepare(ctx context.Context, session *mysqlConn) error {
	if err := applyVariables(ctx, session, d.statementLockWait); err != nil {
		return err
	}

	id, err := readConnectionID(ctx, session)
	if err != nil {
		return err
	}

	session.id = id

	version, err := detectVersion(ctx, session)
	if err != nil {
		return err
	}

	folding, err := readCaseFolding(ctx, session)
	if err != nil {
		return err
	}

	logBin, err := readBinaryLogging(ctx, session)
	if err != nil {
		return err
	}

	declaration := serverFacts{version: version, logBin: logBin}

	caps, err := capability.NewSet(declaration.facts(), declaration.rules())
	if err != nil {
		return wrapConnect("declaring capabilities", err)
	}

	session.version, session.caps = version, caps
	session.folding, session.stored = folding, storedName(d.table, folding)
	session.seedStored = storedName(d.seeds, folding)
	session.lock = namespaceLock(session.database, d.table)

	return nil
}

// wrapConnect reports a failure that stopped a session from being established, naming what it was doing.
func wrapConnect(doing string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrConnect, doing, err)
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
var _ driver.Driver = (*mysqlDriver)(nil)
