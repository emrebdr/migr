package schema

import (
	"fmt"
	"slices"
	"strings"
)

// M11-T6 replaced three per-driver catalog digests with this, and the difference is what it is a
// rendering of. A digest asked the engine to describe itself, so what came back was the engine's own
// text: PostgreSQL's five probes reassembled, MySQL's SHOW CREATE, SQLite's recorded CREATE TABLE.
// Two moments compared that way differ wherever the engine reworded itself -- which is why SQLite's
// round trip had to exempt the one table its rebuild touched, and why MySQL's had to sort its lines
// to survive a reversed drop_column appending a column where AFTER cannot put it back.
//
// This renders a snapshot instead, so there is one source of truth and this is a view of it rather
// than a second answer. What a snapshot holds is what a schema has and never how it is worded, so
// neither workaround has anything to be about: the comparison is Equal, and this is what a failure
// prints.
//
// It is here rather than in a test helper because three packages need it and Go shares a declaration
// no other way. Marshal is the canonical form for a machine; this is the one for a person.

// The width every line's kind is padded to, so the objects below it line up under each other.
const renderKindWidth = 11

// Render renders a snapshot as sorted lines, which is what a comparison of two moments prints.
func Render(s Snapshot) string {
	held := s.Canonical()

	var lines []string

	for _, table := range held.Tables {
		lines = append(lines, renderedTable(table)...)
	}

	for _, view := range held.Views {
		lines = append(lines, line("view", view.Name, quoted(view.Query), view.Beyond))
	}

	slices.Sort(lines)

	return strings.Join(lines, "\n")
}

// A table is rendered as itself and then as everything it holds, each line naming the table again.
// The table's own line is what tells a table emptied by drop_column from one that was dropped:
// every other line here describes contents, and an engine can hold a relation with no columns at
// all. Without it a rollback that dropped a table it was meant to empty would compare equal.

// renderedTable renders one table, its own line first and then one for everything it holds.
func renderedTable(table Table) []string {
	lines := []string{line("table", table.Name, "", table.Beyond)}

	for _, column := range table.Columns {
		lines = append(lines, line("column", table.Name+"."+column.Name,
			renderedColumn(column), column.Beyond))
	}

	if table.PrimaryKey != nil {
		lines = append(lines, line("primary_key", named(table.Name, table.PrimaryKey.Name),
			columns(table.PrimaryKey.Columns), table.PrimaryKey.Beyond))
	}

	return append(lines, renderedConstraints(table)...)
}

// renderedConstraints renders the four constraint kinds a table holds any number of.
func renderedConstraints(table Table) []string {
	var lines []string

	for _, index := range table.Indexes {
		lines = append(lines, line("index", named(table.Name, index.Name),
			columns(index.Columns)+unique(index.Unique), index.Beyond))
	}

	for _, one := range table.Uniques {
		lines = append(lines, line("unique", named(table.Name, one.Name), columns(one.Columns),
			one.Beyond))
	}

	for _, check := range table.Checks {
		lines = append(lines, line("check", named(table.Name, check.Name), quoted(check.Expression),
			check.Beyond))
	}

	for _, key := range table.ForeignKeys {
		lines = append(lines, line("foreign_key", named(table.Name, key.Name), references(key),
			key.Beyond))
	}

	return lines
}

// A column's type is rendered through ColumnType.String, so an exact one is the logical type alone
// and a guess says so -- int32? (integer) -- which is the whole of what a comparison can act on.

// renderedColumn renders a column's type, whether it accepts null, and what it defaults to.
func renderedColumn(column Column) string {
	rendered := column.Type.String() + " " + nullable(column.Nullable)

	if column.Default != nil {
		rendered += " default " + column.Default.String()
	}

	return rendered
}

// line renders one element as its kind, what it is called, what it holds, and what it exceeds.
func line(kind, subject, detail string, beyond *Unrepresented) string {
	rendered := fmt.Sprintf("%-*s %s", renderKindWidth, kind, subject)

	if detail != "" {
		rendered += " " + detail
	}

	if beyond != nil {
		rendered += " [beyond: " + beyond.Detail + "]"
	}

	return rendered
}

// named qualifies a constraint's name with its table, since two tables may each hold one unnamed.
func named(table, name string) string {
	if name == "" {
		return table + ".<unnamed>"
	}

	return table + " " + name
}

// columns renders an ordered column list, the order being part of every element that has one.
func columns(held []string) string {
	return "(" + strings.Join(held, ", ") + ")"
}

// references renders what a foreign key points at and what it does when that row moves.
func references(key ForeignKey) string {
	rendered := columns(key.Columns) + " -> " + key.ReferencesTable + columns(key.ReferencesColumns)

	if key.OnDelete != "" {
		rendered += " on delete " + string(key.OnDelete)
	}

	if key.OnUpdate != "" {
		rendered += " on update " + string(key.OnUpdate)
	}

	return rendered
}

// nullable names whether a column accepts null, which an unqualified column does not.
func nullable(accepts bool) string {
	if accepts {
		return "null"
	}

	return "not null"
}

// unique marks an index that enforces uniqueness, which ir.AddIndex cannot express and no driver sets.
func unique(enforces bool) string {
	if enforces {
		return " unique"
	}

	return ""
}

// quoted renders engine text so a body holding a newline stays one line of the rendering.
func quoted(text string) string {
	return fmt.Sprintf("%q", strings.Join(strings.Fields(text), " "))
}

// Two renderings are compared line by line, so what disagreed is what is printed. A failure that
// dumped both schemas whole would make an operator find the one line themselves, and on a database
// several test suites share that is most of a screen for a one-column difference.

// Difference renders the lines two renderings disagree on, which is what a failed comparison prints.
func Difference(want, got string) string {
	var out strings.Builder

	removed, added := Changes(want, got)

	for _, line := range removed {
		out.WriteString("  - " + line + "\n")
	}

	for _, line := range added {
		out.WriteString("  + " + line + "\n")
	}

	return out.String()
}

// Changes returns the lines of one rendering the next no longer holds, and the lines it gained.
func Changes(before, after string) ([]string, []string) {
	return absentFrom(before, after), absentFrom(after, before)
}

// absentFrom returns the lines of one rendering the other does not hold, blanks apart.
func absentFrom(from, other string) []string {
	held := make(map[string]bool)
	for _, line := range strings.Split(other, "\n") {
		held[line] = true
	}

	var missing []string

	for _, line := range strings.Split(from, "\n") {
		if line != "" && !held[line] {
			missing = append(missing, line)
		}
	}

	return missing
}
