package sqlite

import (
	"strings"

	"github.com/emrebdr/migr/internal/ir"
)

// columnDefinition renders one column as CREATE TABLE and ADD COLUMN both spell it.
func columnDefinition(c ir.Column) string {
	definition := quote(c.Name) + " " + nativeType(c.Type)

	if !c.Nullable {
		definition += " NOT NULL"
	}

	if clause := defaultClause(c.Default); clause != "" {
		definition += " " + clause
	}

	if clause := labelCheck(c); clause != "" {
		definition += " " + clause
	}

	return definition
}

// labelCheck renders what holds an enum column to its labels, since SQLite has no enum type.
func labelCheck(c ir.Column) string {
	labels, isEnum := c.Type.Values()
	if !isEnum {
		return ""
	}

	return "CHECK (" + quote(c.Name) + " IN (" + literalList(labels) + "))"
}

// literalList renders label values as the string literals a CHECK compares against.
func literalList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = quoteLiteral(value)
	}

	return strings.Join(quoted, ", ")
}

// defaultClause renders a column's default, and nothing when it has none.
func defaultClause(d *ir.Default) string {
	if d == nil {
		return ""
	}

	if d.Literal != nil {
		return "DEFAULT " + quoteLiteral(*d.Literal)
	}

	return "DEFAULT " + nativeFunction(d.Function)
}

// quoteLiteral renders a value as one string literal, which SQLite coerces to the column's affinity.
func quoteLiteral(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}
