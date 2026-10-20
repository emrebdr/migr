package capture

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrUnsupported marks a driver or server that cannot read its replication log for a shadow table.
var ErrUnsupported = errors.New("log-based change capture is not available")

// Position is a place in a server's replication log, in the driver's own spelling, recorded as text.
type Position string

// Request asks a driver for a stream of the changes made to some tables, from a position or from now.
type Request struct {
	// Name is the server-side resource the stream reads through, derived so an abort can find it unrecorded.
	Name string
	// Schema is the namespace the tables live in, where the engine has one.
	Schema string
	// Tables are the tables whose changes the stream carries.
	Tables []string
	// From is where to read from, and empty starts at the log's current end.
	From Position
}

// Validate reports a request that names no resource or no table.
func (r Request) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("a capture request names no resource to read through")
	}

	if len(r.Tables) == 0 {
		return fmt.Errorf("the capture request %q names no table", r.Name)
	}

	return nil
}

// Kind is what one change did to a row.
type Kind string

// The three things a row-format log records a row going through.
const (
	Insert Kind = "insert"
	Update Kind = "update"
	Delete Kind = "delete"
)

// Valid reports whether k is one of the three kinds a log records.
func (k Kind) Valid() bool {
	return k == Insert || k == Update || k == Delete
}

// Change is one row as a write left it, or the key of a row a delete removed.
type Change struct {
	// Kind is what the write did.
	Kind Kind
	// Table is the table it was made to.
	Table string
	// Columns name Row's values, in order.
	Columns []string
	// Row is every column's value after an insert or update and the key's before a delete, nil for NULL.
	Row []*string
}

// Batch is the changes a stream read in one call, and where the log stands after them.
type Batch struct {
	// Changes are what was read, in the order the server committed them.
	Changes []Change
	// Through is the position just past the last change, where the next read starts.
	Through Position
	// Behind is how far the stream trails the log's end, as the server measures it.
	Behind time.Duration
}

// Stream reads one table's changes from a server's replication log, on a connection of its own.
type Stream interface {
	// Start returns the position the stream began reading at.
	Start() Position
	// Next returns the changes committed since the last call, and none only once it has read to where the log ended as it began.
	Next(ctx context.Context) (Batch, error)
	// Close stops reading and keeps the server-side resource, so a later stream resumes from a position.
	Close(ctx context.Context) error
	// Drop stops reading and releases the server-side resource, and is nothing where it is already gone.
	Drop(ctx context.Context) error
}
