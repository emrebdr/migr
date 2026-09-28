package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	lib "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/emrebdr/migr/internal/driver"
)

// hexPrefix opens the rendering of a bytes value, which is the one PostgreSQL gives a bytea.
const hexPrefix = `\x`

// Query runs a migration's own read with query_only set, so SQLite refuses a write rather than migr.
func (c *sqliteConn) Query(ctx context.Context, q driver.Query) (out driver.Rows, err error) {
	if err := c.outsideTransaction(ErrQuery); err != nil {
		return driver.Rows{}, err
	}

	if err := pinPragma(ctx, c, pragmaQueryOnly, pragmaOn); err != nil {
		return driver.Rows{}, fmt.Errorf("%w: %w", ErrQuery, err)
	}

	defer func() { err = errors.Join(err, c.allowWrites(ctx)) }()

	return c.readText(ctx, q)
}

// allowWrites puts the session back, on a context that outlives whatever canceled the read.
func (c *sqliteConn) allowWrites(ctx context.Context) error {
	outliving := context.WithoutCancel(ctx)

	if err := pinPragma(outliving, c, pragmaQueryOnly, pragmaOff); err != nil {
		return fmt.Errorf("%w: %w", ErrQuery, err)
	}

	return nil
}

// Perform runs a migration's own statement, outside the transaction its operations will run in.
func (c *sqliteConn) Perform(ctx context.Context, q driver.Query) (int64, error) {
	if err := c.outsideTransaction(ErrPerform); err != nil {
		return 0, err
	}

	result, err := c.conn.ExecContext(ctx, q.SQL, bindings(q.Args)...)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrPerform, err)
	}

	changed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("%w: reading how many rows it changed: %w", ErrPerform, err)
	}

	return changed, nil
}

// outsideTransaction refuses a migration's own SQL on a session that is already inside one.
func (c *sqliteConn) outsideTransaction(sentinel error) error {
	if err := c.checkIdle(); err != nil {
		return fmt.Errorf("%w: %w", sentinel, err)
	}

	return nil
}

// readText runs the statement and renders every value SQLite returns as text.
func (c *sqliteConn) readText(ctx context.Context, q driver.Query) (driver.Rows, error) {
	rows, err := c.conn.QueryContext(ctx, q.SQL, bindings(q.Args)...)
	if err != nil {
		return driver.Rows{}, classifyRead(err)
	}

	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return driver.Rows{}, fmt.Errorf("%w: naming the columns: %w", ErrQuery, err)
	}

	out := driver.Rows{Columns: columns, Values: [][]*string{}}

	for rows.Next() {
		row, err := textRow(rows, columns)
		if err != nil {
			return driver.Rows{}, err
		}

		out.Values = append(out.Values, row)
	}

	if err := rows.Err(); err != nil {
		return driver.Rows{}, classifyRead(err)
	}

	return out, nil
}

// textRow scans one row as whatever SQLite stored and renders each value as text.
func textRow(rows *sql.Rows, columns []string) ([]*string, error) {
	scanned := make([]any, len(columns))
	into := make([]any, len(columns))

	for i := range scanned {
		into[i] = &scanned[i]
	}

	if err := rows.Scan(into...); err != nil {
		return nil, fmt.Errorf("%w: reading a row: %w", ErrQuery, err)
	}

	row := make([]*string, len(columns))

	for i, value := range scanned {
		text, err := textValue(columns[i], value)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrQuery, err)
		}

		row[i] = text
	}

	return row, nil
}

// textValue renders one stored value the way docs/protocol.md §5 carries it, and nil for a null.
func textValue(column string, value any) (*string, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case int64:
		return text(strconv.FormatInt(v, 10)), nil
	case float64:
		return text(strconv.FormatFloat(v, 'g', -1, 64)), nil
	case []byte:
		return text(hexPrefix + hex.EncodeToString(v)), nil
	case string:
		return checkUTF8(column, v)
	default:
		return nil, &NotTextError{Column: column, Detail: describeValue(value)}
	}
}

// checkUTF8 refuses a text value no JSON string could carry without being silently rewritten.
func checkUTF8(column, value string) (*string, error) {
	if !utf8.ValidString(value) {
		return nil, &NotTextError{Column: column, Detail: fmt.Sprintf("SQLite returned %d bytes of "+
			"text that are not valid UTF-8, which SQLite permits in a TEXT column", len(value))}
	}

	return text(value), nil
}

// text takes the address of a rendered value, since a null is the nil this type distinguishes.
func text(value string) *string {
	return &value
}

// classifyRead reports a write SQLite refused as the refusal it is, rather than as a failure.
func classifyRead(err error) error {
	var refused *lib.Error
	if errors.As(err, &refused) && refused.Code() == sqlite3.SQLITE_READONLY {
		return fmt.Errorf("%w: %w: %w", ErrQuery, driver.ErrNotReadOnly, err)
	}

	return fmt.Errorf("%w: %w", ErrQuery, err)
}

// bindings renders a migration's textual parameters, which SQLite binds positionally as text.
func bindings(args []string) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		out[i] = arg
	}

	return out
}
