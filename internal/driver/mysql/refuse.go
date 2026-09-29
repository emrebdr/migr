package mysql

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// identifierLimit is how many characters MySQL keeps in a name, and it refuses one longer.
//
// Characters rather than bytes, which is why this is counted with utf8.RuneCountInString. Error 1059
// on 8.0.46 and 1103 on 11.4.13 -- the number is the same on both and the message is not, which is
// the reason a driver refuses a name itself rather than forwarding the server's answer.
const identifierLimit = 64

// pending is why this driver refuses eighteen operations, and it names the task that stops it refusing.
//
// Two are not on that list. The history table is created through driver-compiled operations
// (invariant #3), so create_table and add_index have to compile before there is a history store at
// all -- which is why they are here rather than in M9-T2, and why that task is a diff of eighteen.
const pending = "this driver does not compile this operation yet: M9-T2 is where MySQL's SQL is " +
	"written, and until then the declaration and the compiler refuse together rather than one of " +
	"them pretending. create_table and add_index are the two exceptions, because the history " +
	"table is itself created through compiled operations (invariant #4 and invariant #3)"

// refuse builds this driver's refusal to compile an operation, for a reason found in its payload.
func refuse(op ir.Operation, reason string) error {
	return driver.Unsupported(Name, op.Kind(), reason)
}

// notYet builds this driver's refusal to compile an operation M9-T2 has not written yet.
func notYet(op ir.Operation) error {
	return driver.Unsupported(Name, op.Kind(), pending)
}

// checkIdentifier reports why MySQL cannot carry a name, and nothing when it can.
func checkIdentifier(field, name string) string {
	if strings.ContainsRune(name, 0) {
		return fmt.Sprintf("%s %q holds a NUL byte, which MySQL reads as the end of the quoted "+
			"identifier and then reports a syntax error", field, name)
	}

	if count := utf8.RuneCountInString(name); count > identifierLimit {
		return fmt.Sprintf("%s %q is %d characters and MySQL keeps %d", field, name, count,
			identifierLimit)
	}

	if strings.HasSuffix(name, " ") {
		return fmt.Sprintf("%s %q ends in a space, which MySQL refuses as an incorrect name even "+
			"quoted", field, name)
	}

	return ""
}

// checkNamedColumns reports why MySQL cannot carry a named object over a table's columns.
func checkNamedColumns(table, name string, columns []string) string {
	if problem := checkIdentifier("table", table); problem != "" {
		return problem
	}

	if problem := checkIdentifier("name", name); problem != "" {
		return problem
	}

	return checkColumnNames("columns", columns)
}

// checkColumnNames reports the first column in a list MySQL cannot carry.
func checkColumnNames(field string, columns []string) string {
	for i, column := range columns {
		if problem := checkIdentifier(fmt.Sprintf("%s[%d]", field, i), column); problem != "" {
			return problem
		}
	}

	return ""
}

// checkColumn reports why MySQL cannot carry a column's name, type, or default.
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

// checkType reports why MySQL cannot carry a logical type, and nothing when it can.
func checkType(field string, t schema.Type) string {
	if t.Kind() == schema.KindArray {
		return fmt.Sprintf("%s is %s, and MySQL has no array type: a JSON column would hold a list "+
			"but would be a different type from the one the migration named, with no element type "+
			"and no per-element constraint", field, t)
	}

	if nativeType(t) == "" {
		return fmt.Sprintf("%s is %s, which this driver has no MySQL type for", field, t)
	}

	return ""
}

// checkDefault reports why MySQL cannot carry a column default, and nothing when it can.
func checkDefault(field string, d *ir.Default) string {
	if d == nil {
		return ""
	}

	if d.Literal != nil {
		return checkLiteral(field, *d.Literal)
	}

	if nativeFunction(d.Function) == "" {
		return fmt.Sprintf("%s defaults to the function %q, which this driver has no MySQL spelling "+
			"for", field, d.Function)
	}

	return ""
}

// checkLiteral reports a default value that would not survive being written into a statement.
func checkLiteral(field, literal string) string {
	if strings.ContainsRune(literal, 0) {
		return fmt.Sprintf("%s defaults to %q, which holds a NUL byte: a default is written into "+
			"the statement rather than bound, and MySQL reads the byte as the end of the literal",
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

	return fmt.Sprintf("%s is %s and defaults to %q, which is not one of its labels: MySQL's enum is "+
		"native, so the default can never be a value the column accepts and every insert relying on "+
		"it fails. Declare a label, or leave the column without a default", field, t, *d.Literal)
}
