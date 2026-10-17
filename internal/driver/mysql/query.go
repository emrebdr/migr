package mysql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	lib "github.com/go-sql-driver/mysql"

	"github.com/emrebdr/migr/internal/driver"
)

// hexPrefix opens the rendering of a bytes value, which is the one PostgreSQL gives a bytea.
const hexPrefix = `\x`

// readOnlyRefused is the error number both servers raise for a write under a read-only transaction.
const readOnlyRefused = 1792

// binaryTypes are the column types whose bytes are not text, and so are rendered as hex.
//
// MySQL hands text and bytes to a client as the same Go type -- everything but an integer and a
// double arrives as []byte, measured across twenty column types -- so unlike SQLite, the value
// itself cannot say which it is. The column can: go-sql-driver derives BLOB from TEXT, and BINARY
// from CHAR, by whether the field's character set is the binary one.
var binaryTypes = map[string]bool{
	"BLOB": true, "TINYBLOB": true, "MEDIUMBLOB": true, "LONGBLOB": true,
	"BINARY": true, "VARBINARY": true, "GEOMETRY": true, "BIT": true, "VECTOR": true,
}

// Query runs a migration's own read with the session refused writes, so the server refuses, not migr.
func (c *mysqlConn) Query(ctx context.Context, q driver.Query) (driver.Rows, error) {
	var out driver.Rows

	err := c.interruptible(ctx, func(ctx context.Context) error {
		read, err := c.readOnly(ctx, q)
		out = read

		return err
	})

	return out, err
}

// readOnly runs the read inside a read-only transaction the server enforces, the session pinned around it.
func (c *mysqlConn) readOnly(ctx context.Context, q driver.Query) (out driver.Rows, err error) {
	if err := c.outsideTransaction(ErrQuery); err != nil {
		return driver.Rows{}, err
	}

	if err := pinNumber(ctx, c, varReadOnly, variableOn); err != nil {
		return driver.Rows{}, fmt.Errorf("%w: %w", ErrQuery, err)
	}

	defer func() { err = errors.Join(err, c.allowWrites(ctx)) }()

	tx, err := c.conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return driver.Rows{}, fmt.Errorf("%w: opening the read-only transaction: %w", ErrQuery, err)
	}

	defer func() { err = errors.Join(err, discard(tx)) }()

	return readText(ctx, tx, q)
}

// allowWrites puts the session back, on a context that outlives whatever canceled the read.
func (c *mysqlConn) allowWrites(ctx context.Context) error {
	outliving := context.WithoutCancel(ctx)

	if err := pinNumber(outliving, c, varReadOnly, variableOff); err != nil {
		return fmt.Errorf("%w: %w", ErrQuery, err)
	}

	return nil
}

// discard rolls back a read, and one database/sql already rolled back is not a second failure.
func discard(tx *sql.Tx) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("%w: discarding the read-only transaction: %w", ErrQuery, err)
	}

	return nil
}

// Perform runs a migration's own statement, outside the transaction its operations will run in.
func (c *mysqlConn) Perform(ctx context.Context, q driver.Query) (int64, error) {
	if err := c.outsideTransaction(ErrPerform); err != nil {
		return 0, err
	}

	var changed int64

	err := c.interruptible(ctx, func(ctx context.Context) error {
		result, err := c.conn.ExecContext(ctx, q.SQL, bindings(q.Args)...)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrPerform, err)
		}

		changed, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("%w: reading how many rows it changed: %w", ErrPerform, err)
		}

		return nil
	})

	return changed, err
}

// outsideTransaction refuses a migration's own SQL on a session that is already inside one.
func (c *mysqlConn) outsideTransaction(sentinel error) error {
	if err := c.checkIdle(); err != nil {
		return fmt.Errorf("%w: %w", sentinel, err)
	}

	return nil
}

