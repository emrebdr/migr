package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// kindQuery reports what holds a table name in this database, and the spelling the catalog holds.
//
// The stored name comes back so it can be compared byte for byte here, because the collation of
// information_schema.TABLES.TABLE_NAME is not the same on the two servers -- utf8mb3_bin on 8.0.46
// and utf8mb3_general_ci on 11.4.13, measured -- and a case-insensitive comparison on a server that
// stores names case-sensitively could answer about a different table. Comparing in Go makes the
// answer independent of whatever collation the comparison happened to resolve to.
const kindQuery = "SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES " +
	"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?"

// historyColumnQuery lists the columns a table has that this build could write to.
//
// GENERATION_EXPRESSION rather than EXTRA, and that is the whole check working. A generated column
// is one nothing can INSERT into, so it must count as absent -- but MySQL 8 sets EXTRA to
// "DEFAULT_GENERATED" for any column with an expression default, so filtering EXTRA on "GENERATED"
// would call five perfectly writable columns missing. The generation expression is empty on MySQL
// and null on MariaDB for an ordinary column, which is what the coalesce is for. Measured on both.
const historyColumnQuery = "SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS " +
	"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND coalesce(GENERATION_EXPRESSION, '') = ''"

// ordinaryTable is the TABLE_TYPE of a plain table, which is the only kind this store uses.
const ordinaryTable = "BASE TABLE"

// Ensure creates the history table if this database does not have it yet, or finishes a partial one.
func (h mysqlHistory) Ensure(ctx context.Context) error {
	kind, found, err := h.kind(ctx)
	if err != nil {
		return err
	}

	if !found {
		return h.create(ctx)
	}

	if err := h.checkKind(kind); err != nil {
		return err
	}

	if err := h.checkColumns(ctx); err != nil {
		return err
	}

	return h.complete(ctx)
}

// create runs every statement of the neutral schema on a database that holds none of it.
func (h mysqlHistory) create(ctx context.Context) error {
	stmts, err := h.compile(history.Schema(h.table))
	if err != nil {
		return err
	}

	for _, stmt := range stmts {
		if err := h.session.Exec(ctx, stmt); err != nil {
			return h.explainCreate(ctx, err)
		}
	}

	return nil
}

// complete runs the schema statements after the table, finishing an installation only it survived.
func (h mysqlHistory) complete(ctx context.Context) error {
	stmts, err := h.compile(afterTable(h.table))
	if err != nil {
		return err
	}

	for _, stmt := range stmts {
		if err := h.session.Exec(ctx, stmt); err != nil && !hasNumber(err, errDuplicateKeyName) {
			return h.wrap("finishing the table", err)
		}
	}

	return nil
}

// afterTable returns the schema operations that are not the table, which are the repeatable ones.
func afterTable(table string) ir.Operations {
	var rest ir.Operations

	for _, op := range history.Schema(table) {
		if _, isTable := op.(ir.CreateTable); isTable {
			continue
		}

		rest = append(rest, op)
	}

	return rest
}

// compile turns neutral history operations into statements, before any of them runs.
func (h mysqlHistory) compile(ops ir.Operations) ([]driver.Statement, error) {
	var stmts []driver.Statement

	for _, op := range ops {
		compiled, err := compile(op)
		if err != nil {
			return nil, h.wrap("compiling the history schema", err)
		}

		stmts = append(stmts, compiled...)
	}

	return stmts, nil
}

// explainCreate reports a create that failed, naming a table another process got in first with.
func (h mysqlHistory) explainCreate(ctx context.Context, cause error) error {
	if hasNumber(cause, errTableExists) {
		return h.race(cause)
	}

	if _, found, err := h.kind(ctx); err == nil && found {
		return h.race(cause)
	}

	return h.wrap("creating the table", cause)
}

// race reports a table another process created between this session's check and its CREATE TABLE.
func (h mysqlHistory) race(cause error) error {
	return &HistoryError{Table: h.table, Err: errors.Join(ErrHistoryRace, cause), Detail: fmt.Sprintf(
		"%v, between this session's existence check and its CREATE TABLE. Hold the migration lock "+
			"around Ensure", ErrHistoryRace)}
}

