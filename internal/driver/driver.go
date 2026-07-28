package driver

import (
	"context"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// Statement is one compiled unit of work, ready to run against the engine that produced it.
type Statement struct {
	// SQL is the statement text, exactly as it will be sent.
	SQL string
	// NoTransaction declares that this statement cannot run inside a transaction.
	NoTransaction bool
}

// String returns the statement text, so a plan can print a statement directly.
func (s Statement) String() string {
	return s.SQL
}

// Compiler turns neutral operations into engine statements, without a database.
type Compiler interface {
	// Compile returns the statements one operation becomes, or refuses with a reason.
	Compile(op ir.Operation) ([]Statement, error)
}

// Driver is one database engine: what it is called, what it compiles to, how to reach it.
type Driver interface {
	Compiler

	// Name returns the driver's registered name, as it appears in migr.yaml.
	Name() string
	// Open connects to a database and returns a session that owns the connection.
	Open(ctx context.Context, dsn string) (Conn, error)
}

// Unlock releases a migration lock, and is safe to call more than once.
type Unlock func(ctx context.Context) error

// DefaultHistoryTable is where migrations are recorded when migr.yaml names no other table.
const DefaultHistoryTable = "migr_migrations"

// Conn is one pinned database session. Everything needing a database lives here.
type Conn interface {
	// Exec runs one compiled statement.
	Exec(ctx context.Context, stmt Statement) error
	// Lock acquires the migration lock, returning the release that must always run.
	Lock(ctx context.Context) (Unlock, error)
	// Capabilities reports what this server can do, resolved against its detected version.
	Capabilities() capability.Set
	// History returns this session's view of the migration history, already bound to it.
	History() history.Store
	// ServerVersion returns the connected server's version, as the driver detected it at Open.
	ServerVersion() string
	// Close releases the session and anything it holds.
	Close(ctx context.Context) error
}
