package sqlite

import (
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// The keywords a recorded item is read for, each of which the pragmas answer nothing about.
const (
	keywordPrimary = "PRIMARY"
	keywordCheck   = "CHECK"
	keywordForeign = "FOREIGN"
	keywordKey     = "KEY"
)

// item is one entry of a recorded column list, read far enough to say what it declares.
type item struct {
	// text is the entry exactly as SQLite recorded it.
	text string
	// name is the constraint's declared name, or the column's own, and is empty for an unnamed one.
	name string
	// keyword is the table constraint this entry opens, upper-cased, and empty for a column.
	keyword string
}

// classifyItems reads every entry of a recorded column list far enough to find things in it.
func classifyItems(items []string) []item {
	read := make([]item, 0, len(items))

	for _, text := range items {
		found, _ := classify(text)
		read = append(read, item{text: text, name: found.name, keyword: found.keyword})
	}

	return read
}

// columnItem returns the entry declaring one column, which is where its own constraints live.
func columnItem(items []item, column string) (item, bool) {
	for _, found := range items {
		if found.keyword == "" && found.name == column {
			return found, true
		}
	}

	return item{}, false
}

// constraintName returns the name the definition gives the first constraint of a keyword.
func constraintName(items []item, keyword string) string {
	for _, found := range items {
		if found.keyword == keyword {
			return found.name
		}
	}

	return ""
}

// SQLite has no catalog for a check at all: its expression exists only inside the CREATE TABLE
// statement, so a table whose definition this driver cannot take apart reports none and says so.

// checksOf lists the check constraints a recorded definition declares, table-level and column-level.
func checksOf(items []item, columns []schema.Column) []schema.Check {
	var found []schema.Check

	for _, declared := range items {
		if declared.keyword == keywordCheck {
			found = append(found, namedCheck(declared))

			continue
		}

		if declared.keyword == "" {
			found = append(found, columnCheck(declared, columns)...)
		}
	}

	return found
}

// namedCheck reads a table-level check back under whatever name the definition gave it.
func namedCheck(declared item) schema.Check {
	body, _ := checkBody(declared.text)

	return schema.Check{Name: declared.name, Expression: body}
}

// columnCheck reads a column's own check back, and reports none for one absorbed into its type.
func columnCheck(declared item, columns []schema.Column) []schema.Check {
	body, declares := checkBody(declared.text)
	if !declares {
		return nil
	}

	if labels := absorbed(declared.name, nativeOf(columns, declared.name), declared); labels != nil {
		return nil
	}

	return []schema.Check{{Expression: body}}
}

// nativeOf returns the spelling the catalog answered for one column, which decides what absorbs.
func nativeOf(columns []schema.Column, column string) string {
	for _, found := range columns {
		if found.Name == column {
			return found.Type.Native()
		}
	}

	return ""
}

// labelCheck renders an enum as CHECK ("name" IN ('a', 'b')) because SQLite has no enum type, so
// reporting that check as a constraint would have a differ propose dropping the column's own type.
// The match is against this driver's exact rendering and nothing looser: a hand-written check that
// is byte-identical to it is indistinguishable from an enum, which is why the type stays ambiguous.

// absorbed returns the enum labels a column's own check holds, where that check is this driver's own.
func absorbed(column, declared string, own item) []string {
	if base, _ := splitDeclared(declared); base != textType {
		return nil
	}

	body, declares := checkBody(own.text)
	if !declares {
		return nil
	}

	return labelList(column, body)
}

// labelList reads a check body back as the label list labelCheck renders, and nothing else.
func labelList(column, body string) []string {
	prefix := quote(column) + " IN ("
	if !strings.HasPrefix(body, prefix) || !strings.HasSuffix(body, string(shutParen)) {
		return nil
	}

	read := make([]string, 0, 1)

	for _, label := range strings.Split(body[len(prefix):len(body)-1], ", ") {
		value, quoted := unquoteLiteral(label)
		if !quoted {
			return nil
		}

		read = append(read, value)
	}

	return read
}

// checkBody returns the condition a CHECK declares inside one recorded entry, where it declares one.
func checkBody(text string) (string, bool) {
	after, declares := afterKeyword(text, keywordCheck)
	if !declares {
		return "", false
	}

	return parenBody(after)
}

// declaredKeyColumns returns the columns a table-level FOREIGN KEY entry names, in order.
func declaredKeyColumns(text string) []string {
	after, declares := afterKeyword(text, keywordForeign)
	if !declares {
		return nil
	}

	after, named := afterKeyword(after, keywordKey)
	if !named {
		return nil
	}

	body, listed := parenBody(after)
	if !listed {
		return nil
	}

	return unquoteList(body)
}

// unquoteList reads a comma-separated list of identifiers back, whatever each was quoted with.
func unquoteList(body string) []string {
	parts := strings.Split(body, ",")

	read := make([]string, 0, len(parts))
	for _, part := range parts {
		read = append(read, unquote(strings.TrimSpace(part)))
	}

	return read
}

// parenBody returns what the first parenthesised group of s holds, outside every quote.
func parenBody(s string) (string, bool) {
	open := indexAtDepth(s, openParen)
	if open < 0 {
		return "", false
	}

	shut := matchingParen(s, open)
	if shut < 0 {
		return "", false
	}

	return strings.TrimSpace(s[open+1 : shut]), true
}

// declaresWords reports whether a recorded fragment names these words as adjacent bare tokens.
func declaresWords(text string, words ...string) bool {
	after, found := afterKeyword(text, words[0])
	if !found {
		return false
	}

	for _, wanted := range words[1:] {
		token, rest := word(after)
		if !strings.EqualFold(token, wanted) {
			return false
		}

		after = rest
	}

	return true
}

// afterKeyword returns what follows the first bare occurrence of a keyword outside every quote.
func afterKeyword(text, keyword string) (string, bool) {
	rest := text

	for rest != "" {
		token, after := word(rest)
		if token == "" {
			return "", false
		}

		if strings.EqualFold(token, keyword) {
			return after, true
		}

		rest = after
	}

	return "", false
}
