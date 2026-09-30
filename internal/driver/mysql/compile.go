package mysql

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// indent offsets a column definition inside CREATE TABLE.
const indent = "    "

// terminator ends every statement this driver composes, and none that it passes through.
const terminator = ";"

// compile turns one operation into the statements MySQL runs for it, or refuses with a reason.
func compile(op ir.Operation) ([]driver.Statement, error) {
	if op == nil {
		return nil, fmt.Errorf("%s was asked to compile no operation at all", Name)
	}

	switch o := op.(type) {
	case ir.CreateTable:
		return compileCreateTable(o)
	case ir.DropTable:
		return compileDropTable(o)
	case ir.RenameTable:
		return compileRenameTable(o)
	case ir.AddColumn:
		return compileAddColumn(o)
	case ir.DropColumn:
		return compileDropColumn(o)
	case ir.RenameColumn:
		return compileRenameColumn(o)
	case ir.AlterColumn:
		return compileAlterColumn(o)
	case ir.AddIndex:
		return compileAddIndex(o)
	case ir.DropIndex:
		return compileDropIndex(o)
	case ir.AddForeignKey:
		return compileAddForeignKey(o)
	case ir.DropForeignKey:
		return compileDropForeignKey(o)
	case ir.AddUnique:
		return compileAddUnique(o)
	case ir.DropUnique:
		return compileDropUnique(o)
	case ir.AddCheck:
		return compileAddCheck(o)
	case ir.DropCheck:
		return compileDropCheck(o)
	case ir.AddPrimaryKey:
		return compileAddPrimaryKey(o)
	case ir.DropPrimaryKey:
		return compileDropPrimaryKey(o)
	case ir.CreateView:
		return compileCreateView(o)
	case ir.DropView:
		return compileDropView(o)
	case ir.Raw:
		return compileRaw(o)
	default:
		return nil, fmt.Errorf("%s has no compiler for %s: adding an operation is the five-part "+
			"change in CLAUDE.md invariant #4, and this driver is one of the five", Name, op.Kind())
	}
}

// one wraps the single statement most operations compile to.
func one(sql string) []driver.Statement {
	return []driver.Statement{{SQL: sql + terminator}}
}

// alterTable renders the prefix every operation that changes a table in place begins with.
func alterTable(table string) string {
	return "ALTER TABLE " + quote(table) + " "
}

// compileCreateTable renders a table with its columns, its primary key, and its character set.
func compileCreateTable(op ir.CreateTable) ([]driver.Statement, error) {
	if problem := checkCreateTable(op); problem != "" {
		return nil, refuse(op, problem)
	}

	lines := make([]string, 0, len(op.Columns)+1)
	for _, column := range op.Columns {
		lines = append(lines, indent+columnDefinition(column))
	}

	if len(op.PrimaryKey) > 0 {
		lines = append(lines, indent+"PRIMARY KEY ("+quoteAll(op.PrimaryKey)+")")
	}

	return one("CREATE TABLE " + quote(op.Table) + " (\n" + strings.Join(lines, ",\n") + "\n)" +
		tableOptions()), nil
}

// compileDropTable renders a table drop, which fails rather than cascading to what depends on it.
func compileDropTable(op ir.DropTable) ([]driver.Statement, error) {
	if problem := checkIdentifier("table", op.Table); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("DROP TABLE " + quote(op.Table)), nil
}

// compileRenameTable renders a table rename, spelled as an alteration rather than as RENAME TABLE.
func compileRenameTable(op ir.RenameTable) ([]driver.Statement, error) {
	if problem := checkRename(op.From, op.To); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(alterTable(op.From) + "RENAME TO " + quote(op.To)), nil
}

// compileAddColumn renders a column added to a table that already exists.
func compileAddColumn(op ir.AddColumn) ([]driver.Statement, error) {
	if problem := checkIdentifier("table", op.Table); problem != "" {
		return nil, refuse(op, problem)
	}

	if problem := checkColumn(op.Column); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(alterTable(op.Table) + "ADD COLUMN " + columnDefinition(op.Column)), nil
}

// compileDropColumn renders a column drop, which takes the data in it.
func compileDropColumn(op ir.DropColumn) ([]driver.Statement, error) {
	if problem := checkTableColumn(op.Table, op.Column); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(alterTable(op.Table) + "DROP COLUMN " + quote(op.Column)), nil
}

// compileRenameColumn renders a column rename, which preserves the data in it.
func compileRenameColumn(op ir.RenameColumn) ([]driver.Statement, error) {
	if problem := checkIdentifier("table", op.Table); problem != "" {
		return nil, refuse(op, problem)
	}

	if problem := checkRename(op.From, op.To); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(alterTable(op.Table) + "RENAME COLUMN " + quote(op.From) + " TO " + quote(op.To)), nil
}

// compileAddIndex renders a non-unique index over one table's columns, with no prefix length (doc.go).
func compileAddIndex(op ir.AddIndex) ([]driver.Statement, error) {
	if problem := checkNamedColumns(op.Table, op.Name, op.Columns); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("CREATE INDEX " + quote(op.Name) + " ON " + quote(op.Table) +
		" (" + quoteAll(op.Columns) + ")"), nil
}

