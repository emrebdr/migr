package postgres

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// The data operations compile against PostgreSQL's assignment rule, which is the strict one: a text
// expression is not assigned to an integer, boolean, numeric, date or json column at all, so a
// transform is cast to the target's native type explicitly. A backfill literal needs no cast,
// since a quoted literal is untyped until the column types it -- both measured on 16 and 17.

// compileDataCopy renders an insert of every row of one table into another, column by column.
func compileDataCopy(op ir.DataCopy) ([]driver.Statement, error) {
	if problem := checkDataCopy(op); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(copyHead(op)), nil
}

// copyHead renders the insert a copy is, up to the clause a chunk narrows it by.
func copyHead(op ir.DataCopy) string {
	read := make([]string, len(op.Columns))
	written := make([]string, len(op.Columns))

	for i, pair := range op.Columns {
		read[i], written[i] = quote(pair.From), quote(pair.To)
	}

	return "INSERT INTO " + quote(op.To) + " (" + strings.Join(written, ", ") + ")\nSELECT " +
		strings.Join(read, ", ") + " FROM " + quote(op.From)
}

// checkDataCopy reports why PostgreSQL cannot carry either table or any column pair.
func checkDataCopy(op ir.DataCopy) string {
	if problem := checkIdentifier("from", op.From); problem != "" {
		return problem
	}

	if problem := checkIdentifier("to", op.To); problem != "" {
		return problem
	}

	for i, pair := range op.Columns {
		field := fmt.Sprintf("columns[%d]", i)
		if problem := checkColumnNames(field, []string{pair.From, pair.To}); problem != "" {
			return problem
		}
	}

	return ""
}

// compileDataBackfill renders an update writing one value into every NULL of one column.
func compileDataBackfill(op ir.DataBackfill) ([]driver.Statement, error) {
	if problem := checkDataBackfill(op); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(backfillHead(op) + " WHERE " + backfillCondition(op)), nil
}

// checkDataBackfill reports why PostgreSQL cannot carry a backfill's target or the value it writes.
func checkDataBackfill(op ir.DataBackfill) string {
	if problem := checkDataTarget(op.Table, op.Column, op.Type); problem != "" {
		return problem
	}

	return checkDefault(fmt.Sprintf("column %q", op.Column), op.Value)
}

// backfillHead renders the update a backfill is, up to its WHERE.
func backfillHead(op ir.DataBackfill) string {
	return "UPDATE " + quote(op.Table) + " SET " + quote(op.Column) + " = " + backfilled(op.Value)
}

// backfillCondition renders what keeps a backfill to the NULLs of its column.
func backfillCondition(op ir.DataBackfill) string {
	return quote(op.Column) + " IS NULL"
}

// backfilled renders a backfill's value as the default clause would, without the DEFAULT keyword.
func backfilled(d *ir.Default) string {
	if d.Literal != nil {
		return quoteLiteral(*d.Literal)
	}

	return nativeFunction(d.Function)
}

// compileDataTransform renders an update rewriting one column of every row from another.
func compileDataTransform(op ir.DataTransform) ([]driver.Statement, error) {
	if problem := checkDataTransform(op); problem != "" {
		return nil, refuse(op, problem)
	}

	return one(transformHead(op)), nil
}

// checkDataTransform reports why PostgreSQL cannot carry a transform's target or the column it reads.
func checkDataTransform(op ir.DataTransform) string {
	if problem := checkDataTarget(op.Table, op.Column, op.Type); problem != "" {
		return problem
	}

	return checkIdentifier("from", op.From)
}

// transformHead renders the update a transform is, up to the clause a chunk narrows it by.
func transformHead(op ir.DataTransform) string {
	return "UPDATE " + quote(op.Table) + " SET " + quote(op.Column) + " = CAST(" +
		transformed(op.Function, quote(op.From)) + " AS " + nativeType(op.Type) + ")"
}

// transformed applies a transform function to a source, the text functions over its text.
func transformed(f ir.TransformFunction, source string) string {
	switch f {
	case ir.TransformLower:
		return "lower(CAST(" + source + " AS text))"
	case ir.TransformUpper:
		return "upper(CAST(" + source + " AS text))"
	case ir.TransformTrim:
		return "btrim(CAST(" + source + " AS text))"
	case ir.TransformCopy:
		return source
	default:
		return source
	}
}

// checkDataTarget reports why PostgreSQL cannot carry the table, column, or type a data write names.
func checkDataTarget(table, column string, t schema.Type) string {
	if problem := checkTableColumn(table, column); problem != "" {
		return problem
	}

	return checkType(fmt.Sprintf("column %q", column), t)
}
