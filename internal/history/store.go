package history

import "context"

// Reader is the read half of a store, and is all reconciliation is ever given.
type Reader interface {
	// List returns every recorded run, in the order the runs happened.
	List(ctx context.Context) ([]Record, error)
}

// Store is one connection's view of the migration history, bound by the driver that opened it.
type Store interface {
	Reader

	// Ensure creates the history table if this database does not have it yet.
	Ensure(ctx context.Context) error
	// Record appends a run that completed, and is the only method that writes Applied.
	Record(ctx context.Context, rec Record) error
	// Latest returns the most recent run, and whether there has been one at all.
	Latest(ctx context.Context) (Record, bool, error)
	// MarkFailed appends a run that failed without changing the schema.
	MarkFailed(ctx context.Context, rec Record, cause error) error
	// MarkDirty appends a run that failed partway, naming the last statement that succeeded.
	MarkDirty(ctx context.Context, rec Record, lastStatement int32, cause error) error
	// MarkResolved appends an operator's assertion that a migration left between states is settled.
	MarkResolved(ctx context.Context, rec Record, reason string) error
}
