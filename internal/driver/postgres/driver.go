package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// Name is the name this driver is registered under, and the name every refusal it gives states.
const Name = "postgres"

// New builds the PostgreSQL driver from the settings resolved out of migr.yaml.
func New(opts driver.Options) (driver.Driver, error) {
	schema, table := opts.Namespace()

	return &pgDriver{
		schema:   opts.Schema,
		key:      namespaceKey(schema, table),
		lockWait: defaultLockWait,
	}, nil
}

// pgDriver is the PostgreSQL driver: what it is called, what it compiles, how to reach a server.
type pgDriver struct {
	schema string
	key    lockKey
	// lockWait bounds an acquire server-side. M4-T4 sets it from migr.yaml's lock.timeout.
	lockWait string
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

	return &pgConn{
		conn:     conn,
		version:  facts.version,
		caps:     caps,
		key:      d.key,
		lockWait: d.lockWait,
	}, nil
}

// compile-time proof that this driver satisfies the contract every driver is held to.
var _ driver.Driver = (*pgDriver)(nil)
