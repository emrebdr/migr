package sqlite

import (
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
)

// constraintKeywords open a table constraint, and are what tells one from a column definition.
var constraintKeywords = []string{"CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN"}

// columnConstraintKeywords open a column's own constraints, and so end its type name.
var columnConstraintKeywords = []string{
	"CONSTRAINT", "PRIMARY", "NOT", "NULL", "UNIQUE", "CHECK",
	"DEFAULT", "COLLATE", "REFERENCES", "GENERATED", "AS",
}

// modify applies one operation to a table's definition, or refuses with what it could not find.
func modify(table string, d tableDefinition, op ir.Operation) (tableDefinition, error) {
	switch o := op.(type) {
	case ir.AlterColumn:
		return alterColumn(table, d, o)
	case ir.AddCheck:
		return appendItem(d, constraint(o.Name, "CHECK ("+o.Expression+")")), nil
	case ir.DropCheck:
		return dropItem(table, d, o.Name, "CHECK")
	case ir.AddForeignKey:
		return appendItem(d, constraint(o.Name, foreignKeyClause(o))), nil
	case ir.DropForeignKey:
		return dropItem(table, d, o.Name, "FOREIGN")
	case ir.AddPrimaryKey:
		return addPrimaryKey(table, d, o)
	case ir.DropPrimaryKey:
		return dropItem(table, d, o.Name, "PRIMARY")
	default:
		return tableDefinition{}, &RebuildError{Table: table, Detail: fmt.Sprintf("this driver has "+
			"no rebuild for %s, and only the operations it declares emulated reach here", op.Kind())}
	}
}

// addPrimaryKey adds a key, refusing a table that already declares one however it declared it.
func addPrimaryKey(table string, d tableDefinition,
	op ir.AddPrimaryKey,
) (tableDefinition, error) {
	if where := existingKey(d.items); where != "" {
		return tableDefinition{}, &RebuildError{Table: table, Detail: fmt.Sprintf("it already "+
			"declares a primary key %s, and SQLite refuses a table with two. Drop the existing "+
			"key first, or alter the column that carries it", where)}
	}

	return appendItem(d, constraint(op.Name, "PRIMARY KEY ("+quoteAll(op.Columns)+")")), nil
}

// existingKey names where a definition already declares a primary key, and nothing when it does not.
func existingKey(items []string) string {
	for _, item := range items {
		found, isConstraint := classify(item)

		if isConstraint && found.keyword == "PRIMARY" {
			return "as a table constraint"
		}

		if !isConstraint && hasKeyword(item, "PRIMARY") {
			return fmt.Sprintf("on column %q", found.name)
		}
	}

	return ""
}

// hasKeyword reports whether a bare keyword appears in an item outside every quote and comment.
func hasKeyword(item, keyword string) bool {
	for i := 0; i < len(item); {
		width, _ := scan(item[i:])

		if isKeywordAt(item, i, []string{keyword}) {
			return true
		}

		i += width
	}

	return false
}

// constraint renders a table constraint, named where the operation named one.
func constraint(name, body string) string {
	if name == "" {
		return body
	}

	return "CONSTRAINT " + quote(name) + " " + body
}

// foreignKeyClause renders a foreign key as a table constraint, with the actions it declares.
func foreignKeyClause(op ir.AddForeignKey) string {
	clause := "FOREIGN KEY (" + quoteAll(op.Columns) + ") REFERENCES " +
		quote(op.ReferencesTable) + " (" + quoteAll(op.ReferencesColumns) + ")"

	if action := nativeAction(op.OnDelete); action != "" {
		clause += " ON DELETE " + action
	}

	if action := nativeAction(op.OnUpdate); action != "" {
		clause += " ON UPDATE " + action
	}

	return clause
}

// appendItem adds a table constraint after everything the definition already declares.
func appendItem(d tableDefinition, item string) tableDefinition {
	d.items = append(slices.Clone(d.items), item)

	return d
}

// dropItem removes the one item an operation names, refusing when the definition carries no such one.
func dropItem(table string, d tableDefinition, name, keyword string) (tableDefinition, error) {
	at := findItem(d.items, name, keyword)
	if at < 0 {
		return tableDefinition{}, &RebuildError{Table: table, Detail: describeMissing(name, keyword)}
	}

	d.items = slices.Delete(slices.Clone(d.items), at, at+1)

	return d, nil
}

// findItem returns the item a named constraint sits at, or the first of a keyword when unnamed.
func findItem(items []string, name, keyword string) int {
	for i, item := range items {
		found, isConstraint := classify(item)
		if !isConstraint {
			continue
		}

		if name != "" && strings.EqualFold(found.name, name) {
			return i
		}

		if name == "" && found.keyword == keyword {
			return i
		}
	}

	return -1
}

// describeMissing states what the definition does not carry, since that is what stopped the rebuild.
func describeMissing(name, keyword string) string {
	if name != "" {
		return fmt.Sprintf("its definition declares no constraint named %q, and a rebuild changes "+
			"what the definition says rather than what the catalog reports -- an unnamed constraint "+
			"cannot be dropped by name, because SQLite records no name to match", name)
	}

	return fmt.Sprintf("its definition declares no table-level %s constraint, and this operation "+
		"names none to look for. A key declared on the column itself is part of that column's "+
		"definition; dropping it changes the column, which alter_column is for", keyword)
}

// classified is what one item of a column list declares, read only as far as identifying it.
type classified struct {
	// name is the constraint's declared name, or the column's, and is blank for an unnamed one.
	name string
	// keyword is the constraint this item opens, upper-cased, and blank for a column definition.
	keyword string
}

// classify reads one item far enough to say what it is, and never far enough to rewrite it.
func classify(item string) (classified, bool) {
	first, rest := word(item)
	upper := strings.ToUpper(first)

	if strings.EqualFold(first, "CONSTRAINT") {
		name, after := word(rest)
		keyword, _ := word(after)

		return classified{name: unquote(name), keyword: strings.ToUpper(keyword)}, true
	}

	if slices.Contains(constraintKeywords, upper) {
		return classified{keyword: upper}, true
	}

	return classified{name: unquote(first)}, false
}

// word returns the first token of s and what follows it, treating a quoted run as one token.
func word(s string) (string, string) {
	s = strings.TrimLeft(s, " \t\r\n")
	if s == "" {
		return "", ""
	}

	width, _ := scan(s)
	if width > 1 || !isBare(s[0]) {
		return s[:width], s[width:]
	}

	for i := 1; i < len(s); i++ {
		if !isBare(s[i]) {
			return s[:i], s[i:]
		}
	}

	return s, ""
}

// isBare reports whether a byte can appear in an identifier or keyword nobody quoted.
func isBare(b byte) bool {
	return b == '_' || b == '$' || b >= 0x80 ||
		b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// unquote strips whichever delimiter an identifier was written with, and undoubles what is inside.
func unquote(token string) string {
	if len(token) < 2 {
		return token
	}

	switch token[0] {
	case doubleQuote, backtick, singleQuote:
		inner := token[1 : len(token)-1]

		return strings.ReplaceAll(inner, string(token[0])+string(token[0]), string(token[0]))
	case openBracket:
		return token[1 : len(token)-1]
	default:
		return token
	}
}
