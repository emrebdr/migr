package mysql

import "github.com/emrebdr/migr/internal/ir"

// columnDefinition renders one column as CREATE TABLE and ADD COLUMN spell it.
func columnDefinition(c ir.Column) string {
	definition := quote(c.Name) + " " + nativeType(c.Type) + characterSetClause(c.Type)

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
	return renderDefault(c.Default, needsExpressionDefault(c.Type))
}

// renderDefault renders a default clause, parenthesising a literal where MySQL takes no bare one.
func renderDefault(d *ir.Default, expression bool) string {
	if d == nil {
		return ""
	}

	if d.Literal != nil {
		return "DEFAULT " + literalDefault(*d.Literal, expression)
	}

	return "DEFAULT " + nativeFunction(d.Function)
}

// literalDefault renders a literal default, as an expression where MySQL takes no bare one.
func literalDefault(literal string, expression bool) string {
	if expression {
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

// nullability renders the clause a redefinition states, which must be stated in both directions.
func nullability(nullable bool) string {
	if nullable {
		return "NULL"
	}

	return "NOT NULL"
}
