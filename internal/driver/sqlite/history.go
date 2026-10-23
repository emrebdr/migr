package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	lib "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// historyColumns is the order this store binds and scans, and the only place that order is stated.
//
// It is spelled out rather than taken from history.Schema because the SQL text must be a constant a
// reader can check against the scan targets, which is PostgreSQL's reasoning unchanged.
// history_schema_test.go holds this list against the neutral schema's own columns.
var historyColumns = []string{
	"batch", "ordinal", "version", "name", "direction", "outcome", "checksum", "applied_at",
	"duration_ms", "last_statement", "error", "driver", "runtime", "engine_version", "ir_version",
}

// runOrder is the order the runs happened, which is the key rather than the clock.
const runOrder = `"batch", "ordinal"`

// latestOrder reverses the run order, so one row is the most recent run.
const latestOrder = `"batch" DESC, "ordinal" DESC LIMIT 1`

// sqliteHistory is one session's view of the migration history, bound to it at Open.
type sqliteHistory struct {
	session *sqliteConn
	// table is where runs are recorded, in the one schema a SQLite database has.
	table string
}

// insertStatement renders the parameterized insert this store writes every run through.
func (h sqliteHistory) insertStatement() string {
	placeholders := make([]string, len(historyColumns))
	for i := range historyColumns {
		placeholders[i] = "?"
	}

	return "INSERT INTO " + quote(h.table) + " (" + quoteAll(historyColumns) + ")\nVALUES (" +
		strings.Join(placeholders, ", ") + ")"
}

// selectStatement renders the read this store scans every run out of, ordered as given.
func (h sqliteHistory) selectStatement(order string) string {
	return "SELECT " + quoteAll(historyColumns) + "\nFROM " + quote(h.table) + "\nORDER BY " + order
}

// Record appends a run that completed, and is the only method that writes Applied.
func (h sqliteHistory) Record(ctx context.Context, rec history.Record) error {
	rec.Outcome = history.Applied
	rec.Error = ""
	rec.LastStatement = 0

	return h.write(ctx, rec)
}

// MarkFailed appends a run that failed without changing the schema.
func (h sqliteHistory) MarkFailed(ctx context.Context, rec history.Record, cause error) error {
	if err := h.checkCause("MarkFailed", cause); err != nil {
		return err
	}

	rec.Outcome = history.Failed
	rec.Error = cause.Error()
	rec.LastStatement = 0

	return h.write(ctx, rec)
}

// MarkDirty appends a run that failed partway, naming the last statement that succeeded.
func (h sqliteHistory) MarkDirty(ctx context.Context, rec history.Record, last int32,
	cause error,
) error {
	if err := h.checkCause("MarkDirty", cause); err != nil {
		return err
	}

	rec.Outcome = history.Dirty
	rec.Error = cause.Error()
	rec.LastStatement = last

	return h.write(ctx, rec)
}

// MarkResolved appends an operator's assertion that a migration left between states is settled.
func (h sqliteHistory) MarkResolved(ctx context.Context, rec history.Record, reason string) error {
	rec.Outcome = history.Resolved
	rec.Error = reason
	rec.LastStatement = 0

	return h.write(ctx, rec)
}

// checkCause refuses a run marked as not completing without saying what stopped it.
func (h sqliteHistory) checkCause(method string, cause error) error {
	if cause == nil {
		return &HistoryError{Table: h.table, Detail: method +
			" needs the failure it is recording; a run that did not complete must say why"}
	}

	return nil
}

// write stamps the build, holds the record to what a run must carry, and appends it.
func (h sqliteHistory) write(ctx context.Context, rec history.Record) error {
	rec = rec.Stamp()

	if err := rec.Validate(); err != nil {
		return err
	}

	_, err := h.session.runner().ExecContext(ctx, h.insertStatement(), h.values(rec)...)
	if err != nil {
		return h.explainWrite(ctx, rec, err)
	}

	return nil
}

// values renders a record as the arguments the insert binds, in historyColumns order.
func (h sqliteHistory) values(rec history.Record) []any {
	return []any{
		rec.Batch, rec.Ordinal, rec.Version, rec.Name, string(rec.Direction), string(rec.Outcome),
		rec.Checksum.String(), renderTime(rec.AppliedAt), rec.Duration.Milliseconds(),
		rec.LastStatement, nullText(rec.Error), rec.Driver, rec.Runtime, rec.EngineVersion,
		rec.IRVersion,
	}
}

