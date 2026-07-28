package postgres

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

	return definition
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

// quoteLiteral renders a value as one string literal, which PostgreSQL coerces to the column's type.
func quoteLiteral(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}
