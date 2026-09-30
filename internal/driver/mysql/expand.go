package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// columnQuery reads everything a redefinition must restate, and the two fields that say it cannot.
//
// COLUMN_TYPE rather than DATA_TYPE, because a redefinition needs varchar(255) and not varchar. The
// coalesce on the two nullable columns is M9-T1's finding again: MySQL reports an ordinary column's
// generation expression as the empty string and MariaDB as null.
const columnQuery = "SELECT COLUMN_TYPE, DATA_TYPE, IS_NULLABLE, EXTRA, " +
	"coalesce(COLLATION_NAME, ''), COLUMN_DEFAULT " +
	"FROM information_schema.COLUMNS " +
	"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?"

// inlineCheckQuery counts the checks MariaDB records under a column's own name, which are its own.
//
// MariaDB writes a column's check inside the column definition and records it with the column's name
// as the constraint's, so a MODIFY drops it: measured, a longtext's json_valid check was gone
// afterwards and invalid JSON was accepted, and a hand-written CHECK (n > 0) went the same way and
// took -1. MySQL holds every check apart from the column, names it after the table, and a MODIFY
// keeps it -- error 3819 still refused -1 after one. So this is asked of MariaDB alone, which is also
// the only variant whose CHECK_CONSTRAINTS has a TABLE_NAME column at all.
const inlineCheckQuery = "SELECT count(*) FROM information_schema.CHECK_CONSTRAINTS " +
	"WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = ? AND CONSTRAINT_NAME = ?"

// defaultGenerated is what MySQL puts in EXTRA for any column with an expression default.
const defaultGenerated = "DEFAULT_GENERATED"

// nullableYes is how the catalog spells a column that accepts null.
const nullableYes = "YES"

// noDefault is what MariaDB records for a nullable column nobody gave a default, quoted by nothing.
const noDefault = "NULL"

// expressionOnly names the type families MySQL takes no bare literal default for, error 1101.
//
// Matched on suffix because the family is what decides it and the width does not: tinytext through
// longtext, tinyblob through longblob. json and geometry are their own names. MariaDB's json is a
// longtext, so it is caught by the first.
var expressionOnly = []string{"text", "blob", "json", "geometry"}

// existingColumn is what the catalog holds about a column a redefinition is about to restate.
type existingColumn struct {
	// declared is the server's own rendering of the type, which is faithful by construction.
	declared string
	// data is the type without its length, which is what decides the legal default syntax.
	data string
	// nullable is whether the column accepts null today.
	nullable bool
	// extra carries every attribute a redefinition would silently drop, and is empty otherwise.
	extra string
	// collation is what the column compares under, empty for a type that carries none.
	collation string
	// hasDefault is whether the column carries a default a redefinition would have to restate.
	hasDefault bool
}

// Expand returns the statements one emulated operation becomes against this database.
func (c *mysqlConn) Expand(ctx context.Context, op ir.Operation) ([]driver.Statement, error) {
	altered, ok := op.(ir.AlterColumn)
	if !ok {
		return nil, fmt.Errorf("%w: %s is not an operation this driver compiles against the "+
			"database, and only the ones it declares emulated reach here", ErrRedefine, op.Kind())
	}

	found, err := c.readColumn(ctx, altered.Table, altered.Column)
	if err != nil {
		return nil, err
	}

	if err := c.checkRestatable(ctx, altered, found); err != nil {
		return nil, err
	}

	restated, err := restate(altered, found)
	if err != nil {
		return nil, err
	}

	return one(restated.render()), nil
}