// nullText renders a string meaning "nothing was recorded" as NULL rather than as an empty one.
func nullText(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// explainWrite asks the catalog why a write failed, which is also where a write reports ErrNoTable.
func (h sqliteHistory) explainWrite(ctx context.Context, rec history.Record, cause error) error {
	if hasCode(cause, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
		return h.alreadyRecorded(rec, cause)
	}

	outliving := context.WithoutCancel(ctx)

	if err := h.requireTable(outliving); err != nil {
		return err
	}

	if err := h.checkColumns(outliving); err != nil {
		return err
	}

	return h.wrap(fmt.Sprintf("recording migration %q", rec.Version), cause)
}

// alreadyRecorded reports a run already written under the batch and ordinal it claims.
func (h sqliteHistory) alreadyRecorded(rec history.Record, cause error) error {
	return &HistoryError{Table: h.table, Err: errors.Join(ErrAlreadyRecorded, cause),
		Detail: fmt.Sprintf("%v: batch %d ordinal %d already names a run, and recording migration "+
			"%q there would replace the evidence of it", ErrAlreadyRecorded, rec.Batch, rec.Ordinal,
			rec.Version)}
}

// List returns every recorded run, in the order the runs happened.
func (h sqliteHistory) List(ctx context.Context) ([]history.Record, error) {
	if err := h.requireTable(ctx); err != nil {
		return nil, err
	}

	rows, err := h.session.runner().QueryContext(ctx, h.selectStatement(runOrder))
	if err != nil {
		return nil, h.wrap("reading the history", err)
	}

	defer rows.Close()

	records, err := h.collect(rows)
	if err != nil {
		return nil, err
	}

	if err := rows.Err(); err != nil {
		return nil, h.wrap("reading the history", err)
	}

	return records, nil
}

// collect scans every row a read returned, stopping at the first this build cannot read back.
func (h sqliteHistory) collect(rows *sql.Rows) ([]history.Record, error) {
	var records []history.Record

	for rows.Next() {
		rec, err := h.scan(rows)
		if err != nil {
			return nil, err
		}

		records = append(records, rec)
	}

	return records, nil
}

// Latest returns the most recent run, and whether there has been one at all.
func (h sqliteHistory) Latest(ctx context.Context) (history.Record, bool, error) {
	if err := h.requireTable(ctx); err != nil {
		return history.Record{}, false, err
	}

	rows, err := h.session.runner().QueryContext(ctx, h.selectStatement(latestOrder))
	if err != nil {
		return history.Record{}, false, h.wrap("reading the history", err)
	}

	defer rows.Close()

	if !rows.Next() {
		return history.Record{}, false, h.checkEmpty(rows)
	}

	rec, err := h.scan(rows)
	if err != nil {
		return history.Record{}, false, err
	}

	return rec, true, rows.Err()
}

// checkEmpty separates a history with no runs from a read that ended before it returned any.
func (h sqliteHistory) checkEmpty(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		return h.wrap("reading the history", err)
	}

	return nil
}

// scan reads one row back, reporting a value this build cannot decode as the corrupt row it is.
func (h sqliteHistory) scan(rows *sql.Rows) (history.Record, error) {
	var rec history.Record
	var direction, outcome, sum, applied string
	var failure *string
	var durationMS int64

	err := rows.Scan(&rec.Batch, &rec.Ordinal, &rec.Version, &rec.Name, &direction, &outcome, &sum,
		&applied, &durationMS, &rec.LastStatement, &failure, &rec.Driver, &rec.Runtime,
		&rec.EngineVersion, &rec.IRVersion)
	if err != nil {
		return history.Record{}, h.wrap("reading a row of the history", err)
	}

	rec.Duration = time.Duration(durationMS) * time.Millisecond
	rec.Error = recordedText(failure)

	return h.decode(rec, direction, outcome, sum, applied)
}

// decode turns the four recorded strings back into the types a record carries.
func (h sqliteHistory) decode(rec history.Record,
	direction, outcome, sum, applied string,
) (history.Record, error) {
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

	return h.decodeRest(rec, sum, applied)
}

// decodeRest turns the checksum and the instant back, both of which this build parses itself.
func (h sqliteHistory) decodeRest(rec history.Record, sum, applied string) (history.Record, error) {
	parsed, err := checksum.Parse(sum)
	if err != nil {
		return history.Record{}, h.corrupt(rec, "checksum", err)
	}

	rec.Checksum = parsed

	at, err := parseTime(applied)
	if err != nil {
		return history.Record{}, h.corrupt(rec, "applied_at", err)
	}

	rec.AppliedAt = at

	return rec, nil
}

// corrupt reports a recorded value this build cannot read back, naming the row that holds it.
func (h sqliteHistory) corrupt(rec history.Record, field string, err error) error {
	return &history.CorruptError{
		Table:   h.table,
		Version: rec.Version,
		Batch:   rec.Batch,
		Ordinal: rec.Ordinal,
		Field:   field,
		Err:     err,
	}
}

// recordedText renders a column that was NULL as the empty string a record carries for it.
func recordedText(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// wrap reports a history failure that is the engine's rather than this build's to explain.
func (h sqliteHistory) wrap(doing string, err error) error {
	return &HistoryError{Table: h.table, Err: err, Detail: doing + ": " + err.Error()}
}

// hasCode reports whether a failure is SQLite raising one particular result code.
func hasCode(err error, code int) bool {
	var reported *lib.Error

	return errors.As(err, &reported) && reported.Code() == code
}

// hasPrimaryCode reports an error whose result code is code or one of its extended codes, which share its low byte.
func hasPrimaryCode(err error, code int) bool {
	var reported *lib.Error

	return errors.As(err, &reported) && reported.Code()&0xff == code
}

// compile-time proof that this store satisfies the contract every driver's store is held to.
var _ history.Store = sqliteHistory{}
