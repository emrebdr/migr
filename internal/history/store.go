package history

import "context"

// Store is one connection's view of the migration history, bound by the driver that opened it.
type Store interface {
	// Ensure creates the history table if this database does not have it yet.
	Ensure(ctx context.Context) error
	// Record appends a run that completed, and is the only method that writes Applied.
	Record(ctx context.Context, rec Record) error
	// List returns every recorded run, in the order the runs happened.
	List(ctx context.Context) ([]Record, error)
	// Latest returns the most recent run, and whether there has been one at all.
	Latest(ctx context.Context) (Record, bool, error)
	// MarkFailed appends a run that failed without changing the schema.
	MarkFailed(ctx context.Context, rec Record, cause error) error
	// MarkDirty appends a run that failed partway, naming the last statement that succeeded.
	MarkDirty(ctx context.Context, rec Record, lastStatement int32, cause error) error
}
