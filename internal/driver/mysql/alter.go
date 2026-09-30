package mysql

import (
	"fmt"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// redefinitionReason states why a column change reaches the database before it becomes a statement.
const redefinitionReason = "MySQL has no statement that changes one attribute of a column: a type " +
	"or nullability change is MODIFY COLUMN, a full redefinition, and every attribute it does not " +
	"restate is dropped -- measured, a MODIFY of the type alone lost both the NOT NULL and the " +
	"DEFAULT. Setting a default is the same statement whenever the column is a text, blob or json " +
	"one, which ALTER COLUMN ... SET DEFAULT refuses outright with error 1101 on MySQL. So the " +
	"attributes the operation does not name, and the column's type where it decides which default " +
	"syntax is legal, are read from the database first"

// compileAlterColumn renders the one change MySQL states without a type, and sends the rest to a session.
func compileAlterColumn(op ir.AlterColumn) ([]driver.Statement, error) {
	if problem := checkAlterColumn(op); problem != "" {
		return nil, refuse(op, problem)
	}

	if !onlyDropsDefault(op) {
		return nil, driver.NeedsSession(Name, op.Kind(), redefinitionReason)
	}

	return one(alterColumn(op.Table, op.Column) + "DROP DEFAULT"), nil
}

// onlyDropsDefault reports the one alteration that names no value, so needs no type to be spelled.
func onlyDropsDefault(op ir.AlterColumn) bool {
	return op.DropDefault && op.Type == nil && op.Nullable == nil && op.Default == nil
}

// alterColumn renders the prefix the two attribute-scoped column statements begin with.
func alterColumn(table, column string) string {
	return alterTable(table) + "ALTER COLUMN " + quote(column) + " "
}

// checkAlterColumn reports an alteration MySQL cannot carry, or one that asks for nothing.
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
		return "it both sets a default and drops one, and MySQL would apply whichever this driver " +
			"restated last"
	}

	if op.Type == nil && op.Nullable == nil && op.Default == nil && !op.DropDefault {
		return "it requests no change, so it would compile to no statements at all while " +
			"reporting success"
	}

	return ""
}

// redefinition is a column as MODIFY COLUMN must restate it, every attribute already resolved.
type redefinition struct {
	// table is the table the column belongs to.
	table string
	// column is the column being restated.
	column string
	// declared is the type text, either this driver's for a new type or the server's for a kept one.
	declared string
	// collate declares the pinned character set, and is false for a type that accepts none.
	collate bool
	// nullable is what the column will be, stated in both directions because omitting it means null.
	nullable bool
	// clause is the rendered DEFAULT, empty where the column is to have none.
	clause string
}

// render renders the full redefinition MySQL spells a column change as.
func (r redefinition) render() string {
	definition := quote(r.column) + " " + r.declared

	if r.collate {
		definition += " " + collateClause()
	}

	definition += " " + nullability(r.nullable)

	if r.clause != "" {
		definition += " " + r.clause
	}

	return alterTable(r.table) + "MODIFY COLUMN " + definition
}
