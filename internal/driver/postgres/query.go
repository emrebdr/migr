package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/driver"
)

// textResults asks for every value in the server's own text output rather than in binary.
var textResults = pgx.QueryResultFormats{}

// Query runs a migration's own read inside a read-only transaction, so the server refuses a write.
func (c *pgConn) Query(ctx context.Context, q driver.Query) (out driver.Rows, err error) {
	defer func() { err = stoppedBy(ctx, err) }()

	if err := c.outsideTransaction(ErrQuery); err != nil {
		return driver.Rows{}, err
	}

	tx, err := c.conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return driver.Rows{}, fmt.Errorf("%w: opening the read-only transaction: %w", ErrQuery, err)
	}

	defer func() { err = errors.Join(err, discard(ctx, tx)) }()

	return readText(ctx, tx, q)
}

// Perform runs a migration's own statement, outside the transaction its operations will run in.
func (c *pgConn) Perform(ctx context.Context, q driver.Query) (int64, error) {
	if err := c.outsideTransaction(ErrPerform); err != nil {
		return 0, err
	}

	tag, err := c.conn.Exec(ctx, q.SQL, bindings(q.Args)...)
	if err != nil {
		return 0, stoppedBy(ctx, fmt.Errorf("%w: %w", ErrPerform, err))
	}

	return tag.RowsAffected(), nil
}

// outsideTransaction refuses a migration's own SQL on a session that is already inside one.
func (c *pgConn) outsideTransaction(sentinel error) error {
	if err := c.checkIdle(); err != nil {
		return fmt.Errorf("%w: %w", sentinel, err)
	}

	return nil
}

// readText runs the statement asking for text output and takes the server's bytes verbatim.
func readText(ctx context.Context, tx pgx.Tx, q driver.Query) (driver.Rows, error) {
	rows, err := tx.Query(ctx, q.SQL, arguments(q.Args)...)
	if err != nil {
		return driver.Rows{}, classifyRead(err)
	}

	defer rows.Close()

	out := driver.Rows{Columns: columnsOf(rows), Values: [][]*string{}}

	for rows.Next() {
		out.Values = append(out.Values, textRow(rows.RawValues()))
	}

	if err := rows.Err(); err != nil {
		return driver.Rows{}, classifyRead(err)
	}

	return out, nil
}

// readOnlySQLTransaction is the SQLSTATE PostgreSQL raises for a write in a read-only transaction.
const readOnlySQLTransaction = "25006"

// classifyRead reports a write the server refused as the refusal it is, rather than as a failure.
func classifyRead(err error) error {
	var refused *pgconn.PgError
	if errors.As(err, &refused) && refused.Code == readOnlySQLTransaction {
		return fmt.Errorf("%w: %w: %s", ErrQuery, driver.ErrNotReadOnly, refused.Message)
	}

	return fmt.Errorf("%w: %w", ErrQuery, err)
}

// arguments renders a migration's textual parameters, behind the request for text output.
func arguments(args []string) []any {
	out := make([]any, 0, len(args)+1)
	out = append(out, textResults)

	return append(out, bindings(args)...)
}

// bindings renders a migration's textual parameters for a call that takes no query options.
func bindings(args []string) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		out[i] = arg
	}

	return out
}

// columnsOf names each column, in the order the statement selected them.
func columnsOf(rows pgx.Rows) []string {
	fields := rows.FieldDescriptions()

	names := make([]string, len(fields))
	for i, field := range fields {
		names[i] = field.Name
	}

	return names
}

// textRow copies one row's bytes, which pgx reuses, keeping a SQL null distinct from an empty string.
func textRow(raw [][]byte) []*string {
	row := make([]*string, len(raw))

	for i, value := range raw {
		if value == nil {
			continue
		}

		text := string(value)
		row[i] = &text
	}

	return row
}

// discard rolls back a read on a context that outlives whatever canceled the read itself.
func discard(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Rollback(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("%w: discarding the read-only transaction: %w", ErrQuery, err)
	}

	return nil
}
