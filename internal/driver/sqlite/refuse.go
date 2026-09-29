package sqlite

import (
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// rebuildStrategy is the strategy the seven emulated operations name, in a refusal and in migr plan.
const rebuildStrategy = "table rebuild"

// RebuildStrategy is what migr plan prints beside an emulated operation, exported so a test can ask.
const RebuildStrategy = rebuildStrategy

// rebuildReason names the strategy and what it has to read, and ends every emulated answer.
const rebuildReason = "This is performed by a " + rebuildStrategy + ": copy the rows into a " +
	"table carrying the new definition, drop the original, put the new one in its place, copy " +
	"back, and restore every index and trigger. Building those statements means reading the " +
	"table's current definition out of sqlite_schema, which is why it is compiled against the " +
	"database rather than by Compile alone"

// refuse builds this driver's refusal to compile an operation, for a reason found in its payload.
func refuse(op ir.Operation, reason string) error {
	return driver.Unsupported(Name, op.Kind(), reason)
}

// rebuildNeeded reports an operation SQLite performs only by rebuilding the whole table.
func rebuildNeeded(op ir.Operation, cannot string) error {
	return driver.NeedsSession(Name, op.Kind(), fmt.Sprintf("SQLite's ALTER TABLE cannot %s -- it "+
		"renames a table, renames a column, adds a column and drops one, and does nothing else. %s",
		cannot, rebuildReason))
}

// checkIdentifier reports why SQLite cannot carry a name, and nothing when it can.
func checkIdentifier(field, name string) string {
	if strings.ContainsRune(name, 0) {
		return fmt.Sprintf("%s %q holds a NUL byte, which ends the statement where it appears: a "+
			"query reaches SQLite as a C string", field, name)
	}

	return ""
}

// checkNamed reports why SQLite cannot carry a table name or the object name beside it.
func checkNamed(table, name string) string {
	if problem := checkIdentifier("table", table); problem != "" {
		return problem
	}

	return checkIdentifier("name", name)
}

// checkColumnNames reports the first column in a list SQLite cannot carry.
func checkColumnNames(field string, columns []string) string {
	for i, column := range columns {
		if problem := checkIdentifier(fmt.Sprintf("%s[%d]", field, i), column); problem != "" {
			return problem
		}
	}

	return ""
}

// checkColumn reports why SQLite cannot carry a column's name, type, or default.
func checkColumn(c ir.Column) string {
	if problem := checkIdentifier("column", c.Name); problem != "" {
		return problem
	}

	field := fmt.Sprintf("column %q", c.Name)

	if problem := checkType(field, c.Type); problem != "" {
		return problem
	}

	if problem := checkDefault(field, c.Default); problem != "" {
		return problem
	}

	return checkLabelDefault(field, c.Type, c.Default)
}

// checkType reports why SQLite cannot carry a logical type, and nothing when it can.
func checkType(field string, t schema.Type) string {
	if t.Kind() == schema.KindArray {
		return fmt.Sprintf("%s is %s, and SQLite has no array type: every value belongs to one of "+
			"five storage classes and none of them is a list. Holding JSON text in the column "+
			"would make it a different type from the one the migration named", field, t)
	}

	return describeUnmapped(field, t)
}

// describeUnmapped reports a type this driver has no SQLite declaration for.
func describeUnmapped(field string, t schema.Type) string {
	if nativeType(t) != "" {
		return ""
	}

	return fmt.Sprintf("%s is %s, which this driver has no SQLite type for", field, t)
}

// checkDefault reports why SQLite cannot carry a column default, and nothing when it can.
func checkDefault(field string, d *ir.Default) string {
	if d == nil {
		return ""
	}

	if d.Literal != nil {
		return checkLiteral(field, *d.Literal)
	}

	if d.Function == ir.FuncUUID {
		return fmt.Sprintf("%s defaults to uuid(), and SQLite links no UUID function: the "+
			"extension that provides one is not compiled into this library. The hex(randomblob()) "+
			"idiom would write a generator into the schema rather than name one, so this driver "+
			"refuses instead -- generate the value in the application, or use string(36)", field)
	}

	if nativeFunction(d.Function) == "" {
		return fmt.Sprintf("%s defaults to the function %q, which this driver has no SQLite "+
			"spelling for", field, d.Function)
	}

	return ""
}

// checkLiteral reports a default value that would not survive being written into a statement.
func checkLiteral(field, literal string) string {
	if strings.ContainsRune(literal, 0) {
		return fmt.Sprintf("%s defaults to %q, which holds a NUL byte: a default is written into "+
			"the statement rather than bound, and a query reaches SQLite as a C string",
			field, literal)
	}

	return ""
}

// checkLabelDefault reports an enum column defaulting to something that is not one of its labels.
func checkLabelDefault(field string, t schema.Type, d *ir.Default) string {
	labels, isEnum := t.Values()
	if !isEnum || d == nil || d.Literal == nil || slices.Contains(labels, *d.Literal) {
		return ""
	}

	return fmt.Sprintf("%s is %s and defaults to %q, which is not one of its labels: the default "+
		"can never satisfy the constraint the labels compile to, so the column is unusable the "+
		"first time a row relies on it. Declare a label, or leave the column without a default",
		field, t, *d.Literal)
}

// checkVerbatim reports engine SQL this driver embeds that would not survive being sent.
func checkVerbatim(field, sql string) string {
	if strings.ContainsRune(sql, 0) {
		return fmt.Sprintf("%s holds a NUL byte, which ends the statement where it appears: a "+
			"query reaches SQLite as a C string", field)
	}

	return ""
}
