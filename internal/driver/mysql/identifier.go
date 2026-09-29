package mysql

import "strings"

// quote renders an identifier MySQL reads exactly as written, whatever it spells.
func quote(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// quoteAll renders a column list in the order the operation declared it.
func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quote(name)
	}

	return strings.Join(quoted, ", ")
}

// quoteLiteral renders a value as one string literal, under the grammar this session pins (doc.go).
func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// literalList renders values as the string literals an enum declaration lists.
func literalList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = quoteLiteral(value)
	}

	return strings.Join(quoted, ", ")
}
