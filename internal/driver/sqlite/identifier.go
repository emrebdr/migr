package sqlite

import "strings"

// quote renders an identifier SQLite reads exactly as written, whatever it spells.
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
