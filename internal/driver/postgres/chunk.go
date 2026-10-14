package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// A chunk is two statements in the transaction the engine opened around it: a read of the key of the
// last row it covers, and the operation's own write narrowed to the rows after the previous chunk's
// last key and up to that one. The key range is a row-value comparison, which PostgreSQL turns into
// an index condition on the primary key -- measured with EXPLAIN on 16 for a composite key, the
// UPDATE through a bitmap index scan touching only the chunk's rows. A cursor is bound as text and
// the parameter takes the key column's type from the comparison, so a bigint or a uuid key compares
// as itself; the bound is read back through ::text, which is the form a checkpoint stores.
//
// The ORDER BY names the key qualified, and that is a measurement. PostgreSQL resolves an unqualified
// ORDER BY "id" against the output columns first, and the output column called id is the ::text one
// -- so the bound was the tenth key in text order: a chunk of 10 over keys 1 to 60 covered 18 rows,
// and over keys 1 to a million the tenth in text order is 1000002, one chunk taking the whole table.
// No row was lost or written twice, the range itself comparing as the key's own type; the size was.

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
func (d *pgDriver) CompileChunk(op ir.Operation, key []string) ([]driver.Statement, error) {
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
	read := make([]string, len(key))
	for i, column := range key {
		read[i] = quote(column) + "::text"
	}

	query := "SELECT " + strings.Join(read, ", ") + " FROM " + quote(s.table)
	next := 1

	if after {
		query += " WHERE " + compare(key, ">", next)
		next += len(key)
	}

	return query + " ORDER BY " + qualifiedKey(s.table, key) + " LIMIT 1 OFFSET $" + strconv.Itoa(next)
}

// qualifiedKey names every key column by its table, which is what keeps ORDER BY off the ::text output.
func qualifiedKey(table string, key []string) string {
	qualified := make([]string, len(key))
	for i, column := range key {
		qualified[i] = quote(table) + "." + quote(column)
	}

	return strings.Join(qualified, ", ")
}

// write renders the chunk's write, narrowed to the key range its two bounds leave.
func (s chunkShape) write(key []string, after, upTo bool) string {
	conditions := []string{}
	next := 1

	if after {
		conditions = append(conditions, compare(key, ">", next))
		next += len(key)
	}

	if upTo {
		conditions = append(conditions, compare(key, "<=", next))
	}

	conditions = append(conditions, s.conditions...)
	if len(conditions) == 0 {
		return s.head
	}

	return s.head + " WHERE " + strings.Join(conditions, " AND ")
}

// compare renders the key compared against one bound, numbering its placeholders from first.
func compare(key []string, operator string, first int) string {
	if len(key) == 1 {
		return quote(key[0]) + " " + operator + " $" + strconv.Itoa(first)
	}

	marks := make([]string, len(key))
	for i := range key {
		marks[i] = "$" + strconv.Itoa(first+i)
	}

	return "(" + quoteAll(key) + ") " + operator + " (" + strings.Join(marks, ", ") + ")"
}

// Chunk runs one slice inside the session's open transaction, and reports where it ended.
func (c *pgConn) Chunk(ctx context.Context, s driver.Slice) (driver.Sliced, error) {
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
func (c *pgConn) chunkBound(ctx context.Context, shape chunkShape, s driver.Slice) ([]string, bool, error) {
	args := append(textArgs(s.After), s.Rows-1)

	values := make([]*string, len(s.Key))
	targets := make([]any, len(values))

	for i := range values {
		targets[i] = &values[i]
	}

	err := c.runner().QueryRow(ctx, shape.boundQuery(s.Key, len(s.After) > 0), args...).
		Scan(targets...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("reading where a chunk of %s ends on %s: %w", s.Op.Kind(), Name, err)
	}

	return keyValues(s, values)
}

// keyValues returns a bound's key as text, refusing a NULL that no key comparison could resume after.
func keyValues(s driver.Slice, values []*string) ([]string, bool, error) {
	bound := make([]string, len(values))

	for i, value := range values {
		if value == nil {
			return nil, false, fmt.Errorf("a chunk of %s reached a row whose key column %q is NULL, "+
				"which no cursor can resume after", s.Op.Kind(), s.Key[i])
		}

		bound[i] = *value
	}

	return bound, true, nil
}

// chunkWrite runs the operation's write over the key range one chunk covers, reporting the rows it wrote.
func (c *pgConn) chunkWrite(ctx context.Context, shape chunkShape, s driver.Slice, bound []string,
	found bool,
) (int64, error) {
	args := append(textArgs(s.After), textArgs(bound)...)

	tag, err := c.runner().Exec(ctx, shape.write(s.Key, len(s.After) > 0, found), args...)
	if err != nil {
		return 0, fmt.Errorf("executing a chunk of %s on %s: %w", s.Op.Kind(), Name, err)
	}

	return tag.RowsAffected(), nil
}

// textArgs binds key values as the text a cursor holds them as.
func textArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}

	return args
}

// compile-time proof that this driver renders a chunk and this session runs one.
var (
	_ driver.ChunkCompiler = (*pgDriver)(nil)
	_ driver.Chunker       = (*pgConn)(nil)
)
