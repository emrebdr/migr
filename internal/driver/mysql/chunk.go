package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// A chunk is two statements in the transaction the engine opened around it: a read of the key of the
// last row it covers, and the operation's own write narrowed to the rows after the previous chunk's
// last key and up to that one.
//
// The key range is never a row-value comparison on this engine, and that is a measurement rather
// than a style. Both MySQL 8.0 and MariaDB 11.4 answer (a, b) > (?, ?) by scanning the primary key
// from its start -- 49,101 handler reads to return the 100th row past a cursor near the end of a
// 50,000-row table -- and an UPDATE under REPEATABLE READ locks every row it scans, so a chunk written
// that way held a lock on every row of the table for the length of its transaction: a write to a
// row far outside the chunk timed out. Spelled out as a >= ? AND (a > ? OR b > ?) the same read is
// 101 handler reads and the same UPDATE locks its own range, plus the gap at its upper bound. A
// single-column key is a plain comparison either way. Every value is the server's own text.

// chunkShape is a data operation split into the table whose key a chunk pages and what it writes.
type chunkShape struct {
	// table is the table whose primary key the chunks page through.
	table string
	// head is the operation's statement up to the clause a chunk narrows it by.
	head string
	// conditions are what the write keeps beyond the key range.
	conditions []string
}

// shapeOf splits a data operation into a chunk's shape, refusing what is not one or cannot run here.
func shapeOf(op ir.Operation, key []string) (chunkShape, error) {
	if problem := checkColumnNames("key", key); problem != "" {
		return chunkShape{}, refuse(op, problem)
	}

	switch o := op.(type) {
	case ir.DataBackfill:
		if problem := checkDataBackfill(o); problem != "" {
			return chunkShape{}, refuse(op, problem)
		}

		return chunkShape{table: o.Table, head: backfillHead(o),
			conditions: []string{backfillCondition(o)}}, nil
	case ir.DataTransform:
		if problem := checkDataTransform(o); problem != "" {
			return chunkShape{}, refuse(op, problem)
		}

		return chunkShape{table: o.Table, head: transformHead(o)}, nil
	case ir.DataCopy:
		if problem := checkDataCopy(o); problem != "" {
			return chunkShape{}, refuse(op, problem)
		}

		return chunkShape{table: o.From, head: copyHead(o)}, nil
	default:
		return chunkShape{}, refuse(op, "only a data operation runs in chunks")
	}
}

// CompileChunk returns a chunk's read of its upper bound and its write, keyed by the given columns.
func (d *mysqlDriver) CompileChunk(op ir.Operation, key []string) ([]driver.Statement, error) {
	shape, err := shapeOf(op, key)
	if err != nil {
		return nil, err
	}

	return []driver.Statement{
		{SQL: shape.boundQuery(key, true) + terminator},
		{SQL: shape.write(key, true, true) + terminator},
	}, nil
}

// boundQuery renders the read of the key of the last row a chunk covers, after a cursor or from the start.
func (s chunkShape) boundQuery(key []string, after bool) string {
	query := "SELECT " + quoteAll(key) + " FROM " + quote(s.table)

	if after {
		query += " WHERE " + pastCursor(key)
	}

	return query + " ORDER BY " + quoteAll(key) + " LIMIT 1 OFFSET ?"
}

// write renders the chunk's write, narrowed to the key range its two bounds leave.
func (s chunkShape) write(key []string, lower, upper bool) string {
	conditions := []string{}

	if lower {
		conditions = append(conditions, pastCursor(key))
	}

	if upper {
		conditions = append(conditions, atOrBefore(key))
	}

	conditions = append(conditions, s.conditions...)
	if len(conditions) == 0 {
		return s.head
	}

	return s.head + " WHERE " + strings.Join(conditions, " AND ")
}

// pastCursor renders the key strictly after a cursor, leading with a bound on its first column.
func pastCursor(key []string) string {
	first := quote(key[0])
	if len(key) == 1 {
		return first + " > ?"
	}

	return first + " >= ? AND (" + first + " > ? OR (" + pastCursor(key[1:]) + "))"
}

