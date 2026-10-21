package history

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/ir"
)

// shadowSuffix is what a history table's name gains to name the table its shadow-table migrations live in.
const shadowSuffix = "_shadows"

// nameLimit is the longest table or resource name the shadow table stores, which is the width of its columns.
const nameLimit = 255

// Stage is where a shadow-table migration stands, each recorded in the transaction of the work it names.
type Stage string

// The stages a shadow-table migration passes through, in order. Retirement deletes the row.
const (
	// StageCopying is a shadow being filled from the table while its changes are captured.
	StageCopying Stage = "copying"
	// StageSyncing is a filled shadow held current from the log until a cutover is confirmed.
	StageSyncing Stage = "syncing"
	// StageCutOver is a shadow swapped in for the table, whose original is retained under a derived name.
	StageCutOver Stage = "cut_over"
)

// Valid reports whether s is one of the three stages a row records.
func (s Stage) Valid() bool {
	return s == StageCopying || s == StageSyncing || s == StageCutOver
}

// ShadowState is how far one shadow-table migration got, and what it is called on the server.
type ShadowState struct {
	// Version is the migration it belongs to.
	Version string
	// Table is the table the migration changes.
	Table string
	// Shadow is the copy being filled and kept current.
	Shadow string
	// Retired is the name the original takes at cutover.
	Retired string
	// Capture is the server-side resource the change stream reads through.
	Capture string
	// Stage is where the migration stands.
	Stage Stage
	// Cursor is the key of the last row the copy covered, in key order, and empty before the first.
	Cursor []string
	// Position is the log position every change before it has been applied to the shadow from.
	Position string
	// Copied is how many rows the copy wrote.
	Copied int64
	// Applied is how many captured changes were written to the shadow.
	Applied int64
	// Behind is how far the stream trailed the log's end at its last read, as the server measured it.
	Behind time.Duration
	// Confirmed reports that someone confirmed the cutover, which the engine reads and never writes.
	Confirmed bool
	// UpdatedAt is when the row last moved -- while syncing, when the log was last read -- supplied by the caller.
	UpdatedAt time.Time
}

// Validate reports a state naming no migration, an unknown stage, or names the table cannot store.
func (s ShadowState) Validate() error {
	if strings.TrimSpace(s.Version) == "" {
		return errors.New("a shadow-table state names no migration version")
	}

	if !s.Stage.Valid() {
		return fmt.Errorf("the shadow-table state of %s records the unknown stage %q", s.Version, s.Stage)
	}

	for _, name := range []string{s.Table, s.Shadow, s.Retired, s.Capture} {
		if strings.TrimSpace(name) == "" || len(name) > nameLimit {
			return fmt.Errorf("the shadow-table state of %s names %q, and a name is 1 to %d bytes",
				s.Version, name, nameLimit)
		}
	}

	if s.Behind < 0 {
		return fmt.Errorf("the shadow-table state of %s records a stream %s behind the log, which no "+
			"reading measures", s.Version, s.Behind)
	}

	if s.Copied < 0 || s.Applied < 0 {
		return fmt.Errorf("the shadow-table state of %s records %d rows copied and %d changes "+
			"applied, which nothing leaves", s.Version, s.Copied, s.Applied)
	}

	return nil
}

// Shadows is a store that remembers where each shadow-table migration stands.
type Shadows interface {
	// BeginShadow records a new shadow-table migration, unconfirmed, inside the open transaction.
	BeginShadow(ctx context.Context, s ShadowState) error
	// SaveShadow records how far one got inside the open transaction, leaving its confirmation as it is.
	SaveShadow(ctx context.Context, s ShadowState) error
	// ConfirmShadow records that the cutover of one is confirmed, where it is recorded at all.
	ConfirmShadow(ctx context.Context, version string) error
	// ShadowStates returns every one recorded, and none where the table does not exist yet.
	ShadowStates(ctx context.Context) ([]ShadowState, error)
	// ClearShadow removes the record of one, where there is one.
	ClearShadow(ctx context.Context, version string) error
}

// ShadowTable names the table a history table's shadow-table migrations live in.
func ShadowTable(table string) string {
	return table + shadowSuffix
}

// ShadowSchema returns the operations that create the shadow-table state beside a history table.
func ShadowSchema(table string) ir.Operations {
	return ir.Operations{
		ir.CreateTable{
			Table:      ShadowTable(table),
			Columns:    shadowColumns(),
			PrimaryKey: []string{"version"},
		},
	}
}

// shadowColumns returns one column per ShadowState field, in the order the state declares them.
func shadowColumns() []ir.Column {
	name := typeOf(fmt.Sprintf("string(%d)", nameLimit))

	return []ir.Column{
		{Name: "version", Type: typeOf("string(64)")},
		{Name: "table_name", Type: name},
		{Name: "shadow", Type: name},
		{Name: "retired", Type: name},
		{Name: "capture", Type: name},
		{Name: "stage", Type: typeOf("string(16)")},
		{Name: "cursor", Type: typeOf("text")},
		{Name: "position", Type: typeOf("text")},
		{Name: "copied", Type: typeOf("int64")},
		{Name: "applied", Type: typeOf("int64")},
		{Name: "behind", Type: typeOf("int64")},
		{Name: "confirmed", Type: typeOf("bool")},
		{Name: "updated_at", Type: typeOf("timestamptz")},
	}
}

// BehindMillis is how a recorded lag is stored: whole milliseconds.
func BehindMillis(d time.Duration) int64 {
	return d.Milliseconds()
}

// BehindOf reads a stored lag back from its whole milliseconds.
func BehindOf(millis int64) time.Duration {
	return time.Duration(millis) * time.Millisecond
}

// FindShadow returns the state of one migration among several, and whether there is one.
func FindShadow(states []ShadowState, version string) (ShadowState, bool) {
	for _, s := range states {
		if s.Version == version {
			return s, true
		}
	}

	return ShadowState{}, false
}

// Owned names every table migr keeps for one history table: the history and the three beside it.
func Owned(table string) []string {
	return []string{table, ProgressTable(table), ReleaseTable(table), ShadowTable(table)}
}

// OwnedSchema returns the operations that create every table migr keeps for one history table.
func OwnedSchema(table string) ir.Operations {
	return slices.Concat(Schema(table), ProgressSchema(table), ReleaseSchema(table), ShadowSchema(table))
}
