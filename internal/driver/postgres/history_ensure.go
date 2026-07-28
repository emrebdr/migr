package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// relkindQuery reports what kind of relation holds a name, and no row when nothing holds it.
//
// quote_ident is not decoration: to_regclass folds an unquoted name to lower case, so a history
// table spelled MixedCase would report absent on every run and Ensure would try to create it
// forever. The same fix applySearchPath needs, for the same reason.
const relkindQuery = `SELECT relkind::text FROM pg_class WHERE oid = to_regclass(quote_ident($1))`

// columnNamesQuery lists the columns a relation has, so an unusable table is named before a write.
const columnNamesQuery = `
SELECT a.attname
  FROM pg_attribute a
 WHERE a.attrelid = to_regclass(quote_ident($1)) AND a.attnum > 0 AND NOT a.attisdropped`

// ordinaryTable is the relkind of a plain table, which is what this driver creates.
const ordinaryTable = "r"

// partitionedTable is the relkind of a partitioned table, which an operator may reasonably prefer
// for a log nothing prunes.
const partitionedTable = "p"

// duplicateTable is the SQLSTATE PostgreSQL raises for a relation that already exists.
const duplicateTable = "42P07"

// uniqueViolation is the SQLSTATE a losing concurrent CREATE raises, on a pg_catalog index rather
// than on anything migr declared -- so it means a race here and a duplicate run on a write, and is
// classified per call site rather than from one table.
const uniqueViolation = "23505"

// checkHistoryTable reports why PostgreSQL could not carry the configured history table name.
func checkHistoryTable(table string) error {
	if problem := checkIdentifier("history.table", table); problem != "" {
		return &HistoryError{Table: table, Detail: problem}
	}

	for _, op := range history.Schema(table) {
		if err := op.Validate(); err != nil {
			return &HistoryError{Table: table, Detail: err.Error(), Err: err}
		}

		if _, err := compile(op); err != nil {
			return &HistoryError{Table: table, Detail: describeDerived(table, err), Err: err}
		}
	}

	return nil
}

// describeDerived names the derived object a table name is too long for, not just the table.
func describeDerived(table string, err error) string {
	return fmt.Sprintf("the history schema cannot be compiled for a table named %q: %v. Note that "+
		"the index is named after the table, so the usable length is shorter than an identifier's",
		table, err)
}

// Ensure creates the history table if this database does not have it yet.
func (h pgHistory) Ensure(ctx context.Context) error {
	kind, found, err := h.relkind(ctx)
	if err != nil {
		return err
	}

	if !found {
		return h.create(ctx)
	}

	if kind != ordinaryTable && kind != partitionedTable {
		return &HistoryError{Table: h.table, Err: ErrHistoryNotATable, Detail: fmt.Sprintf(
			"%v: the name is held by a relation of kind %q", ErrHistoryNotATable, kind)}
	}

	return h.checkColumns(ctx)
}

// relkind reports what holds the history name, and whether anything holds it at all.
func (h pgHistory) relkind(ctx context.Context) (string, bool, error) {
	var kind string

	err := h.session.runner().QueryRow(ctx, relkindQuery, h.table).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}

	if err != nil {
		return "", false, h.wrap("looking for the table", err)
	}

	return kind, true, nil
}

// create compiles the neutral schema and runs every statement of it as one unit.
func (h pgHistory) create(ctx context.Context) error {
	stmts, err := h.compileSchema()
	if err != nil {
		return err
	}

	return h.session.inTransaction(ctx, func(ctx context.Context) error {
		return h.run(ctx, stmts)
	})
}

// compileSchema turns the neutral history schema into statements, before any of them runs.
func (h pgHistory) compileSchema() ([]driver.Statement, error) {
	var stmts []driver.Statement

	for _, op := range history.Schema(h.table) {
		compiled, err := compile(op)
		if err != nil {
			return nil, h.wrap("compiling the history schema", err)
		}

		stmts = append(stmts, compiled...)
	}

	return stmts, nil
}

// run executes the schema statements, reporting a concurrent create as the race it is.
func (h pgHistory) run(ctx context.Context, stmts []driver.Statement) error {
	for _, stmt := range stmts {
		if err := h.session.Exec(ctx, stmt); err != nil {
			return h.classifyCreate(err)
		}
	}

	return nil
}

// classifyCreate separates another process winning the race from every other way a create fails.
func (h pgHistory) classifyCreate(err error) error {
	if !hasCode(err, duplicateTable) && !hasCode(err, uniqueViolation) {
		return h.wrap("creating the table", err)
	}

	return &HistoryError{Table: h.table, Err: errors.Join(ErrHistoryRace, err), Detail: fmt.Sprintf(
		"%v, between this session's existence check and its CREATE TABLE. Hold the migration lock "+
			"around Ensure: its advisory key is derived from the schema and table names and needs "+
			"no table to exist, so it is available on a database with no history at all%s",
		ErrHistoryRace, h.describeLock())}
}

// describeLock names the other diagnosis, for a race that happened under the lock rather than without it.
func (h pgHistory) describeLock() string {
	if !h.session.locked {
		return ""
	}

	return ". This session did hold the lock, so the other process locked on a different key -- " +
		"which means the two migr.yaml files disagree about the schema or the history table"
}

// checkColumns reports a history table missing anything this build writes, extras being fine.
func (h pgHistory) checkColumns(ctx context.Context) error {
	present, err := h.columnNames(ctx)
	if err != nil {
		return err
	}

	missing := missingColumns(declaredColumns(h.table), present)
	if len(missing) == 0 {
		return nil
	}

	return &HistoryError{Table: h.table, Err: ErrHistoryShape, Detail: fmt.Sprintf(
		"%v: it has no %s. Every column this build writes must be present; extra ones are fine",
		ErrHistoryShape, strings.Join(missing, ", "))}
}

// columnNames reads the columns a relation currently has.
func (h pgHistory) columnNames(ctx context.Context) (map[string]bool, error) {
	rows, err := h.session.runner().Query(ctx, columnNamesQuery, h.table)
	if err != nil {
		return nil, h.wrap("reading the table's columns", err)
	}

	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, h.wrap("reading the table's columns", err)
	}

	present := make(map[string]bool, len(names))
	for _, name := range names {
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
