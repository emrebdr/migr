package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// Name is the name this driver is registered under, and the name every refusal it gives states.
const Name = "postgres"

// strategy is the one migration lock this driver implements, and the only one lock.strategy may ask for.
const strategy = "advisory"

// New builds the PostgreSQL driver from the settings resolved out of migr.yaml.
func New(opts driver.Options) (driver.Driver, error) {
	schema, table := opts.Namespace()

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
	if err := checkWaits(lockWait, statementLockWait); err != nil {
		return nil, err
	}

	if len(opts.Replicas) > 0 {
		return nil, ErrReplicas
	}

	return &pgDriver{
		schema:            opts.Schema,
		table:             table,
		seeds:             opts.Seeding(),
		key:               namespaceKey(schema, table),
		lockWait:          lockWait,
		statementLockWait: statementLockWait,
	}, nil
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
	if err := checkLockWait("lock.timeout", lockWait); err != nil {
		return err
	}

	return checkLockWait("safety.lock_timeout", statementLockWait)
}

// pgDriver is the PostgreSQL driver: what it is called, what it compiles, how to reach a server.
type pgDriver struct {
	schema string
	// table is where migrations are recorded, resolved by Options.Namespace rather than assumed.
	table string
	// seeds is where seeds are recorded, a second table that keys no lock of its own.
	seeds string
	key   lockKey
	// lockWait bounds an acquire server-side, and is migr.yaml's lock.timeout.
	lockWait time.Duration
	// statementLockWait bounds each statement, and is migr.yaml's safety.lock_timeout.
	statementLockWait time.Duration
}

// Name returns the driver's registered name.
func (d *pgDriver) Name() string {
	return Name
}

// Compile turns an IR operation into the PostgreSQL statements that perform it.
func (d *pgDriver) Compile(op ir.Operation) ([]driver.Statement, error) {
	return compile(op)
}

// Open connects to a server and returns a session bound to what that server can do.
func (d *pgDriver) Open(ctx context.Context, dsn string) (driver.Conn, error) {
	cfg, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}

	pinLockTimeout(cfg, d.statementLockWait)
	pinApplicationName(cfg)
	pinKeepalives(cfg)
	pinCancellation(cfg)

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnect, err)
	}

	session, err := d.establish(ctx, conn)
	if err != nil {
		return nil, errors.Join(err, conn.Close(context.WithoutCancel(ctx)))
	}

	return session, nil
}

// establish turns an open connection into a session, or reports why it cannot be one.
func (d *pgDriver) establish(ctx context.Context, conn *pgx.Conn) (*pgConn, error) {
	if err := applyStringGrammar(ctx, conn); err != nil {
		return nil, err
	}

	if err := applySearchPath(ctx, conn, d.schema); err != nil {
		return nil, err
	}

	facts, err := gatherFacts(ctx, conn)
	if err != nil {
		return nil, err
	}

	caps, err := capability.NewSet(facts.facts(), facts.rules())
	if err != nil {
		return nil, fmt.Errorf("%w: declaring capabilities: %w", ErrConnect, err)
	}

	session := &pgConn{
		conn:     conn,
		version:  facts.version,
		caps:     caps,
		key:      d.key,
		lockWait: d.lockWait,
	}
	session.history = pgHistory{session: session, table: d.table}
	session.seeds = pgHistory{session: session, table: d.seeds}

	return session, nil
}

// compile-time proof that this driver satisfies the contract every driver is held to.
var _ driver.Driver = (*pgDriver)(nil)
