package postgres

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

// compile turns one operation into the statements PostgreSQL runs for it.
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
		return compileDropConstraint(o, o.Table, o.Name)
	case ir.AddUnique:
		return compileAddUnique(o)
	case ir.DropUnique:
		return compileDropConstraint(o, o.Table, o.Name)
	case ir.AddCheck:
		return compileAddCheck(o)
	case ir.DropCheck:
		return compileDropConstraint(o, o.Table, o.Name)
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

// checkCreateTable reports why PostgreSQL cannot create a table as the operation declares it.
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

// compileRenameTable renders a table rename, whose target is unqualified by PostgreSQL's grammar.
func compileRenameTable(op ir.RenameTable) ([]driver.Statement, error) {
	if problem := checkRename(op.From, op.To); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("ALTER TABLE " + quote(op.From) + " RENAME TO " + quote(op.To)), nil
}

// checkRename reports why PostgreSQL cannot carry either side of a rename.
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

// checkTableColumn reports why PostgreSQL cannot carry a table name or the column beside it.
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

// compileAlterColumn renders each requested change as its own statement, in a fixed order.
func compileAlterColumn(op ir.AlterColumn) ([]driver.Statement, error) {
	if problem := checkAlterColumn(op); problem != "" {
		return nil, refuse(op, problem)
	}

	prefix := "ALTER TABLE " + quote(op.Table) + " ALTER COLUMN " + quote(op.Column) + " "

	var stmts []driver.Statement

	if op.Type != nil {
		stmts = append(stmts, one(prefix+"TYPE "+nativeType(*op.Type))...)
	}

	if op.Nullable != nil {
		stmts = append(stmts, one(prefix+nullabilityChange(*op.Nullable))...)
	}

	if op.Default != nil {
		stmts = append(stmts, one(prefix+"SET "+defaultClause(op.Default))...)
	}

	if op.DropDefault {
		stmts = append(stmts, one(prefix+"DROP DEFAULT")...)
	}

	return stmts, nil
}

// nullabilityChange renders the clause that makes a column nullable, or stops it being so.
func nullabilityChange(nullable bool) string {
	if nullable {
		return "DROP NOT NULL"
	}

	return "SET NOT NULL"
}

// checkAlterColumn reports an alteration PostgreSQL cannot carry, or one that asks for nothing.
func checkAlterColumn(op ir.AlterColumn) string {
	if problem := checkTableColumn(op.Table, op.Column); problem != "" {
		return problem
	}

	field := fmt.Sprintf("column %q", op.Column)

	if op.Type != nil {
		if problem := checkType(field, *op.Type); problem != "" {
			return problem
		}
	}

	if problem := checkDefault(field, op.Default); problem != "" {
		return problem
	}

	return checkAlterationRequested(op)
}

// checkAlterationRequested reports an alteration that would run no statements, or contradict itself.
func checkAlterationRequested(op ir.AlterColumn) string {
	if op.Default != nil && op.DropDefault {
		return "it both sets a default and drops one, and PostgreSQL would apply whichever this " +
			"driver emitted last"
	}

	if op.Type == nil && op.Nullable == nil && op.Default == nil && !op.DropDefault {
		return "it requests no change, so it would compile to no statements at all while " +
			"reporting success"
	}

	return ""
}

// compileAddIndex renders a non-unique index, which locks writes for as long as it builds.
func compileAddIndex(op ir.AddIndex) ([]driver.Statement, error) {
	if problem := checkNamedColumns(op.Table, op.Name, op.Columns); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("CREATE INDEX " + quote(op.Name) + " ON " + quote(op.Table) +
		" (" + quoteAll(op.Columns) + ")"), nil
}

// checkNamedColumns reports why PostgreSQL cannot carry a named object over a table's columns.
func checkNamedColumns(table, name string, columns []string) string {
	if problem := checkNamed(table, name); problem != "" {
		return problem
	}

	return checkColumnNames("columns", columns)
}

// compileDropIndex renders an index drop, which needs no table: PostgreSQL indexes are schema-scoped.
func compileDropIndex(op ir.DropIndex) ([]driver.Statement, error) {
	if problem := checkIdentifier("name", op.Name); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("DROP INDEX " + quote(op.Name)), nil
}