// compileDropIndex renders an index drop, which names its table: MySQL indexes are table-scoped.
func compileDropIndex(op ir.DropIndex) ([]driver.Statement, error) {
	if problem := checkNamed(op.Table, op.Name); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("DROP INDEX " + quote(op.Name) + " ON " + quote(op.Table)), nil
}

// compileAddForeignKey renders a foreign key with the referential actions the operation declares.
func compileAddForeignKey(op ir.AddForeignKey) ([]driver.Statement, error) {
	if problem := checkAddForeignKey(op); problem != "" {
		return nil, refuse(op, problem)
	}

	sql := alterTable(op.Table) + "ADD CONSTRAINT " + quote(op.Name) +
		" FOREIGN KEY (" + quoteAll(op.Columns) + ")" +
		" REFERENCES " + quote(op.ReferencesTable) + " (" + quoteAll(op.ReferencesColumns) + ")"

	if action := nativeAction(op.OnDelete); action != "" {
		sql += " ON DELETE " + action
	}

	if action := nativeAction(op.OnUpdate); action != "" {
		sql += " ON UPDATE " + action
	}

	return one(sql), nil
}

// compileDropForeignKey renders a key drop, which MySQL spells for a foreign key specifically.
func compileDropForeignKey(op ir.DropForeignKey) ([]driver.Statement, error) {
	if problem := checkNamed(op.Table, op.Name); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(alterTable(op.Table) + "DROP FOREIGN KEY " + quote(op.Name)), nil
}

// compileAddUnique renders a unique constraint, which MySQL backs with an index of the same name.
func compileAddUnique(op ir.AddUnique) ([]driver.Statement, error) {
	if problem := checkNamedColumns(op.Table, op.Name, op.Columns); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(alterTable(op.Table) + "ADD CONSTRAINT " + quote(op.Name) +
		" UNIQUE (" + quoteAll(op.Columns) + ")"), nil
}

// compileDropUnique renders the index drop a unique constraint is undone by on every version of both.
func compileDropUnique(op ir.DropUnique) ([]driver.Statement, error) {
	if problem := checkNamed(op.Table, op.Name); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(alterTable(op.Table) + "DROP INDEX " + quote(op.Name)), nil
}

// compileAddCheck renders a check constraint, whose expression is engine SQL run as written.
func compileAddCheck(op ir.AddCheck) ([]driver.Statement, error) {
	if problem := checkNamed(op.Table, op.Name); problem != "" {
		return nil, refuse(op, problem)
	}

	if problem := checkVerbatim("expression", op.Expression); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(alterTable(op.Table) + "ADD CONSTRAINT " + quote(op.Name) +
		" CHECK (" + op.Expression + ")"), nil
}

// compileDropCheck renders a check drop, spelled DROP CONSTRAINT because MariaDB has no DROP CHECK.
func compileDropCheck(op ir.DropCheck) ([]driver.Statement, error) {
	if problem := checkNamed(op.Table, op.Name); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(alterTable(op.Table) + "DROP CONSTRAINT " + quote(op.Name)), nil
}

// compileAddPrimaryKey renders a primary key, whose name MySQL has no way to carry (doc.go).
func compileAddPrimaryKey(op ir.AddPrimaryKey) ([]driver.Statement, error) {
	if problem := checkAddPrimaryKey(op); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(alterTable(op.Table) + "ADD PRIMARY KEY (" + quoteAll(op.Columns) + ")"), nil
}

// compileDropPrimaryKey renders a key drop, which needs no name: MySQL calls every one PRIMARY.
func compileDropPrimaryKey(op ir.DropPrimaryKey) ([]driver.Statement, error) {
	if problem := checkIdentifier("table", op.Table); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(alterTable(op.Table) + "DROP PRIMARY KEY"), nil
}

// compileCreateView renders a view over a query that is engine SQL, embedded as written.
func compileCreateView(op ir.CreateView) ([]driver.Statement, error) {
	if problem := checkIdentifier("view", op.View); problem != "" {
		return nil, refuse(op, problem)
	}

	if problem := checkVerbatim("query", op.Query); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(createView(op.OrReplace) + quote(op.View) + " AS " + embed(op.Query)), nil
}

// createView renders the verb, which replaces an existing definition only when asked to.
func createView(orReplace bool) string {
	if orReplace {
		return "CREATE OR REPLACE VIEW "
	}

	return "CREATE VIEW "
}

// embed trims the terminator off a fragment being written into the middle of a statement.
func embed(query string) string {
	return strings.TrimRight(query, " \t\r\n"+terminator)
}

// compileDropView renders a view drop, which discards a definition rather than any row.
func compileDropView(op ir.DropView) ([]driver.Statement, error) {
	if problem := checkIdentifier("view", op.View); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("DROP VIEW " + quote(op.View)), nil
}

// compileRaw passes SQL through untouched, and only for the engine the operation names.
func compileRaw(op ir.Raw) ([]driver.Statement, error) {
	if problem := checkRaw(op); problem != "" {
		return nil, refuse(op, problem)
	}

	return []driver.Statement{{SQL: op.SQL}}, nil
}
