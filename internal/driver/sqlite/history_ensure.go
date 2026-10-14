package sqlite

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

// kindQuery reports what kind of object holds a name, and no row when nothing holds it.
//
// COLLATE NOCASE is not decoration: SQLite folds ASCII case in an identifier but compares
// sqlite_schema.name as BINARY, so a history table created as MixedCase would report absent on every
// run and Ensure would try to create it forever. Measured, and PostgreSQL's quote_ident fix in a
// second dialect. ORDER BY keeps the answer deterministic where two objects fold to one name.
const kindQuery = `SELECT type FROM sqlite_schema WHERE name = ? COLLATE NOCASE ` +
	`ORDER BY type LIMIT 1`

// historyColumnQuery lists the columns a table has, so an unusable one is named before a write.
//
// pragma_table_info rather than table_xinfo, and that is the whole check working: table_info omits a
// generated column, which is one this build cannot INSERT into -- so a generated error column is
// reported missing, where xinfo would call a table that fails every write the right shape. Measured.
const historyColumnQuery = `SELECT name FROM pragma_table_info(?)`

// ordinaryTable is the sqlite_schema type of a plain table, which is the only kind this store uses.
const ordinaryTable = "table"

// Ensure creates the history table and its checkpoint table if this database does not have them yet.
func (h sqliteHistory) Ensure(ctx context.Context) error {
	if err := h.ensureHistory(ctx); err != nil {
		return err
	}

	return h.ensureProgress(ctx)
}

// ensureHistory creates the history table, or checks the one already there is one this build can write.
func (h sqliteHistory) ensureHistory(ctx context.Context) error {
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

	return h.checkColumns(ctx)
}

// kind reports what holds the history name, and whether anything holds it at all.
func (h sqliteHistory) kind(ctx context.Context) (string, bool, error) {
	return h.kindOf(ctx, h.table)
}

// kindOf reports what holds a name, and whether anything holds it at all.
func (h sqliteHistory) kindOf(ctx context.Context, name string) (string, bool, error) {
	var kind string

	err := h.session.runner().QueryRowContext(ctx, kindQuery, name).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}

	if err != nil {
		return "", false, h.wrap("looking for the table", err)
	}

	return kind, true, nil
}

// requireTable refuses a database whose history table is absent or is not a table at all.
func (h sqliteHistory) requireTable(ctx context.Context) error {
	kind, found, err := h.kind(ctx)
	if err != nil {
		return err
	}

	if !found {
		return h.missingTable()
	}

	return h.checkKind(kind)
}

// missingTable reports a table nothing created, which reading as "no runs yet" would re-run everything.
func (h sqliteHistory) missingTable() error {
	return &HistoryError{Table: h.table, Err: history.ErrNoTable, Detail: fmt.Sprintf(
		"%v. Ensure creates it; a database that has one under another name is a history.table that "+
			"does not match the one migrations were recorded under", history.ErrNoTable)}
}

// checkKind refuses a history name held by a view, an index, or a trigger rather than by a table.
func (h sqliteHistory) checkKind(kind string) error {
	if kind == ordinaryTable {
		return nil
	}

	return &HistoryError{Table: h.table, Err: ErrHistoryNotATable, Detail: fmt.Sprintf(
		"%v: the name is held by a %s", ErrHistoryNotATable, kind)}
}

// create compiles the neutral schema and runs every statement of it as one unit.
func (h sqliteHistory) create(ctx context.Context) error {
	stmts, err := h.compileSchema(history.Schema(h.table))
	if err != nil {
		return err
	}

	return h.session.inTransaction(ctx, func(ctx context.Context) error {
		return h.run(ctx, stmts)
	})
}

// compileSchema turns a neutral schema into statements, before any of them runs.
func (h sqliteHistory) compileSchema(ops ir.Operations) ([]driver.Statement, error) {
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

// run executes the schema statements, reporting a concurrent create as the race it is.
func (h sqliteHistory) run(ctx context.Context, stmts []driver.Statement) error {
	for _, stmt := range stmts {
		if err := h.session.Exec(ctx, stmt); err != nil {
			return h.explainCreate(ctx, err)
		}
	}

	return nil
}

// explainCreate asks whether another process won the race, SQLite having no code that says so.
func (h sqliteHistory) explainCreate(ctx context.Context, cause error) error {
	if _, found, err := h.kind(ctx); err == nil && found {
		return h.race(cause)
	}

	return h.wrap("creating the table", cause)
}

// race reports a table another process created between this session's check and its CREATE TABLE.
func (h sqliteHistory) race(cause error) error {
	return &HistoryError{Table: h.table, Err: errors.Join(ErrHistoryRace, cause), Detail: fmt.Sprintf(
		"%v, between this session's existence check and its CREATE TABLE. Hold the migration lock "+
			"around Ensure: it is a lock on a file beside the database and needs no table to exist, "+
			"so it is available on a database with no history at all%s", ErrHistoryRace,
		h.describeLock())}
}

// describeLock names the other diagnosis, for a race that happened under the lock rather than without it.
func (h sqliteHistory) describeLock() string {
	if !h.session.locked {
		return ""
	}

	return ". This session did hold the lock, so the other process locked a different file -- which " +
		"means the two migr.yaml files point at databases this one reaches by different paths"
}

// checkColumns reports a history table missing anything this build writes, extras being fine.
func (h sqliteHistory) checkColumns(ctx context.Context) error {
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
func (h sqliteHistory) columnNames(ctx context.Context) (map[string]bool, error) {
	rows, err := h.session.runner().QueryContext(ctx, historyColumnQuery, h.table)
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

// readColumnNames collects the column names one pragma returned.
func (h sqliteHistory) readColumnNames(rows *sql.Rows) (map[string]bool, error) {
	present := make(map[string]bool)

	for rows.Next() {
		var name string

		if err := rows.Scan(&name); err != nil {
			return nil, h.wrap("reading a column name", err)
		}

		present[name] = true
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
