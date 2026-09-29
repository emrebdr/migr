package mysql

import "github.com/emrebdr/migr/internal/ir"

// columnDefinition renders one column as CREATE TABLE spells it.
func columnDefinition(c ir.Column) string {
	definition := quote(c.Name) + " " + nativeType(c.Type)

	if !c.Nullable {
		definition += " NOT NULL"
	}

	if clause := defaultClause(c); clause != "" {
		definition += " " + clause
	}

	return definition
}

// defaultClause renders a column's default, and nothing when it has none.
func defaultClause(c ir.Column) string {
	if c.Default == nil {
		return ""
	}

	if c.Default.Literal != nil {
		return "DEFAULT " + literalDefault(c, *c.Default.Literal)
	}

	return "DEFAULT " + nativeFunction(c.Default.Function)
}

// literalDefault renders a literal default, as an expression where MySQL takes no bare one.
func literalDefault(c ir.Column, literal string) string {
	if needsExpressionDefault(c.Type) {
		return "(" + quoteLiteral(literal) + ")"
	}

	return quoteLiteral(literal)
}

// isExpressionDefault reports a column whose default MySQL only accepts as an expression.
func isExpressionDefault(c ir.Column) bool {
	if c.Default == nil {
		return false
	}

	if c.Default.Literal != nil {
		return needsExpressionDefault(c.Type)
	}

	return expressionFunction(c.Default.Function)
}
