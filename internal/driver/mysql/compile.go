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
	case ir.AddIndex:
		return compileAddIndex(o)
	case ir.DropTable, ir.RenameTable, ir.AddColumn, ir.DropColumn, ir.RenameColumn, ir.AlterColumn,
		ir.DropIndex, ir.AddForeignKey, ir.DropForeignKey, ir.AddUnique, ir.DropUnique, ir.AddCheck,
		ir.DropCheck, ir.AddPrimaryKey, ir.DropPrimaryKey, ir.CreateView, ir.DropView, ir.Raw:
		return nil, notYet(o)
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

// checkCreateTable reports why MySQL cannot create a table as the operation declares it.
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

// compileAddIndex renders a non-unique index over one table's columns, with no prefix length (doc.go).
func compileAddIndex(op ir.AddIndex) ([]driver.Statement, error) {
	if problem := checkNamedColumns(op.Table, op.Name, op.Columns); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("CREATE INDEX " + quote(op.Name) + " ON " + quote(op.Table) +
		" (" + quoteAll(op.Columns) + ")"), nil
}
