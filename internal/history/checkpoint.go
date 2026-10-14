package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
)

// progressSuffix is what a history table's name gains to name the table its checkpoints live in.
const progressSuffix = "_progress"

// Checkpoint is how far a chunked data operation got, committed in the transaction of the chunk it names.
type Checkpoint struct {
	// Version is the migration the operation belongs to.
	Version string
	// Direction is the direction that migration was running in.
	Direction ir.Direction
	// Step is the operation's zero-based position among the migration's operations.
	Step int32
	// Cursor is the key of the last row the last committed chunk covered, in key order.
	Cursor []string
	// Chunks is how many chunks of this operation have committed.
	Chunks int64
	// Written is how many rows those chunks changed or inserted.
	Written int64
}

// Validate reports a checkpoint that names no migration, no direction, or a step before the first.
func (c Checkpoint) Validate() error {
	if strings.TrimSpace(c.Version) == "" {
		return errors.New("a checkpoint names no migration version")
	}

	if !c.Direction.Valid() {
		return fmt.Errorf("a checkpoint of %s records the unknown direction %q", c.Version, c.Direction)
	}

	if c.Step < 0 || c.Chunks < 0 || c.Written < 0 {
		return fmt.Errorf("a checkpoint of %s records step %d after %d chunks and %d rows, which no "+
			"committed chunk leaves", c.Version, c.Step, c.Chunks, c.Written)
	}

	return nil
}

// Checkpointer is a store that remembers how far a chunked data operation got.
type Checkpointer interface {
	// SaveCheckpoint replaces the checkpoint of one migration and direction, inside the open transaction.
	SaveCheckpoint(ctx context.Context, cp Checkpoint) error
	// Checkpoints returns every checkpoint recorded, and none where the table does not exist yet.
	Checkpoints(ctx context.Context) ([]Checkpoint, error)
	// ClearCheckpoint removes the checkpoint of one migration and direction, where there is one.
	ClearCheckpoint(ctx context.Context, version string, dir ir.Direction) error
}

// ProgressTable names the table a history table's checkpoints live in.
func ProgressTable(table string) string {
	return table + progressSuffix
}

// ProgressSchema returns the operations that create the checkpoint table beside a history table.
func ProgressSchema(table string) ir.Operations {
	return ir.Operations{
		ir.CreateTable{
			Table:      ProgressTable(table),
			Columns:    progressColumns(),
			PrimaryKey: []string{"version", "direction"},
		},
	}
}

// progressColumns returns one column per Checkpoint field, in the order the checkpoint declares them.
func progressColumns() []ir.Column {
	return []ir.Column{
		{Name: "version", Type: typeOf("string(64)")},
		{Name: "direction", Type: typeOf("string(8)")},
		{Name: "step", Type: typeOf("int32")},
		{Name: "cursor", Type: typeOf("text")},
		{Name: "chunks", Type: typeOf("int64")},
		{Name: "written", Type: typeOf("int64")},
	}
}

// EncodeCursor renders a cursor as the text the checkpoint table stores it as.
func EncodeCursor(cursor []string) (string, error) {
	if cursor == nil {
		cursor = []string{}
	}

	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encoding a checkpoint cursor: %w", err)
	}

	return string(encoded), nil
}

// DecodeCursor reads a cursor back from the text the checkpoint table stores.
func DecodeCursor(text string) ([]string, error) {
	var cursor []string
	if err := json.Unmarshal([]byte(text), &cursor); err != nil {
		return nil, fmt.Errorf("decoding a checkpoint cursor %q: %w", text, err)
	}

	return cursor, nil
}

// Find returns the checkpoint of one migration and direction among several, and whether there is one.
func Find(cps []Checkpoint, version string, dir ir.Direction) (Checkpoint, bool) {
	for _, cp := range cps {
		if cp.Version == version && cp.Direction == dir {
			return cp, true
		}
	}

	return Checkpoint{}, false
}
