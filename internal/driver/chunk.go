package driver

import (
	"context"
	"time"

	"github.com/emrebdr/migr/internal/ir"
)

// DefaultChunkRows is how many rows one chunk of a data operation covers when migr.yaml names none.
const DefaultChunkRows = 1000

// DefaultChunkPause is how long a chunked data operation waits between chunks when migr.yaml names none.
const DefaultChunkPause = 100 * time.Millisecond

// Slice is one keyed chunk of a data operation: the rows after a cursor, at most so many of them.
type Slice struct {
	// Op is the data operation run a chunk at a time.
	Op ir.Operation
	// Key is the primary key of the table the chunks page through, in key order.
	Key []string
	// After is the key of the last row the previous chunk covered, and empty for the first chunk.
	After []string
	// Rows is the most rows this chunk covers.
	Rows int
}

// Sliced is what one chunk did, and where the next one starts.
type Sliced struct {
	// Written is how many rows the chunk's write changed or inserted.
	Written int64
	// Last is the key of the last row the chunk covered, which is the next chunk's After.
	Last []string
	// Done reports that no row lay beyond what this chunk covered when it ran.
	Done bool
}

// ChunkCompiler renders the statements one chunk of a data operation is, for a report, with no database.
type ChunkCompiler interface {
	// CompileChunk returns a chunk's read of its upper bound and its write, keyed by the given columns.
	CompileChunk(op ir.Operation, key []string) ([]Statement, error)
}

// Chunker is a session that runs a data operation one keyed chunk at a time.
type Chunker interface {
	// Chunk runs one slice inside the session's open transaction, and reports where it ended.
	Chunk(ctx context.Context, s Slice) (Sliced, error)
}
