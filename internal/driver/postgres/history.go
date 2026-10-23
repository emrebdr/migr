package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// historyColumns is the order this store binds and scans, and the only place that order is stated.
//
// It is spelled out rather than taken from history.Schema because the SQL text must be a constant
// a reader can check against the scan targets. schema_test.go restates the same list for the same
// reason, and an integration test reads three columns by name so a transposition cannot survive a
// round trip that used one ordered list for both halves.
var historyColumns = []string{
	"batch", "ordinal", "version", "name", "direction", "outcome", "checksum", "applied_at",
	"duration_ms", "last_statement", "error", "driver", "runtime", "engine_version", "ir_version",
}

// undefinedTable is the SQLSTATE PostgreSQL raises for a statement naming a relation it lacks.
const undefinedTableCode = "42P01"

// undefinedColumn is the SQLSTATE for a column a statement names and the relation does not have.
const undefinedColumn = "42703"

// pgHistory is one session's view of the migration history, bound to it at Open.
type pgHistory struct {
	session *pgConn
	// table is where runs are recorded, unqualified: Options.Schema set search_path at Open.
	table string
}

// insertStatement renders the parameterized insert this store writes every run through.
func (h pgHistory) insertStatement() string {
	placeholders := make([]string, len(historyColumns))
	for i := range historyColumns {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	return "INSERT INTO " + quote(h.table) + " (" + quoteAll(historyColumns) + ")\nVALUES (" +
		strings.Join(placeholders, ", ") + ")"
}

// selectStatement renders the read this store scans every run out of, ordered as given.
func (h pgHistory) selectStatement(order string) string {
	return "SELECT " + quoteAll(historyColumns) + "\nFROM " + quote(h.table) + "\nORDER BY " + order
}

// runOrder is the order the runs happened, which is the key rather than the clock.
const runOrder = `"batch", "ordinal"`

// latestOrder reverses the run order, so one row is the most recent run.
const latestOrder = `"batch" DESC, "ordinal" DESC LIMIT 1`

// Record appends a run that completed, and is the only method that writes Applied.
func (h pgHistory) Record(ctx context.Context, rec history.Record) error {
	rec.Outcome = history.Applied
	rec.Error = ""
	rec.LastStatement = 0

	return h.write(ctx, rec)
}

// MarkFailed appends a run that failed without changing the schema.
func (h pgHistory) MarkFailed(ctx context.Context, rec history.Record, cause error) error {
	if err := h.checkCause("MarkFailed", cause); err != nil {
		return err
	}

	rec.Outcome = history.Failed
	rec.Error = cause.Error()
	rec.LastStatement = 0

	return h.write(ctx, rec)
}

// MarkDirty appends a run that failed partway, naming the last statement that succeeded.
func (h pgHistory) MarkDirty(ctx context.Context, rec history.Record, last int32, cause error) error {
	if err := h.checkCause("MarkDirty", cause); err != nil {
		return err
	}

	rec.Outcome = history.Dirty
	rec.Error = cause.Error()
	rec.LastStatement = last

	return h.write(ctx, rec)
}

// MarkResolved appends an operator's assertion that a migration left between states is settled.
func (h pgHistory) MarkResolved(ctx context.Context, rec history.Record, reason string) error {
	rec.Outcome = history.Resolved
	rec.Error = reason
	rec.LastStatement = 0

	return h.write(ctx, rec)
}

// checkCause refuses a run marked as not completing without saying what stopped it.
func (h pgHistory) checkCause(method string, cause error) error {
	if cause == nil {
		return &HistoryError{Table: h.table, Detail: method +
			" needs the failure it is recording; a run that did not complete must say why"}
	}

	return nil
}

// write stamps the build, holds the record to what a run must carry, and appends it.
func (h pgHistory) write(ctx context.Context, rec history.Record) error {
	rec = rec.Stamp()

	if err := rec.Validate(); err != nil {
		return err
	}

	if _, err := h.session.runner().Exec(ctx, h.insertStatement(), h.values(rec)...); err != nil {
		return h.classifyWrite(rec, err)
	}

	return nil
}

// values renders a record as the arguments the insert binds, in historyColumns order.
func (h pgHistory) values(rec history.Record) []any {
	return []any{
		rec.Batch, rec.Ordinal, rec.Version, rec.Name, string(rec.Direction), string(rec.Outcome),
		rec.Checksum.String(), rec.AppliedAt, rec.Duration.Milliseconds(), rec.LastStatement,
		nullable(rec.Error), rec.Driver, rec.Runtime, rec.EngineVersion, rec.IRVersion,
	}
}

// nullable renders a string that means "nothing was recorded" as NULL rather than as an empty one.
func nullable(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// classifyWrite separates the three ways a write fails for a reason an operator can act on.
func (h pgHistory) classifyWrite(rec history.Record, err error) error {
	switch {
	case hasCode(err, undefinedTableCode):
		return h.missingTable(err)
	case hasCode(err, undefinedColumn):
		return &HistoryError{Table: h.table, Err: errors.Join(ErrHistoryShape, err), Detail: fmt.Sprintf(
			"%v: recording migration %q failed on a column it does not have", ErrHistoryShape, rec.Version)}
	case hasCode(err, uniqueViolation):
		return &HistoryError{Table: h.table, Err: errors.Join(ErrAlreadyRecorded, err), Detail: fmt.Sprintf(
			"%v: batch %d ordinal %d already names a run, and recording migration %q there would "+
				"replace the evidence of it", ErrAlreadyRecorded, rec.Batch, rec.Ordinal, rec.Version)}
	default:
		return h.wrap(fmt.Sprintf("recording migration %q", rec.Version), err)
	}
}

// List returns every recorded run, in the order the runs happened.
func (h pgHistory) List(ctx context.Context) ([]history.Record, error) {
	rows, err := h.session.runner().Query(ctx, h.selectStatement(runOrder))
	if err != nil {
		return nil, h.classifyRead(err)
	}

	records, err := pgx.CollectRows(rows, h.scan)
	if err != nil {
		return nil, h.classifyRead(err)
	}

	return records, nil
}

// Latest returns the most recent run, and whether there has been one at all.
func (h pgHistory) Latest(ctx context.Context) (history.Record, bool, error) {
	rows, err := h.session.runner().Query(ctx, h.selectStatement(latestOrder))
	if err != nil {
		return history.Record{}, false, h.classifyRead(err)
	}

	rec, err := pgx.CollectExactlyOneRow(rows, h.scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return history.Record{}, false, nil
	}

	if err != nil {
		return history.Record{}, false, h.classifyRead(err)
	}

	return rec, true, nil
}

// classifyRead reports a missing history table as itself, never as a database with no runs.
func (h pgHistory) classifyRead(err error) error {
	if hasCode(err, undefinedTableCode) {
		return h.missingTable(err)
	}

	return h.wrap("reading the history", err)
}

// missingTable reports a table nothing created, which reading as "no runs yet" would re-run everything.
func (h pgHistory) missingTable(err error) error {
	return &HistoryError{Table: h.table, Err: errors.Join(history.ErrNoTable, err), Detail: fmt.Sprintf(
		"%v. Ensure creates it; a database that has one under another name is a history.table "+
			"that does not match the one migrations were recorded under", history.ErrNoTable)}
}

// scan reads one row back, reporting a value this build cannot decode as the corrupt row it is.
func (h pgHistory) scan(row pgx.CollectableRow) (history.Record, error) {
	var rec history.Record
	var direction, outcome, sum string
	var failure *string
	var durationMS int64

	err := row.Scan(&rec.Batch, &rec.Ordinal, &rec.Version, &rec.Name, &direction, &outcome, &sum,
		&rec.AppliedAt, &durationMS, &rec.LastStatement, &failure, &rec.Driver, &rec.Runtime,
		&rec.EngineVersion, &rec.IRVersion)
	if err != nil {
		return history.Record{}, h.wrap("reading a row of the history", err)
	}

	rec.Duration = time.Duration(durationMS) * time.Millisecond
	rec.Error = text(failure)

	return h.decode(rec, direction, outcome, sum)
}

// decode turns the three recorded strings back into the types a record carries.
func (h pgHistory) decode(rec history.Record, direction, outcome, sum string) (history.Record, error) {
	rec.Direction = ir.Direction(direction)
	if !rec.Direction.Valid() {
		return history.Record{}, h.corrupt(rec, "direction",
			fmt.Errorf("%q names neither %s nor %s", direction, ir.Up, ir.Down))
	}

	rec.Outcome = history.Outcome(outcome)
	if !rec.Outcome.Valid() {
		return history.Record{}, h.corrupt(rec, "outcome",
			fmt.Errorf("%q names none of the outcomes this build records", outcome))
	}

	parsed, err := checksum.Parse(sum)
	if err != nil {
		return history.Record{}, h.corrupt(rec, "checksum", err)
	}

	rec.Checksum = parsed

	return rec, nil
}

// corrupt reports a recorded value this build cannot read back, naming the row that holds it.
func (h pgHistory) corrupt(rec history.Record, field string, err error) error {
	return &history.CorruptError{
		Table:   h.table,
		Version: rec.Version,
		Batch:   rec.Batch,
		Ordinal: rec.Ordinal,
		Field:   field,
		Err:     err,
	}
}

// text renders a column that was NULL as the empty string a record carries for it.
func text(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// wrap reports a history failure that is the server's rather than this build's to explain.
func (h pgHistory) wrap(doing string, err error) error {
	return &HistoryError{Table: h.table, Err: err, Detail: doing + ": " + err.Error()}
}

// hasCode reports whether a failure is the server raising one particular SQLSTATE.
func hasCode(err error, code string) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) && pgErr.Code == code
}

// compile-time proof that this store satisfies the contract every driver's store is held to.
var _ history.Store = pgHistory{}