// compileAddForeignKey renders a foreign key with the referential actions the operation declares.
func compileAddForeignKey(op ir.AddForeignKey) ([]driver.Statement, error) {
	if problem := checkAddForeignKey(op); problem != "" {
		return nil, refuse(op, problem)
	}

	sql := "ALTER TABLE " + quote(op.Table) + " ADD CONSTRAINT " + quote(op.Name) +
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

// checkAddForeignKey reports why PostgreSQL cannot carry either side of a foreign key.
func checkAddForeignKey(op ir.AddForeignKey) string {
	if problem := checkNamedColumns(op.Table, op.Name, op.Columns); problem != "" {
		return problem
	}

	if problem := checkIdentifier("references_table", op.ReferencesTable); problem != "" {
		return problem
	}

	if problem := checkColumnNames("references_columns", op.ReferencesColumns); problem != "" {
		return problem
	}

	if problem := checkAction("on_delete", op.OnDelete); problem != "" {
		return problem
	}

	return checkAction("on_update", op.OnUpdate)
}

// compileAddUnique renders a unique constraint, which PostgreSQL backs with an index it owns.
func compileAddUnique(op ir.AddUnique) ([]driver.Statement, error) {
	if problem := checkNamedColumns(op.Table, op.Name, op.Columns); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("ALTER TABLE " + quote(op.Table) + " ADD CONSTRAINT " + quote(op.Name) +
		" UNIQUE (" + quoteAll(op.Columns) + ")"), nil
}

// compileAddCheck renders a check constraint, whose expression is engine SQL run as written.
func compileAddCheck(op ir.AddCheck) ([]driver.Statement, error) {
	if problem := checkNamed(op.Table, op.Name); problem != "" {
		return nil, refuse(op, problem)
	}

	if problem := checkVerbatim("expression", op.Expression); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("ALTER TABLE " + quote(op.Table) + " ADD CONSTRAINT " + quote(op.Name) +
		" CHECK (" + op.Expression + ")"), nil
}

// compileAddPrimaryKey renders a primary key, letting PostgreSQL derive the name when none is given.
func compileAddPrimaryKey(op ir.AddPrimaryKey) ([]driver.Statement, error) {
	if problem := checkAddPrimaryKey(op); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("ALTER TABLE " + quote(op.Table) + " " + addConstraint(op.Name) +
		"PRIMARY KEY (" + quoteAll(op.Columns) + ")"), nil
}

// addConstraint renders the ADD CONSTRAINT clause, or a plain ADD when the operation named nothing.
func addConstraint(name string) string {
	if name == "" {
		return "ADD "
	}

	return "ADD CONSTRAINT " + quote(name) + " "
}

// checkAddPrimaryKey reports why PostgreSQL cannot carry a primary key, whose name is optional.
func checkAddPrimaryKey(op ir.AddPrimaryKey) string {
	if problem := checkIdentifier("table", op.Table); problem != "" {
		return problem
	}

	if op.Name != "" {
		if problem := checkIdentifier("name", op.Name); problem != "" {
			return problem
		}
	}

	return checkColumnNames("columns", op.Columns)
}

// compileDropPrimaryKey renders a primary key drop, which PostgreSQL spells by constraint name.
func compileDropPrimaryKey(op ir.DropPrimaryKey) ([]driver.Statement, error) {
	if problem := checkDropPrimaryKey(op); problem != "" {
		return nil, refuse(op, problem)
	}

	return compileDropConstraint(op, op.Table, op.Name)
}

// checkDropPrimaryKey reports the unnamed key PostgreSQL has no statement for.
func checkDropPrimaryKey(op ir.DropPrimaryKey) string {
	if strings.TrimSpace(op.Name) != "" {
		return ""
	}

	return "PostgreSQL has no name-free DROP PRIMARY KEY -- a key is dropped by ALTER TABLE ... " +
		"DROP CONSTRAINT -- so this operation needs the constraint's name. The v1 IR leaves it " +
		"optional because MySQL has no name for a primary key; introspection reports PostgreSQL's"
}

// compileDropConstraint renders the one statement PostgreSQL has for dropping any named constraint.
func compileDropConstraint(op ir.Operation, table, name string) ([]driver.Statement, error) {
	if problem := checkNamed(table, name); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("ALTER TABLE " + quote(table) + " DROP CONSTRAINT " + quote(name)), nil
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
