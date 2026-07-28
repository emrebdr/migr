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
	return &pgDriver{schema: opts.Schema}, nil
}

// pgDriver is the PostgreSQL driver: what it is called, what it compiles, how to reach a server.
type pgDriver struct {
	schema string
}

// Name returns the driver's registered name.
func (d *pgDriver) Name() string {
	return Name
}

// Compile refuses every operation until the compiler is written.
func (d *pgDriver) Compile(op ir.Operation) ([]driver.Statement, error) {
	if op == nil {
		return nil, fmt.Errorf("%s was asked to compile no operation at all", Name)
	}

	return nil, driver.Unsupported(Name, op.Kind(), compilePending)
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
	if err := applySearchPath(ctx, conn, d.schema); err != nil {
		return nil, err
	}

	version, err := detectVersion(ctx, conn)
	if err != nil {
		return nil, err
	}

	caps, err := capability.NewSet(
		capability.Facts{Driver: Name, Server: version.String()}, pendingRules())
	if err != nil {
		return nil, fmt.Errorf("%w: declaring capabilities: %w", ErrConnect, err)
	}

	return &pgConn{conn: conn, version: version, caps: caps}, nil
}

// compile-time proof that this driver satisfies the contract every driver is held to.
var _ driver.Driver = (*pgDriver)(nil)
