package postgres

import "strings"

// maxIdentifier is NAMEDATALEN - 1, the longest name PostgreSQL keeps whole.
const maxIdentifier = 63

// quote renders an identifier PostgreSQL reads exactly as written, whatever it spells.
func quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteAll renders a column list in the order the operation declared it.
func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quote(name)
	}

	return strings.Join(quoted, ", ")
}
