package postgres

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// enumBlocker names what the v1 IR would need before an enum could be supported at all.
const enumBlocker = "an operation that owns the type"

// refuse builds this driver's refusal to compile an operation, for a reason found in its payload.
func refuse(op ir.Operation, reason string) error {
	return driver.Unsupported(Name, op.Kind(), reason)
}

// checkIdentifier reports why PostgreSQL cannot carry a name, and nothing when it can.
func checkIdentifier(field, name string) string {
	if len(name) > maxIdentifier {
		return fmt.Sprintf("%s %q is %d bytes, and PostgreSQL truncates an identifier to %d with a "+
			"notice rather than an error, so two long names silently become one object",
			field, name, len(name), maxIdentifier)
	}

	if strings.ContainsRune(name, 0) {
		return fmt.Sprintf("%s %q holds a NUL byte, which ends the statement where it appears: a "+
			"query reaches the server as a C string", field, name)
	}

	return ""
}

// checkNamed reports why PostgreSQL cannot carry a table name or the object name beside it.
func checkNamed(table, name string) string {
	if problem := checkIdentifier("table", table); problem != "" {
		return problem
	}

	return checkIdentifier("name", name)
}

// checkColumnNames reports the first column in a list PostgreSQL cannot carry.
func checkColumnNames(field string, columns []string) string {
	for i, column := range columns {
		if problem := checkIdentifier(fmt.Sprintf("%s[%d]", field, i), column); problem != "" {
			return problem
		}
	}

	return ""
}

// checkColumn reports why PostgreSQL cannot carry a column's name, type, or default.
func checkColumn(c ir.Column) string {
	if problem := checkIdentifier("column", c.Name); problem != "" {
		return problem
	}

	field := fmt.Sprintf("column %q", c.Name)

	if problem := checkType(field, c.Type); problem != "" {
		return problem
	}

	return checkDefault(field, c.Default)
}

// checkType reports why PostgreSQL cannot carry a logical type, and nothing when it can.
func checkType(field string, t schema.Type) string {
	switch t.Kind() {
	case schema.KindEnum:
		return describeEnum(field, t)
	case schema.KindArray:
		return checkArray(field, t)
	default:
		return describeUnmapped(field, t)
	}
}

// describeEnum states why an enum needs an object no operation in the v1 IR owns.
func describeEnum(field string, t schema.Type) string {
	return fmt.Sprintf("%s is %s, and a PostgreSQL enum is a CREATE TYPE object that no operation "+
		"in the v1 IR owns: nothing would drop it, so applying a migration, rolling it back, and "+
		"applying it again fails on a type that already exists. Supporting it needs %s, which is "+
		"an IR change rather than a driver one; use string(n) with a check constraint meanwhile",
		field, t, enumBlocker)
}

// checkArray reports an array PostgreSQL would flatten, or an element it cannot carry.
func checkArray(field string, t schema.Type) string {
	elem, declared := t.Elem()
	if !declared {
		return fmt.Sprintf("%s is an array of nothing", field)
	}

	if elem.Kind() == schema.KindArray {
		return fmt.Sprintf("%s is %s, and PostgreSQL does not enforce array dimensions: integer[][] "+
			"and integer[] are one type, so a nested array would silently become a flat one",
			field, t)
	}

	return checkType(field, elem)
}

// describeUnmapped reports a type this driver has no PostgreSQL spelling for.
func describeUnmapped(field string, t schema.Type) string {
	if nativeType(t) != "" {
		return ""
	}

	return fmt.Sprintf("%s is %s, which this driver has no PostgreSQL type for", field, t)
}

// checkDefault reports why PostgreSQL cannot carry a column default, and nothing when it can.
func checkDefault(field string, d *ir.Default) string {
	if d == nil {
		return ""
	}

	if d.Literal != nil {
		return checkLiteral(field, *d.Literal)
	}

	if nativeFunction(d.Function) == "" {
		return fmt.Sprintf("%s defaults to the function %q, which this driver has no PostgreSQL "+
			"spelling for", field, d.Function)
	}

	return ""
}

// checkLiteral reports a default value that would not survive being written into a statement.
func checkLiteral(field, literal string) string {
	if strings.ContainsRune(literal, 0) {
		return fmt.Sprintf("%s defaults to %q, which holds a NUL byte: a default is written into "+
			"the statement rather than bound, and a query reaches the server as a C string",
			field, literal)
	}

	return ""
}

// checkVerbatim reports engine SQL this driver embeds that would not survive being sent.
func checkVerbatim(field, sql string) string {
	if strings.ContainsRune(sql, 0) {
		return fmt.Sprintf("%s holds a NUL byte, which ends the statement where it appears: a "+
			"query reaches the server as a C string", field)
	}

	return ""
}

// checkAction reports a referential action this driver has no PostgreSQL spelling for.
func checkAction(field string, a ir.ReferentialAction) string {
	if a == "" || nativeAction(a) != "" {
		return ""
	}

	return fmt.Sprintf("%s is %q, which this driver has no PostgreSQL spelling for", field, a)
}
