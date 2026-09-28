package sqlite

import (
	"fmt"
	"strings"
)

// The delimiters a CREATE TABLE body can carry, each of which suspends every rule but its own end.
const (
	singleQuote = '\''
	doubleQuote = '"'
	backtick    = '`'
	openBracket = '['
	shutBracket = ']'
	openParen   = '('
	shutParen   = ')'
	comma       = ','
)

// tableDefinition is one CREATE TABLE statement taken apart far enough to change one item of it.
type tableDefinition struct {
	// prefix is everything before the opening parenthesis, which names the table this replaces.
	prefix string
	// items are the column definitions and table constraints, each exactly as it was written.
	items []string
	// suffix is everything after the closing parenthesis, which is WITHOUT ROWID and STRICT.
	suffix string
}

// render writes the definition back out under a name, which is how the shadow and the final differ.
func (d tableDefinition) render(table string) string {
	lines := make([]string, len(d.items))
	for i, item := range d.items {
		lines[i] = indent + strings.TrimSpace(item)
	}

	return "CREATE TABLE " + quote(table) + " (\n" + strings.Join(lines, ",\n") + "\n)" + d.suffix
}

// splitTable takes a recorded CREATE TABLE apart, refusing one this driver cannot put back together.
func splitTable(table, statement string) (tableDefinition, error) {
	open := indexAtDepth(statement, openParen)
	if open < 0 {
		return tableDefinition{}, &RebuildError{Table: table, Detail: fmt.Sprintf("SQLite records "+
			"its definition as %q, which carries no column list this driver can find: a table "+
			"created with AS SELECT has none", statement)}
	}

	shut := matchingParen(statement, open)
	if shut < 0 {
		return tableDefinition{}, &RebuildError{Table: table, Detail: "its recorded definition " +
			"opens a column list that nothing closes, so this driver will not guess where it ends"}
	}

	items, err := splitItems(table, statement[open+1:shut])
	if err != nil {
		return tableDefinition{}, err
	}

	return tableDefinition{
		prefix: statement[:open],
		items:  items,
		suffix: statement[shut+1:],
	}, nil
}

// splitItems divides a column list on the commas that separate its items, and on no others.
func splitItems(table, body string) ([]string, error) {
	var items []string

	start, depth := 0, 0

	for i := 0; i < len(body); {
		width, kind := scan(body[i:])

		switch {
		case kind == tokenOpen:
			depth++
		case kind == tokenShut:
			depth--
		case kind == tokenComma && depth == 0:
			items = append(items, body[start:i])
			start = i + width
		}

		i += width
	}

	items = append(items, body[start:])

	return checkItems(table, items)
}

// checkItems refuses a column list with a hole in it, which a trailing comma leaves.
func checkItems(table string, items []string) ([]string, error) {
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			return nil, &RebuildError{Table: table, Detail: "its recorded definition has an empty " +
				"item in its column list, which this driver will not guess the meaning of"}
		}
	}

	return items, nil
}

// token names what one scanned run of bytes is, for the only three the splitter acts on.
type token int

// The tokens the splitter distinguishes; everything else is text it steps over.
const (
	tokenText token = iota
	tokenOpen
	tokenShut
	tokenComma
)

// scan measures the run of bytes at the front of s, so no delimiter's contents are ever read.
func scan(s string) (int, token) {
	switch s[0] {
	case singleQuote, doubleQuote, backtick:
		return quoted(s, s[0], s[0]), tokenText
	case openBracket:
		return quoted(s, openBracket, shutBracket), tokenText
	case openParen:
		return 1, tokenOpen
	case shutParen:
		return 1, tokenShut
	case comma:
		return 1, tokenComma
	default:
		return commentOrByte(s)
	}
}

// commentOrByte measures a comment where one starts here, and one byte where none does.
func commentOrByte(s string) (int, token) {
	if strings.HasPrefix(s, "--") {
		if end := strings.IndexByte(s, '\n'); end >= 0 {
			return end + 1, tokenText
		}

		return len(s), tokenText
	}

	if strings.HasPrefix(s, "/*") {
		if end := strings.Index(s[2:], "*/"); end >= 0 {
			return end + 4, tokenText
		}

		return len(s), tokenText
	}

	return 1, tokenText
}

// quoted measures a delimited run, where SQL doubles the closing delimiter to escape it.
func quoted(s string, open, shut byte) int {
	for i := 1; i < len(s); i++ {
		if s[i] != shut {
			continue
		}

		if open != shut || i+1 >= len(s) || s[i+1] != shut {
			return i + 1
		}

		i++
	}

	return len(s)
}

// indexAtDepth returns where a byte first appears outside every quote and comment, or -1.
func indexAtDepth(s string, want byte) int {
	for i := 0; i < len(s); {
		width, _ := scan(s[i:])
		if width == 1 && s[i] == want {
			return i
		}

		i += width
	}

	return -1
}

// matchingParen returns where the parenthesis opened at start is closed, or -1 if nothing closes it.
func matchingParen(s string, start int) int {
	depth := 0

	for i := start; i < len(s); {
		width, kind := scan(s[i:])

		switch kind {
		case tokenOpen:
			depth++
		case tokenShut:
			depth--

			if depth == 0 {
				return i
			}
		case tokenText, tokenComma:
		}

		i += width
	}

	return -1
}
