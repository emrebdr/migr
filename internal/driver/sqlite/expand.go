package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// shadowPrefix names the table a rebuild holds its rows in, which exists only inside one transaction.
const shadowPrefix = "migr_rebuild_"

// deferKeys is the first statement of every rebuild, and the only thing holding the copy together.
const deferKeys = `PRAGMA defer_foreign_keys = 1;`

// Expand returns the statements one emulated operation becomes against this database.
func (c *sqliteConn) Expand(ctx context.Context, op ir.Operation) ([]driver.Statement, error) {
	table, err := rebuildTarget(op)
	if err != nil {
		return nil, err
	}

	found, err := readExisting(ctx, c, table)
	if err != nil {
		return nil, err
	}

	changed, err := modify(table, found.definition, op)
	if err != nil {
		return nil, err
	}

	if err := checkShadow(ctx, c, table); err != nil {
		return nil, err
	}

	return rebuild(table, found, changed), nil
}

// rebuildTarget names the table an emulated operation changes, refusing anything else entirely.
func rebuildTarget(op ir.Operation) (string, error) {
	switch o := op.(type) {
	case ir.AlterColumn:
		return o.Table, nil
	case ir.AddCheck:
		return o.Table, nil
	case ir.DropCheck:
		return o.Table, nil
	case ir.AddForeignKey:
		return o.Table, nil
	case ir.DropForeignKey:
		return o.Table, nil
	case ir.AddPrimaryKey:
		return o.Table, nil
	case ir.DropPrimaryKey:
		return o.Table, nil
	default:
		return "", fmt.Errorf("%w: %s is not an operation this driver rebuilds a table for, and "+
			"only the ones it declares emulated reach here", ErrRebuild, op.Kind())
	}
}

// checkShadow refuses a rebuild whose working table is a name this database already holds.
func checkShadow(ctx context.Context, c *sqliteConn, table string) error {
	shadow := shadowPrefix + table

	rows, err := c.scan(ctx, tableQuery, shadow)
	if err != nil {
		return err
	}

	if len(rows.Values) == 0 {
		return nil
	}

	return &RebuildError{Table: table, Detail: fmt.Sprintf("a rebuild copies its rows through %q "+
		"and this database already has a table of that name, which a rebuild would drop", shadow)}
}

// rebuild renders the statements that replace a table with a changed definition, keeping its rows.
func rebuild(table string, found existing, changed tableDefinition) []driver.Statement {
	shadow := shadowPrefix + table
	columns := quoteAll(found.columns)

	statements := []string{
		deferKeys,
		changed.render(shadow) + terminator,
		copyRows(shadow, table, columns),
		"DROP TABLE " + quote(table) + terminator,
		changed.render(table) + terminator,
	}

	statements = append(statements, found.indexes...)
	statements = append(statements, copyRows(table, shadow, columns),
		"DROP TABLE "+quote(shadow)+terminator)
	statements = append(statements, found.triggers...)

	return compiled(statements)
}

// copyRows renders the copy between two tables, naming every column so nothing rides on their order.
func copyRows(into, from, columns string) string {
	return "INSERT INTO " + quote(into) + " (" + columns + ") SELECT " + columns +
		" FROM " + quote(from) + terminator
}

// compiled renders each statement as one this engine runs, terminating the ones that carry none.
func compiled(statements []string) []driver.Statement {
	out := make([]driver.Statement, 0, len(statements))

	for _, sql := range statements {
		if !strings.HasSuffix(strings.TrimRight(sql, " \t\r\n"), terminator) {
			sql += terminator
		}

		out = append(out, driver.Statement{SQL: sql})
	}

	return out
}
