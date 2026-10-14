package sqlite

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// The data operations compile to plain assignment and never to CAST, because on this engine a cast
// is what changes the value. A column's declared type is only an affinity, and a type name with no
// recognized fragment -- boolean, timestamp, decimal -- gets NUMERIC affinity, so CAST('true' AS
// boolean) stores the integer 0 and CAST('2026-01-02 03:04:05' AS timestamp) the integer 2026.
// Assignment stores what the column's affinity makes of the value, which is what an insert of the
// same value does. Measured, every logical type this driver maps.
//
// Every column read is qualified by its table, for a reason that is this engine's alone: a
// double-quoted name that resolves to no column is taken as a string literal, so lower("emial")
// writes the text 'emial' into every row, SELECT "emial" copies it, and WHERE "nope" IS NULL
// matches nothing -- each silently. A qualified "people"."emial" is an error, so a misspelled column
// fails the statement rather than the data. The column written needs none: SET takes only a column.

// compileDataCopy renders an insert of every row of one table into another, column by column.
func compileDataCopy(op ir.DataCopy) ([]driver.Statement, error) {
	if problem := checkDataCopy(op); problem != "" {
		return nil, refuse(op, problem)
	}

	read := make([]string, len(op.Columns))
	written := make([]string, len(op.Columns))

	for i, pair := range op.Columns {
		read[i], written[i] = qualified(op.From, pair.From), quote(pair.To)
	}

	return one("INSERT INTO " + quote(op.To) + " (" + strings.Join(written, ", ") + ")\nSELECT " +
		strings.Join(read, ", ") + " FROM " + quote(op.From)), nil
}

// checkDataCopy reports why SQLite cannot carry either table or any column pair.
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
		backfilled(op.Value) + " WHERE " + qualified(op.Table, op.Column) + " IS NULL"), nil
}

// checkDataBackfill reports why SQLite cannot carry a backfill's target or the value it writes.
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

// backfilled renders a backfill's value as the default clause would, without the DEFAULT keyword.
func backfilled(d *ir.Default) string {
	if d.Literal != nil {
		return quoteLiteral(*d.Literal)
	}

	return nativeFunction(d.Function)
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
		transformed(op.Function, qualified(op.Table, op.From))), nil
}

// transformed applies a transform function to a source, which SQLite reads as text by itself.
func transformed(f ir.TransformFunction, source string) string {
	switch f {
	case ir.TransformLower:
		return "lower(" + source + ")"
	case ir.TransformUpper:
		return "upper(" + source + ")"
	case ir.TransformTrim:
		return "trim(" + source + ")"
	case ir.TransformCopy:
		return source
	default:
		return source
	}
}

// qualified names a column by its table, which is the spelling SQLite will not read as a string.
func qualified(table, column string) string {
	return quote(table) + "." + quote(column)
}

// checkDataTarget reports why SQLite cannot carry the table, column, or type a data write names.
func checkDataTarget(table, column string, t schema.Type) string {
	if problem := checkIdentifier("table", table); problem != "" {
		return problem
	}

	if problem := checkIdentifier("column", column); problem != "" {
		return problem
	}

	return checkType(fmt.Sprintf("column %q", column), t)
}