// kind reports what holds the history name, and whether anything holds it at all.
func (h mysqlHistory) kind(ctx context.Context) (string, bool, error) {
	rows, err := h.session.runner().QueryContext(ctx, kindQuery, h.stored)
	if err != nil {
		return "", false, h.wrap("looking for the table", err)
	}

	defer rows.Close()

	kind, found, err := h.readKind(rows)
	if err != nil {
		return "", false, err
	}

	if err := rows.Err(); err != nil {
		return "", false, h.wrap("looking for the table", err)
	}

	return kind, found, nil
}

// readKind finds the row whose stored name is this table's, the comparison being Go's and not a collation's.
func (h mysqlHistory) readKind(rows *sql.Rows) (string, bool, error) {
	for rows.Next() {
		var stored, kind string

		if err := rows.Scan(&stored, &kind); err != nil {
			return "", false, h.wrap("reading what holds the name", err)
		}

		if stored == h.stored {
			return kind, true, nil
		}
	}

	return "", false, nil
}

// requireTable refuses a history name held by a view, absence being the failure's own answer (doc.go).
func (h mysqlHistory) requireTable(ctx context.Context) error {
	kind, found, err := h.kind(ctx)
	if err != nil {
		return err
	}

	if !found {
		return nil
	}

	return h.checkKind(kind)
}

// missingTable reports a table nothing created, which reading as "no runs yet" would re-run everything.
func (h mysqlHistory) missingTable() error {
	return &HistoryError{Table: h.table, Err: history.ErrNoTable, Detail: fmt.Sprintf(
		"%v. Ensure creates it; a database that has one under another name is a history.table that "+
			"does not match the one migrations were recorded under", history.ErrNoTable)}
}

// checkKind refuses a history name held by a view or a sequence rather than by a table.
func (h mysqlHistory) checkKind(kind string) error {
	if kind == ordinaryTable {
		return nil
	}

	return &HistoryError{Table: h.table, Err: ErrHistoryNotATable, Detail: fmt.Sprintf(
		"%v: the catalog reports it as %s", ErrHistoryNotATable, kind)}
}

// checkColumns reports a history table missing anything this build writes, extras being fine.
func (h mysqlHistory) checkColumns(ctx context.Context) error {
	present, err := h.columnNames(ctx)
	if err != nil {
		return err
	}

	missing := missingColumns(declaredColumns(h.table), present)
	if len(missing) == 0 {
		return nil
	}

	return &HistoryError{Table: h.table, Err: ErrHistoryShape, Detail: fmt.Sprintf(
		"%v: it has no %s. Every column this build writes must be present; extra ones are fine, and "+
			"a generated one counts as absent because nothing can INSERT into it",
		ErrHistoryShape, strings.Join(missing, ", "))}
}

// columnNames reads the columns a table currently has that this build could write to.
func (h mysqlHistory) columnNames(ctx context.Context) (map[string]bool, error) {
	rows, err := h.session.runner().QueryContext(ctx, historyColumnQuery, h.stored)
	if err != nil {
		return nil, h.wrap("reading the table's columns", err)
	}

	defer rows.Close()

	present, err := h.readColumnNames(rows)
	if err != nil {
		return nil, err
	}

	if err := rows.Err(); err != nil {
		return nil, h.wrap("reading the table's columns", err)
	}

	return present, nil
}

// readColumnNames collects the column names belonging to the table whose stored spelling is this one's.
func (h mysqlHistory) readColumnNames(rows *sql.Rows) (map[string]bool, error) {
	present := make(map[string]bool)

	for rows.Next() {
		var stored, name string

		if err := rows.Scan(&stored, &name); err != nil {
			return nil, h.wrap("reading a column name", err)
		}

		if stored == h.stored {
			present[name] = true
		}
	}

	return present, nil
}

// declaredColumns names every column the neutral schema declares, in the order it declares them.
func declaredColumns(table string) []string {
	for _, op := range history.Schema(table) {
		created, ok := op.(ir.CreateTable)
		if !ok {
			continue
		}

		names := make([]string, len(created.Columns))
		for i, column := range created.Columns {
			names[i] = column.Name
		}

		return names
	}

	return nil
}

// missingColumns lists the declared columns a table does not have, in declaration order.
func missingColumns(declared []string, present map[string]bool) []string {
	var missing []string

	for _, name := range declared {
		if !present[name] {
			missing = append(missing, name)
		}
	}

	return missing
}
