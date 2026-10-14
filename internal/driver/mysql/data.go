package mysql

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// The data operations compile against this engine's assignment rule, which is the permissive one:
// MySQL and MariaDB convert a value to the column's type on assignment, so nothing is cast -- and
// CAST would not serve anyway, its targets being SIGNED, CHAR, DECIMAL and a few more rather than
// varchar(255) or tinyint(1). The one value assignment will not convert is a boolean spelled as a
// word: 'true' into the tinyint(1) a bool is, error 1366 on both servers, so a bool literal is
// written as the keyword. All measured on MySQL 8.0 and MariaDB 11.4.

// compileDataCopy renders an insert of every row of one table into another, column by column.
func compileDataCopy(op ir.DataCopy) ([]driver.Statement, error) {
	if problem := checkDataCopy(op); problem != "" {
		return nil, refuse(op, problem)
	}

	read := make([]string, len(op.Columns))
	written := make([]string, len(op.Columns))

	for i, pair := range op.Columns {
		read[i], written[i] = quote(pair.From), quote(pair.To)
	}

	return one("INSERT INTO " + quote(op.To) + " (" + strings.Join(written, ", ") + ")\nSELECT " +
		strings.Join(read, ", ") + " FROM " + quote(op.From)), nil
}

// checkDataCopy reports why MySQL cannot carry either table or any column pair.
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

	return one("UPDATE " + quote(op.Table) + " SET " + quote(op.Column) + " = " +
		backfilled(op.Type, op.Value) + " WHERE " + quote(op.Column) + " IS NULL"), nil
}

// checkDataBackfill reports why MySQL cannot carry a backfill's target or the value it writes.
func checkDataBackfill(op ir.DataBackfill) string {
	if problem := checkDataTarget(op.Table, op.Column, op.Type); problem != "" {
		return problem
	}

	field := fmt.Sprintf("column %q", op.Column)
	if problem := checkDefault(field, op.Value); problem != "" {
		return problem
	}

	return checkLabelDefault(field, op.Type, op.Value)
}

// backfilled renders a backfill's value in the column's type, a bool literal as the keyword it means.
func backfilled(t schema.Type, d *ir.Default) string {
	if d.Literal == nil {
		return nativeFunction(d.Function)
	}

	if t.Kind() == schema.KindBool {
		if keyword, isWord := boolKeyword(*d.Literal); isWord {
			return keyword
		}
	}

	return quoteLiteral(*d.Literal)
}

// boolKeyword spells a boolean literal as MySQL's keyword, and reports whether it was a word at all.
func boolKeyword(literal string) (string, bool) {
	switch strings.ToLower(literal) {
	case "true":
		return "TRUE", true
	case "false":
		return "FALSE", true
	default:
		return "", false
	}
}

// compileDataTransform renders an update rewriting one column of every row from another.
func compileDataTransform(op ir.DataTransform) ([]driver.Statement, error) {
	if problem := checkDataTarget(op.Table, op.Column, op.Type); problem != "" {
		return nil, refuse(op, problem)
	}

	if problem := checkIdentifier("from", op.From); problem != "" {
		return nil, refuse(op, problem)
	}

	return one("UPDATE " + quote(op.Table) + " SET " + quote(op.Column) + " = " +
		transformed(op.Function, quote(op.From))), nil
}

// transformed applies a transform function to a source, the text functions over its text.
func transformed(f ir.TransformFunction, source string) string {
	switch f {
	case ir.TransformLower:
		return "LOWER(CAST(" + source + " AS CHAR))"
	case ir.TransformUpper:
		return "UPPER(CAST(" + source + " AS CHAR))"
	case ir.TransformTrim:
		return "TRIM(CAST(" + source + " AS CHAR))"
	case ir.TransformCopy:
		return source
	default:
		return source
	}
}

// checkDataTarget reports why MySQL cannot carry the table, column, or type a data write names.
func checkDataTarget(table, column string, t schema.Type) string {
	if problem := checkTableColumn(table, column); problem != "" {
		return problem
	}

	return checkType(fmt.Sprintf("column %q", column), t)
}
