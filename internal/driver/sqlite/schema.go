package sqlite

import (
	"context"
	"fmt"

	"github.com/emrebdr/migr/internal/driver"
)

// The reads a rebuild makes before it changes anything, all of them against sqlite_schema.
const (
	tableQuery = `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`

	attachedQuery = `SELECT type, sql FROM sqlite_schema WHERE tbl_name = ? AND sql IS NOT NULL ` +
		`AND type IN ('index', 'trigger') ORDER BY type, name`

	columnQuery = `SELECT name FROM pragma_table_info(?)`

	childQuery = `SELECT m.name, f."on_delete" FROM sqlite_schema m ` +
		`JOIN pragma_foreign_key_list(m.name) f ` +
		`WHERE m.type = 'table' AND f."table" = ? ORDER BY m.name`
)

// mutatingActions are the referential actions that change a child's rows when a parent row goes.
var mutatingActions = map[string]bool{"CASCADE": true, "SET NULL": true, "SET DEFAULT": true}

// existing is everything a rebuild has to know about a table before it replaces it.
type existing struct {
	// definition is the CREATE TABLE statement SQLite recorded, split into its parts.
	definition tableDefinition
	// columns are the table's column names, in declaration order, for the copy.
	columns []string
	// indexes are the CREATE INDEX statements, restored before the rows come back (see doc.go).
	indexes []string
	// triggers are the CREATE TRIGGER statements, restored after the rows come back (see doc.go).
	triggers []string
}

// readExisting gathers what a rebuild of one table needs, refusing a table it cannot rebuild.
func readExisting(ctx context.Context, c *sqliteConn, table string) (existing, error) {
	sql, err := readTable(ctx, c, table)
	if err != nil {
		return existing{}, err
	}

	definition, err := splitTable(table, sql)
	if err != nil {
		return existing{}, err
	}

	if err := checkChildren(ctx, c, table); err != nil {
		return existing{}, err
	}

	columns, err := readColumns(ctx, c, table)
	if err != nil {
		return existing{}, err
	}

	indexes, triggers, err := readAttached(ctx, c, table)
	if err != nil {
		return existing{}, err
	}

	return existing{definition: definition, columns: columns,
		indexes: indexes, triggers: triggers}, nil
}

// readTable returns the CREATE TABLE statement SQLite recorded, refusing a table it has none for.
func readTable(ctx context.Context, c *sqliteConn, table string) (string, error) {
	rows, err := c.scan(ctx, tableQuery, table)
	if err != nil {
		return "", err
	}

	if len(rows.Values) == 0 {
		return "", &RebuildError{Table: table, Detail: "this database has no such table, so there " +
			"is no definition to rebuild it from"}
	}

	if rows.Values[0][0] == nil {
		return "", &RebuildError{Table: table, Detail: "SQLite records no CREATE statement for it, " +
			"which is true of its own internal tables and of nothing a migration created"}
	}

	return *rows.Values[0][0], nil
}

// checkChildren refuses a rebuild that would fire a referential action on another table's rows.
func checkChildren(ctx context.Context, c *sqliteConn, table string) error {
	rows, err := c.scan(ctx, childQuery, table)
	if err != nil {
		return err
	}

	for _, row := range rows.Values {
		child, action := valueOf(row[0]), valueOf(row[1])
		if !mutatingActions[action] {
			continue
		}

		return &RebuildError{Table: table, Detail: fmt.Sprintf("table %q references it ON DELETE "+
			"%s, and a rebuild drops this table: SQLite fires that action while the rows are gone, "+
			"so %q would lose rows a migration never asked it to lose. PRAGMA defer_foreign_keys "+
			"holds the *check* until commit and does not hold the action -- measured. Drop the "+
			"foreign key, rebuild, and add it back", child, action, child)}
	}

	return nil
}

// readColumns returns the table's column names in declaration order, which the copy names.
func readColumns(ctx context.Context, c *sqliteConn, table string) ([]string, error) {
	rows, err := c.scan(ctx, columnQuery, table)
	if err != nil {
		return nil, err
	}

	columns := make([]string, 0, len(rows.Values))
	for _, row := range rows.Values {
		columns = append(columns, valueOf(row[0]))
	}

	if len(columns) == 0 {
		return nil, &RebuildError{Table: table, Detail: "it reports no columns at all"}
	}

	return columns, nil
}

// readAttached returns the indexes and the triggers a table carries, which dropping it takes.
func readAttached(ctx context.Context, c *sqliteConn, table string) ([]string, []string, error) {
	rows, err := c.scan(ctx, attachedQuery, table)
	if err != nil {
		return nil, nil, err
	}

	var indexes, triggers []string

	for _, row := range rows.Values {
		if valueOf(row[0]) == "index" {
			indexes = append(indexes, valueOf(row[1]))

			continue
		}

		triggers = append(triggers, valueOf(row[1]))
	}

	return indexes, triggers, nil
}

// valueOf renders a value a read returned, treating a null as the empty string it never is here.
func valueOf(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

// scan runs one of this file's queries on the pinned connection, outside the read-only path.
func (c *sqliteConn) scan(ctx context.Context, sql string, args ...any) (driver.Rows, error) {
	rows, err := c.runner().QueryContext(ctx, sql, args...)
	if err != nil {
		return driver.Rows{}, fmt.Errorf("%w: %w", ErrRebuild, err)
	}

	defer func() { _ = rows.Close() }()

	return collect(rows)
}
