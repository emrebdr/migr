package sqlite

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

// compile turns one operation into the statements SQLite runs for it.
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
		return nil, rebuildNeeded(o, "change a column's type, nullability, or default")
	case ir.AddIndex:
		return compileAddIndex(o)
	case ir.DropIndex:
		return compileDropIndex(o)
	case ir.AddForeignKey:
		return nil, rebuildNeeded(o, "add a foreign key to a table that already exists")
	case ir.DropForeignKey:
		return nil, rebuildNeeded(o, "drop a foreign key")
	case ir.AddUnique:
		return compileAddUnique(o)
	case ir.DropUnique:
		return compileDropUnique(o)
	case ir.AddCheck:
		return nil, rebuildNeeded(o, "add a check constraint to a table that already exists")
	case ir.DropCheck:
		return nil, rebuildNeeded(o, "drop a check constraint")
	case ir.AddPrimaryKey:
		return nil, rebuildNeeded(o, "add a primary key to a table that already exists")
	case ir.DropPrimaryKey:
		return nil, rebuildNeeded(o, "drop a primary key")
	case ir.CreateView:
		return compileCreateView(o)
	case ir.DropView:
		return compileDropView(o)
	case ir.Raw:
		return compileRaw(o)
	case ir.DataCopy:
		return compileDataCopy(o)
	case ir.DataBackfill:
		return compileDataBackfill(o)
	case ir.DataTransform:
		return compileDataTransform(o)
	default:
		return nil, fmt.Errorf("%s has no compiler for %s: adding an operation is the five-part "+
			"change in CLAUDE.md invariant #4, and this driver is one of the five", Name, op.Kind())
	}
}

// one wraps the single statement most operations compile to.
func one(sql string) []driver.Statement {
	return []driver.Statement{{SQL: sql + terminator}}
}

// compileCreateTable renders a table with its columns and, when declared, its primary key.
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

	return one("CREATE TABLE " + quote(op.Table) + " (\n" + strings.Join(lines, ",\n") + "\n)"), nil
}

// checkCreateTable reports why SQLite cannot create a table as the operation declares it.
func checkCreateTable(op ir.CreateTable) string {
	if problem := checkIdentifier("table", op.Table); problem != "" {
		return problem
	}

	for _, column := range op.Columns {
		if problem := checkColumn(column); problem != "" {
			return problem
		}
	}

	return checkColumnNames("primary_key", op.PrimaryKey)
}

// compileDropTable renders a table drop, which fails rather than cascading to what depends on it.
func compileDropTable(op ir.DropTable) ([]driver.Statement, error) {
	if problem := checkIdentifier("table", op.Table); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("DROP TABLE " + quote(op.Table)), nil
}

// compileRenameTable renders a table rename, which SQLite follows into every reference to it.
func compileRenameTable(op ir.RenameTable) ([]driver.Statement, error) {
	if problem := checkRename(op.From, op.To); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("ALTER TABLE " + quote(op.From) + " RENAME TO " + quote(op.To)), nil
}

// checkRename reports why SQLite cannot carry either side of a rename.
func checkRename(from, to string) string {
	if problem := checkIdentifier("from", from); problem != "" {
		return problem
	}

	return checkIdentifier("to", to)
}

// compileAddColumn renders a column added to a table that already exists.
func compileAddColumn(op ir.AddColumn) ([]driver.Statement, error) {
	if problem := checkIdentifier("table", op.Table); problem != "" {
		return nil, refuse(op, problem)
	}

	if problem := checkColumn(op.Column); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("ALTER TABLE " + quote(op.Table) + " ADD COLUMN " + columnDefinition(op.Column)), nil
}

// compileDropColumn renders a column drop, which takes the data in it.
func compileDropColumn(op ir.DropColumn) ([]driver.Statement, error) {
	if problem := checkTableColumn(op.Table, op.Column); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("ALTER TABLE " + quote(op.Table) + " DROP COLUMN " + quote(op.Column)), nil
}

// checkTableColumn reports why SQLite cannot carry a table name or the column beside it.
func checkTableColumn(table, column string) string {
	if problem := checkIdentifier("table", table); problem != "" {
		return problem
	}

	return checkIdentifier("column", column)
}

// compileRenameColumn renders a column rename, which preserves the data in it.
func compileRenameColumn(op ir.RenameColumn) ([]driver.Statement, error) {
	if problem := checkIdentifier("table", op.Table); problem != "" {
		return nil, refuse(op, problem)
	}

	if problem := checkRename(op.From, op.To); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("ALTER TABLE " + quote(op.Table) + " RENAME COLUMN " +
		quote(op.From) + " TO " + quote(op.To)), nil
}

// compileAddIndex renders a non-unique index, which blocks the single writer while it builds.
func compileAddIndex(op ir.AddIndex) ([]driver.Statement, error) {
	if problem := checkNamedColumns(op.Table, op.Name, op.Columns); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("CREATE INDEX " + quote(op.Name) + " ON " + quote(op.Table) +
		" (" + quoteAll(op.Columns) + ")"), nil
}

// checkNamedColumns reports why SQLite cannot carry a named object over a table's columns.
func checkNamedColumns(table, name string, columns []string) string {
	if problem := checkNamed(table, name); problem != "" {
		return problem
	}

	return checkColumnNames("columns", columns)
}

// compileDropIndex renders an index drop, which needs no table: SQLite indexes are database-scoped.
func compileDropIndex(op ir.DropIndex) ([]driver.Statement, error) {
	if problem := checkIdentifier("name", op.Name); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("DROP INDEX " + quote(op.Name)), nil
}

// compileAddUnique renders the unique index a UNIQUE constraint is in SQLite.
func compileAddUnique(op ir.AddUnique) ([]driver.Statement, error) {
	if problem := checkNamedColumns(op.Table, op.Name, op.Columns); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("CREATE UNIQUE INDEX " + quote(op.Name) + " ON " + quote(op.Table) +
		" (" + quoteAll(op.Columns) + ")"), nil
}

// compileDropUnique renders the index drop that undoes what compileAddUnique created.
func compileDropUnique(op ir.DropUnique) ([]driver.Statement, error) {
	if problem := checkIdentifier("name", op.Name); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("DROP INDEX " + quote(op.Name)), nil
}

// compileCreateView renders a view over a query that is engine SQL, embedded as written.
func compileCreateView(op ir.CreateView) ([]driver.Statement, error) {
	if problem := checkIdentifier("view", op.View); problem != "" {
		return nil, refuse(op, problem)
	}

	if problem := checkVerbatim("query", op.Query); problem != "" {
		return nil, refuse(op, problem)
	}

	created := one("CREATE VIEW " + quote(op.View) + " AS " + embed(op.Query))
	if !op.OrReplace {
		return created, nil
	}

	return append(one("DROP VIEW IF EXISTS "+quote(op.View)), created...), nil
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

	return []driver.Statement{{SQL: op.SQL, NoTransaction: op.NoTransaction}}, nil
}

// checkRaw reports SQL addressed to another engine, or SQL that would run nothing.
func checkRaw(op ir.Raw) string {
	if op.Driver != Name {
		return fmt.Sprintf("this raw statement is addressed to %q and nothing makes it portable, "+
			"so only that engine may run it", op.Driver)
	}

	if strings.TrimSpace(op.SQL) == "" {
		return "it carries no SQL, so it would report success while running nothing"
	}

	return checkVerbatim("sql", op.SQL)
}