// readText runs the statement and renders every value the server returns as text.
func readText(ctx context.Context, tx *sql.Tx, q driver.Query) (driver.Rows, error) {
	rows, err := tx.QueryContext(ctx, q.SQL, bindings(q.Args)...)
	if err != nil {
		return driver.Rows{}, classifyRead(err)
	}

	defer rows.Close()

	out, err := collect(rows)
	if err != nil {
		return driver.Rows{}, err
	}

	return out, nil
}

// collect renders every row a result carries as text, which is the only shape this driver returns.
func collect(rows *sql.Rows) (driver.Rows, error) {
	columns, err := rows.Columns()
	if err != nil {
		return driver.Rows{}, fmt.Errorf("%w: naming the columns: %w", ErrQuery, err)
	}

	kinds, err := columnKinds(rows, columns)
	if err != nil {
		return driver.Rows{}, err
	}

	out := driver.Rows{Columns: columns, Values: [][]*string{}}

	for rows.Next() {
		row, err := textRow(rows, columns, kinds)
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

// columnKinds names each column's server type, which is what tells this driver bytes from text.
func columnKinds(rows *sql.Rows, columns []string) ([]string, error) {
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, fmt.Errorf("%w: reading the column types: %w", ErrQuery, err)
	}

	if len(types) != len(columns) {
		return nil, fmt.Errorf("%w: the result names %d columns and reports %d types", ErrQuery,
			len(columns), len(types))
	}

	kinds := make([]string, len(types))
	for i, t := range types {
		kinds[i] = t.DatabaseTypeName()
	}

	return kinds, nil
}

// textRow scans one row as whatever the server sent and renders each value as text.
func textRow(rows *sql.Rows, columns, kinds []string) ([]*string, error) {
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
		text, err := textValue(columns[i], kinds[i], value)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrQuery, err)
		}

		row[i] = text
	}

	return row, nil
}

// textValue renders one value the way docs/protocol.md §5 carries it, and nil for a null.
func textValue(column, kind string, value any) (*string, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case int64:
		return text(strconv.FormatInt(v, 10)), nil
	case uint64:
		return text(strconv.FormatUint(v, 10)), nil
	case float64:
		return text(strconv.FormatFloat(v, 'g', -1, 64)), nil
	case []byte:
		return bytesValue(column, kind, v)
	case string:
		return checkUTF8(column, v)
	default:
		return nil, &NotTextError{Column: column, Detail: describeValue(value)}
	}
}

// bytesValue renders the bytes a column returned, as hex where the column is a binary one.
func bytesValue(column, kind string, value []byte) (*string, error) {
	if binaryTypes[kind] {
		return text(hexPrefix + hex.EncodeToString(value)), nil
	}

	return checkUTF8(column, string(value))
}

// checkUTF8 refuses a text value no JSON string could carry without being silently rewritten.
func checkUTF8(column, value string) (*string, error) {
	if !utf8.ValidString(value) {
		return nil, &NotTextError{Column: column, Detail: fmt.Sprintf("the server returned %d bytes "+
			"of text that are not valid UTF-8, which a binary column carries legitimately and a "+
			"utf8mb4 one should not", len(value))}
	}

	return text(value), nil
}

// text takes the address of a rendered value, since a null is the nil this type distinguishes.
func text(value string) *string {
	return &value
}

// classifyRead reports a write the server refused as the refusal it is, rather than as a failure.
func classifyRead(err error) error {
	if hasNumber(err, readOnlyRefused) {
		return fmt.Errorf("%w: %w: %w", ErrQuery, driver.ErrNotReadOnly, err)
	}

	return fmt.Errorf("%w: %w", ErrQuery, err)
}

// hasNumber reports whether a failure is the server raising one particular error number.
func hasNumber(err error, number uint16) bool {
	var reported *lib.MySQLError

	return errors.As(err, &reported) && reported.Number == number
}

// bindings renders a migration's textual parameters, which the server binds positionally as text.
func bindings(args []string) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		out[i] = arg
	}

	return out
}