// readColumn reads what the catalog holds about the column an operation names.
func (c *mysqlConn) readColumn(ctx context.Context, table, column string) (existingColumn, error) {
	rows, err := c.runner().QueryContext(ctx, columnQuery, storedName(table, c.folding), column)
	if err != nil {
		return existingColumn{}, c.redefineFailure(table, column, "reading the column", err)
	}

	defer rows.Close()

	found, present, err := c.scanColumn(rows, table, column)
	if err != nil {
		return existingColumn{}, err
	}

	if err := rows.Err(); err != nil {
		return existingColumn{}, c.redefineFailure(table, column, "reading the column", err)
	}

	if !present {
		return existingColumn{}, c.missingColumn(table, column)
	}

	return found, nil
}

// scanColumn reads the one row the catalog answers with, and reports a column it does not hold.
func (c *mysqlConn) scanColumn(rows *sql.Rows, table, column string) (existingColumn, bool, error) {
	if !rows.Next() {
		return existingColumn{}, false, nil
	}

	var declared, data, nullable, extra, coll string
	var recorded *string

	if err := rows.Scan(&declared, &data, &nullable, &extra, &coll, &recorded); err != nil {
		return existingColumn{}, false, c.redefineFailure(table, column, "reading the column", err)
	}

	return existingColumn{
		declared:   declared,
		data:       data,
		nullable:   nullable == nullableYes,
		extra:      unrestatableExtra(extra),
		collation:  coll,
		hasDefault: c.carriesDefault(recorded),
	}, true, nil
}

// unrestatableExtra reports what EXTRA says once the marker describing the default is removed.
func unrestatableExtra(extra string) string {
	return strings.TrimSpace(strings.ReplaceAll(extra, defaultGenerated, ""))
}

// carriesDefault reports whether a column has a default a redefinition would have to restate.
func (c *mysqlConn) carriesDefault(recorded *string) bool {
	if recorded == nil {
		return false
	}

	return c.version.variant != MariaDB || *recorded != noDefault
}

// checkRestatable refuses a column carrying anything a redefinition would drop without saying so.
func (c *mysqlConn) checkRestatable(ctx context.Context, op ir.AlterColumn,
	found existingColumn,
) error {
	if err := checkRestatableColumn(op, found); err != nil {
		return err
	}

	return c.checkInlineCheck(ctx, op)
}

// checkRestatableColumn refuses what one catalog row alone says cannot be restated faithfully.
func checkRestatableColumn(op ir.AlterColumn, found existingColumn) error {
	if found.extra != "" {
		return cannotRestate(op, fmt.Sprintf("the catalog records %q for it, which a redefinition "+
			"restates nothing of: auto_increment, an ON UPDATE clause, and a generated column's "+
			"VIRTUAL or STORED marker all appear here, and each would be dropped", found.extra))
	}

	return checkCollation(op, found)
}

// checkCollation refuses a column comparing under something other than what this driver pins.
func checkCollation(op ir.AlterColumn, found existingColumn) error {
	if found.collation == "" || found.collation == collation {
		return nil
	}

	return cannotRestate(op, fmt.Sprintf("it compares under %s where this driver declares %s, and "+
		"a redefinition must restate a collation: restating this driver's would change what equality "+
		"and uniqueness mean on a column the operation only asked to retype", found.collation,
		collation))
}

// checkInlineCheck refuses a column whose own check constraint a redefinition would drop.
func (c *mysqlConn) checkInlineCheck(ctx context.Context, op ir.AlterColumn) error {
	if c.version.variant != MariaDB {
		return nil
	}

	count, err := c.countInlineChecks(ctx, op.Table, op.Column)
	if err != nil {
		return err
	}

	if count == 0 {
		return nil
	}

	return cannotRestate(op, "a check constraint is recorded under the column's own name, which is "+
		"how MariaDB records a check written on the column -- including the json_valid one every json "+
		"column carries, json being a longtext there. A redefinition drops it and the column stops "+
		"being validated at all: measured, invalid JSON was accepted afterwards. MySQL holds a check "+
		"apart from the column and keeps it, so nothing is refused there")
}

