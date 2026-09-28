package sqlite

import (
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
)

// columnParts is one column definition taken apart far enough to change what alter_column changes.
type columnParts struct {
	// name is the column's name exactly as it was written, quoting included.
	name string
	// typeName is its declared type, which may be empty since SQLite does not require one.
	typeName string
	// constraints is everything after the type, which is copied through except where it is changed.
	constraints string
}

// alterColumn rewrites one column's type, nullability and default, and copies the rest through.
func alterColumn(table string, d tableDefinition, op ir.AlterColumn) (tableDefinition, error) {
	at := findColumn(d.items, op.Column)
	if at < 0 {
		return tableDefinition{}, &RebuildError{Table: table, Detail: fmt.Sprintf("its definition "+
			"declares no column named %q", op.Column)}
	}

	parts := splitColumn(d.items[at])

	changed, err := applyAlteration(table, parts, op)
	if err != nil {
		return tableDefinition{}, err
	}

	d.items = slices.Clone(d.items)
	d.items[at] = changed.render()

	return d, nil
}

// findColumn returns the item one column is declared at, or -1 when the definition declares none.
func findColumn(items []string, column string) int {
	for i, item := range items {
		found, isConstraint := classify(item)
		if !isConstraint && strings.EqualFold(found.name, column) {
			return i
		}
	}

	return -1
}

// render writes a column definition back out, with a single space between each part it kept.
func (p columnParts) render() string {
	parts := []string{p.name}

	for _, part := range []string{p.typeName, p.constraints} {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}

	return strings.Join(parts, " ")
}

// splitColumn takes a column definition apart at the first keyword that opens a column constraint.
func splitColumn(item string) columnParts {
	name, rest := word(item)
	at := indexOfConstraint(rest)

	return columnParts{name: name, typeName: rest[:at], constraints: rest[at:]}
}

// indexOfConstraint returns where a column's constraints begin, which is where its type ends.
func indexOfConstraint(s string) int {
	for i := 0; i < len(s); {
		width, kind := scan(s[i:])

		if kind == tokenOpen {
			i = skipParens(s, i)

			continue
		}

		if isKeywordAt(s, i, columnConstraintKeywords) {
			return i
		}

		i += width
	}

	return len(s)
}

// applyAlteration rewrites the parts an operation names, refusing what it cannot rewrite.
func applyAlteration(table string, parts columnParts, op ir.AlterColumn) (columnParts, error) {
	if op.Type != nil {
		parts.typeName = " " + nativeType(*op.Type)
	}

	if op.Nullable != nil {
		parts.constraints = setNotNull(parts.constraints, !*op.Nullable)
	}

	if op.Default != nil || op.DropDefault {
		constraints, err := setDefault(table, parts.constraints, op)
		if err != nil {
			return columnParts{}, err
		}

		parts.constraints = constraints
	}

	return parts, nil
}

// setNotNull adds or removes the one clause that decides whether a column accepts no value.
func setNotNull(constraints string, notNull bool) string {
	at, width := findClause(constraints, "NOT")
	if at >= 0 {
		constraints = constraints[:at] + constraints[at+width:]
	}

	if !notNull {
		return constraints
	}

	return strings.TrimRight(constraints, " \t\r\n") + " NOT NULL"
}

// setDefault replaces or removes a column's default, refusing one this driver cannot render.
func setDefault(table, constraints string, op ir.AlterColumn) (string, error) {
	at, width := findClause(constraints, "DEFAULT")
	if at >= 0 {
		constraints = constraints[:at] + constraints[at+width:]
	}

	if op.DropDefault {
		return constraints, nil
	}

	clause := defaultClause(op.Default)
	if clause == "" {
		return "", &RebuildError{Table: table, Detail: fmt.Sprintf("column %q would default to "+
			"something this driver has no SQLite spelling for", op.Column)}
	}

	return strings.TrimRight(constraints, " \t\r\n") + " " + clause, nil
}

// findClause returns where one column constraint starts and how wide it is, or -1 for absent.
func findClause(s, keyword string) (int, int) {
	for i := 0; i < len(s); {
		width, kind := scan(s[i:])

		if kind == tokenOpen {
			i = skipParens(s, i)

			continue
		}

		if isKeywordAt(s, i, []string{keyword}) {
			return i, clauseWidth(s[i:], keyword)
		}

		i += width
	}

	return -1, 0
}

// clauseWidth measures one whole column constraint, so removing it leaves nothing of it behind.
func clauseWidth(s, keyword string) int {
	_, rest := word(s)
	consumed := len(s) - len(rest)

	if keyword == "NOT" {
		_, after := word(rest)

		return len(s) - len(after)
	}

	return consumed + defaultValueWidth(rest)
}

// defaultValueWidth measures the value a DEFAULT carries, which is one token or one parenthesised run.
func defaultValueWidth(s string) int {
	trimmed := strings.TrimLeft(s, " \t\r\n")
	leading := len(s) - len(trimmed)

	if trimmed == "" {
		return leading
	}

	if trimmed[0] == openParen {
		return leading + skipParens(trimmed, 0)
	}

	if trimmed[0] == '+' || trimmed[0] == '-' {
		_, after := word(trimmed[1:])

		return leading + 1 + len(trimmed) - 1 - len(after)
	}

	_, after := word(trimmed)

	return leading + len(trimmed) - len(after)
}

// skipParens returns where the parenthesised run opening at start ends, or the end of the string.
func skipParens(s string, start int) int {
	if shut := matchingParen(s, start); shut >= 0 {
		return shut + 1
	}

	return len(s)
}

// isKeywordAt reports whether one of the given bare keywords begins at i and ends a token there.
func isKeywordAt(s string, i int, keywords []string) bool {
	if i > 0 && isBare(s[i-1]) {
		return false
	}

	token, _ := word(s[i:])

	return slices.Contains(keywords, strings.ToUpper(token))
}
