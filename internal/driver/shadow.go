package driver

import (
	"context"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/ir"
)

// Shadow is one table changed through a copy of itself: the table, its copy, and the name it retires under.
type Shadow struct {
	// Table is the table the migration changes, which keeps its name and takes the shadow's place.
	Table string
	// Shadow is the copy the changes are made to while the table goes on serving.
	Shadow string
	// Retired is the name the original takes at cutover, kept until a separate step drops it.
	Retired string
	// Key is the table's primary key, which the copy pages through and a change is matched by.
	Key []string
	// Ops are the migration's operations, each naming Table, which the shadow is created already carrying.
	Ops ir.Operations
}

// CatchUp applies every change the log holds up to now, which a cutover calls under its own brief lock.
type CatchUp func(ctx context.Context) error

// Shadower is a session that changes a table through a shadow copy kept current from the replication log.
type Shadower interface {
	// Shadows reports why an operation cannot be performed through a shadow table, and nil where it can.
	Shadows(op ir.Operation) error
	// Admits reports why this table cannot be changed through a shadow here, and nil where it can.
	Admits(ctx context.Context, s Shadow) error
	// CreateShadow returns the statements creating the shadow, empty and already carrying the operations.
	CreateShadow(ctx context.Context, s Shadow) ([]Statement, error)
	// CopyChunk copies the rows after a key into the shadow inside the open transaction, keeping any already there.
	CopyChunk(ctx context.Context, s Shadow, after []string, rows int) (Sliced, error)
	// ApplyChanges writes captured changes to the shadow inside the open transaction, idempotently.
	ApplyChanges(ctx context.Context, s Shadow, changes []capture.Change) error
	// Cutover takes the brief lock, calls catchUp under it, and swaps the shadow in for the table.
	Cutover(ctx context.Context, s Shadow, catchUp CatchUp) error
	// DropShadow returns the statements dropping the shadow, which are nothing where it does not exist.
	DropShadow(s Shadow) ([]Statement, error)
	// DropRetired returns the statements dropping the retired original, which are nothing where it is gone.
	DropRetired(s Shadow) ([]Statement, error)
}