// countInlineChecks counts the check constraints recorded under a column's own name.
func (c *mysqlConn) countInlineChecks(ctx context.Context, table, column string) (int, error) {
	rows, err := c.runner().QueryContext(ctx, inlineCheckQuery, storedName(table, c.folding), column)
	if err != nil {
		return 0, c.redefineFailure(table, column, "reading the column's check constraints", err)
	}

	defer rows.Close()

	count := 0

	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return 0, c.redefineFailure(table, column, "counting check constraints", err)
		}
	}

	if err := rows.Err(); err != nil {
		return 0, c.redefineFailure(table, column, "reading the column's check constraints", err)
	}

	return count, nil
}

// redefineFailure reports a catalog read that failed, which is the server's to explain rather than this driver's.
func (c *mysqlConn) redefineFailure(table, column, doing string, err error) error {
	return &RedefineError{Table: table, Column: column, Detail: doing + ": " + err.Error(), Err: err}
}

// missingColumn reports a column the catalog does not hold, which a redefinition cannot be built for.
func (c *mysqlConn) missingColumn(table, column string) error {
	return &RedefineError{Table: table, Column: column, Detail: "this database has no such column, " +
		"so there is no definition to restate. A column change is compiled against the database " +
		"because MySQL's only statement for one is a full redefinition"}
}

// cannotRestate reports a column carrying something a full redefinition would drop without saying so.
func cannotRestate(op ir.AlterColumn, because string) error {
	return &RedefineError{Table: op.Table, Column: op.Column, Detail: because +
		". MySQL changes a column only by restating its whole definition, so this driver refuses " +
		"rather than emit a statement that quietly loses it"}
}

// cannotRestateDefault reports a default the operation did not replace and the catalog cannot spell back.
func cannotRestateDefault(op ir.AlterColumn) error {
	return &RedefineError{Table: op.Table, Column: op.Column, Detail: "it carries a default this " +
		"operation does not replace, and a redefinition drops every attribute it does not restate. " +
		"What the default is cannot be read back faithfully: MySQL reports a string default as the " +
		"raw value and MariaDB as a quoted literal, and MySQL renders an expression default with " +
		"backslash escapes, which this session's NO_BACKSLASH_ESCAPES makes unreadable. Set default " +
		"on this operation to state what it should be"}
}

// restate merges what the operation changes over what the catalog holds, refusing what it cannot carry.
func restate(op ir.AlterColumn, found existingColumn) (redefinition, error) {
	out := redefinition{
		table:    op.Table,
		column:   op.Column,
		declared: found.declared,
		collate:  found.collation != "",
		nullable: found.nullable,
	}

	if op.Type != nil {
		out.declared = nativeType(*op.Type)
		out.collate = collated(*op.Type)
	}

	if op.Nullable != nil {
		out.nullable = *op.Nullable
	}

	clause, err := restatedDefault(op, found)
	if err != nil {
		return redefinition{}, err
	}

	out.clause = clause

	return out, nil
}

// restatedDefault renders the default the redefinition carries, refusing one it cannot read back.
func restatedDefault(op ir.AlterColumn, found existingColumn) (string, error) {
	if op.DropDefault {
		return "", nil
	}

	if op.Default != nil {
		return renderDefault(op.Default, parenthesised(op, found)), nil
	}

	if !found.hasDefault {
		return "", nil
	}

	return "", cannotRestateDefault(op)
}

// parenthesised reports whether the column a default is being set on takes only an expression one.
func parenthesised(op ir.AlterColumn, found existingColumn) bool {
	if op.Type != nil {
		return needsExpressionDefault(*op.Type)
	}

	return expressionOnlyType(found.data)
}

// expressionOnlyType reports a catalog type MySQL refuses a bare literal default on, error 1101.
func expressionOnlyType(data string) bool {
	lowered := strings.ToLower(strings.TrimSpace(data))

	for _, family := range expressionOnly {
		if strings.HasSuffix(lowered, family) {
			return true
		}
	}

	return false
}