// atOrBefore renders the key at or before a bound, leading with a bound on its first column.
func atOrBefore(key []string) string {
	first := quote(key[0])
	if len(key) == 1 {
		return first + " <= ?"
	}

	return first + " <= ? AND (" + first + " < ? OR (" + atOrBefore(key[1:]) + "))"
}

// spelled binds a key's values in the order pastCursor and atOrBefore name them, each but the last twice.
func spelled(values []string) []any {
	if len(values) == 0 {
		return nil
	}

	if len(values) == 1 {
		return []any{values[0]}
	}

	return append([]any{values[0], values[0]}, spelled(values[1:])...)
}

// Chunk runs one slice inside the session's open transaction, and reports where it ended.
func (c *mysqlConn) Chunk(ctx context.Context, s driver.Slice) (driver.Sliced, error) {
	if c.tx == nil {
		return driver.Sliced{}, fmt.Errorf("%w: a chunk runs inside the transaction its checkpoint "+
			"is written in, and this session has none open", ErrTransaction)
	}

	if s.Rows < 1 {
		return driver.Sliced{}, fmt.Errorf("a chunk of %s must cover at least one row, not %d",
			s.Op.Kind(), s.Rows)
	}

	shape, err := shapeOf(s.Op, s.Key)
	if err != nil {
		return driver.Sliced{}, err
	}

	bound, found, err := c.chunkBound(ctx, shape, s)
	if err != nil {
		return driver.Sliced{}, err
	}

	written, err := c.chunkWrite(ctx, shape, s, bound, found)
	if err != nil {
		return driver.Sliced{}, err
	}

	if !found {
		return driver.Sliced{Written: written, Last: s.After, Done: true}, nil
	}

	return driver.Sliced{Written: written, Last: bound}, nil
}

// chunkBound reads the key of the last row a chunk covers, and reports none when fewer rows remain.
func (c *mysqlConn) chunkBound(ctx context.Context, shape chunkShape, s driver.Slice) ([]string, bool, error) {
	args := append(spelled(s.After), s.Rows-1)

	values := make([]sql.NullString, len(s.Key))
	targets := make([]any, len(values))

	for i := range values {
		targets[i] = &values[i]
	}

	err := c.runner().QueryRowContext(ctx, shape.boundQuery(s.Key, len(s.After) > 0), args...).
		Scan(targets...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("reading where a chunk of %s ends on %s: %w", s.Op.Kind(), Name, err)
	}

	return keyValues(s, values)
}

// keyValues returns a bound's key as text, refusing a NULL that no key comparison could resume after.
func keyValues(s driver.Slice, values []sql.NullString) ([]string, bool, error) {
	bound := make([]string, len(values))

	for i, value := range values {
		if !value.Valid {
			return nil, false, fmt.Errorf("a chunk of %s reached a row whose key column %q is NULL, "+
				"which no cursor can resume after", s.Op.Kind(), s.Key[i])
		}

		bound[i] = value.String
	}

	return bound, true, nil
}

// chunkWrite runs the operation's write over the key range one chunk covers, reporting the rows it wrote.
func (c *mysqlConn) chunkWrite(ctx context.Context, shape chunkShape, s driver.Slice, bound []string,
	found bool,
) (int64, error) {
	args := append(spelled(s.After), spelled(bound)...)

	result, err := c.runner().ExecContext(ctx, shape.write(s.Key, len(s.After) > 0, found), args...)
	if err != nil {
		return 0, fmt.Errorf("executing a chunk of %s on %s: %w", s.Op.Kind(), Name, err)
	}

	written, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting the rows a chunk of %s wrote on %s: %w", s.Op.Kind(), Name, err)
	}

	return written, nil
}

// compile-time proof that this driver renders a chunk and this session runs one.
var (
	_ driver.ChunkCompiler = (*mysqlDriver)(nil)
	_ driver.Chunker       = (*mysqlConn)(nil)
)
