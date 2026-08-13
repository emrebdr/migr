package history

import (
	"errors"
	"fmt"
)

// ErrIncomplete marks a record that does not carry what a run must record, for errors.Is.
var ErrIncomplete = errors.New("history record is incomplete")

// ErrCorrupt marks a recorded row this build cannot read back, for errors.Is.
var ErrCorrupt = errors.New("history row is corrupt")

// ErrNoTable marks a database with no migration history table, which is not one with no runs.
var ErrNoTable = errors.New("this database has no migration history table")

// IncompleteError reports a record that cannot be written, and which field is why.
type IncompleteError struct {
	// Version is the migration the record describes, where it named one.
	Version string
	// Field is the record field at fault, spelled as its history column.
	Field string
	// Detail states what that field must carry, in terms a driver author can act on.
	Detail string
}

// Error names the migration, the field at fault, and what that field must carry.
func (e *IncompleteError) Error() string {
	return fmt.Sprintf("history record for %s: %s %s", describeVersion(e.Version), e.Field, e.Detail)
}

// Unwrap reports the sentinel so errors.Is(err, ErrIncomplete) succeeds.
func (e *IncompleteError) Unwrap() error {
	return ErrIncomplete
}

// describeVersion names the migration a record describes, saying so plainly when it named none.
func describeVersion(version string) string {
	if version == "" {
		return "an unnamed migration"
	}

	return fmt.Sprintf("migration %q", version)
}

// CorruptError reports a recorded row whose stored value this build cannot read back.
type CorruptError struct {
	// Table is the history table the row was read from.
	Table string
	// Version is the migration the row records a run of, which is what names it to an operator.
	Version string
	// Batch is the run the row belongs to.
	Batch int64
	// Ordinal is the row's position within that run.
	Ordinal int32
	// Field is the column whose value could not be read, spelled as the column is.
	Field string
	// Err is why it could not be read, such as a checksum that will not parse.
	Err error
}

// Error names the migration, the row, the column, and why its recorded value could not be read.
func (e *CorruptError) Error() string {
	return fmt.Sprintf("%s row for %s (batch %d, ordinal %d): %s is unreadable: %v",
		describeTable(e.Table), describeVersion(e.Version), e.Batch, e.Ordinal, e.Field, e.Err)
}

// Unwrap reports the sentinel and the underlying cause, so errors.Is finds either.
func (e *CorruptError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrCorrupt}
	}

	return []error{ErrCorrupt, e.Err}
}

// describeTable names the history table, saying so plainly when the store named none.
func describeTable(table string) string {
	if table == "" {
		return "an unnamed history table"
	}

	return fmt.Sprintf("history table %q", table)
}
