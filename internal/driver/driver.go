package driver

import (
	"context"
	"fmt"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
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

// Query is a statement a migration wrote itself, which the compiler never saw.
type Query struct {
	// SQL is the statement text, in the target engine's own dialect.
	SQL string
	// Args are the bound parameters, positional and textual, in the engine's placeholder style.
	Args []string
}

// Rows is what a read returned: every value text, and absent where the column was null.
type Rows struct {
	// Columns names each column, in the order the statement selected them.
	Columns []string
	// Values holds one slice per row, each as wide as Columns.
	Values [][]*string
}

// Validate reports a result whose rows are not the shape its own columns describe.
func (r Rows) Validate() error {
	for i, row := range r.Values {
		if len(row) != len(r.Columns) {
			return fmt.Errorf("row %d is %d values wide and the result has %d columns",
				i, len(row), len(r.Columns))
		}
	}

	return nil
}

// Compiler turns neutral operations into engine statements, without a database.
type Compiler interface {
	// Compile returns the statements one operation becomes, or refuses with a reason.
	Compile(op ir.Operation) ([]Statement, error)
}

// Expander is a session that produces statements for an operation its Compiler cannot.
type Expander interface {
	// Expand returns the statements one operation becomes on this database, or refuses with a reason.
	Expand(ctx context.Context, op ir.Operation) ([]Statement, error)
}

// Assessor is a session that confirms, from the table itself, the condition a verdict was lowered on.
type Assessor interface {
	// Assess returns the verdict at its cheaper cost where the table meets its condition, else unchanged.
	Assess(ctx context.Context, op ir.Operation, v capability.Verdict) (capability.Verdict, error)
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

// Work is what runs inside a transaction: the statements of one group, and nothing that ends it.
type Work func(ctx context.Context) error

// DefaultHistoryTable is where migrations are recorded when migr.yaml names no other table.
const DefaultHistoryTable = "migr_migrations"

// DefaultSeedTable is where seeds are recorded when migr.yaml names no other table.
const DefaultSeedTable = "migr_seeds"

// DefaultLockWait is how long migr waits for the migration lock when migr.yaml names no timeout.
const DefaultLockWait = 30 * time.Second

// DefaultStatementLockWait bounds one statement's wait for a lock when migr.yaml names none.
const DefaultStatementLockWait = 3 * time.Second

// Conn is one pinned database session. Everything needing a database lives here.
type Conn interface {
	// Exec runs one compiled statement.
	Exec(ctx context.Context, stmt Statement) error
	// Query runs a migration's own read, which the server enforces read-only, and returns text.
	Query(ctx context.Context, q Query) (Rows, error)
	// Introspect reads this session's own namespace as a snapshot, less migr's own history table.
	Introspect(ctx context.Context) (*schema.Snapshot, error)
	// Perform runs a migration's own statement and reports how many rows it changed.
	Perform(ctx context.Context, q Query) (int64, error)
	// Lock acquires the migration lock, returning the release that must always run.
	Lock(ctx context.Context) (Unlock, error)
	// Transact runs work inside one transaction, committing it only if work returns nil.
	Transact(ctx context.Context, work Work) error
	// Capabilities reports what this server can do, resolved against its detected version.
	Capabilities() capability.Set
	// History returns this session's view of the migration history, already bound to it.
	History() history.Store
	// Seeds returns this session's view of the seed history, a second table under the same lock.
	Seeds() history.Store
	// ServerVersion returns the connected server's version, as the driver detected it at Open.
	ServerVersion() string
	// Close releases the session and anything it holds.
	Close(ctx context.Context) error
}
