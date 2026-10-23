package visual

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// mermaidType is the whole of what Mermaid's ER grammar accepts as an attribute type.
var mermaidType = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_\-\[\]()]*$`)

// mermaidName is a table or column name Mermaid reads bare, anything else being quoted.
var mermaidName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_\-]*$`)

// MermaidER renders a schema as a Mermaid entity-relationship diagram: tables, columns and foreign keys.
func MermaidER(s schema.Snapshot) string {
	held := s.Canonical()

	var out strings.Builder

	out.WriteString("erDiagram\n")

	for _, t := range held.Tables {
		fmt.Fprintf(&out, "  %s {\n", mermaidEntity(t.Name))

		for _, c := range t.Columns {
			out.WriteString("    " + mermaidAttribute(t, c) + "\n")
		}

		out.WriteString("  }\n")
	}

	for _, t := range held.Tables {
		for _, k := range t.ForeignKeys {
			fmt.Fprintf(&out, "  %s }o--%s %s : \"%s\"\n", mermaidEntity(t.Name), mermaidParent(t, k),
				mermaidEntity(k.ReferencesTable), mermaidComment(strings.Join(k.Columns, ", ")))
		}
	}

	return out.String()
}

// mermaidEntity writes a table's name the way Mermaid reads it, quoted where it is not a plain word.
func mermaidEntity(name string) string {
	if mermaidName.MatchString(name) {
		return name
	}

	return `"` + mermaidComment(name) + `"`
}

// mermaidAttribute writes one column: its type, its name, its keys, and what the type could not say.
func mermaidAttribute(t schema.Table, c schema.Column) string {
	spelled := c.Type.String()
	kind, _, _ := strings.Cut(spelled, "(")

	var notes []string

	if !mermaidType.MatchString(spelled) {
		notes = append(notes, spelled)
		spelled = kind
	}

	if c.Nullable {
		notes = append(notes, "null")
	}

	line := spelled + " " + mermaidColumn(c.Name)

	if keys := keysOf(t, c.Name); len(keys) > 0 {
		line += " " + strings.Join(keys, ",")
	}

	if len(notes) > 0 {
		line += ` "` + mermaidComment(strings.Join(notes, ", ")) + `"`
	}

	return line
}

// mermaidColumn writes a column name, which Mermaid cannot quote, with anything it refuses replaced.
func mermaidColumn(name string) string {
	if mermaidName.MatchString(name) {
		return name
	}

	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			return r
		}

		return '_'
	}, name)
}

// mermaidComment escapes text for a quoted Mermaid string, which ends at the first quote.
func mermaidComment(text string) string {
	return strings.ReplaceAll(text, `"`, "'")
}

// mermaidParent is the referenced end of a foreign key: exactly one row, or none where a column may be null.
func mermaidParent(t schema.Table, k schema.ForeignKey) string {
	for _, c := range t.Columns {
		if c.Nullable && slices.Contains(k.Columns, c.Name) {
			return "o|"
		}
	}

	return "||"
}

// keysOf names the keys one column takes part in, as an ER diagram marks them.
func keysOf(t schema.Table, column string) []string {
	var keys []string

	if t.PrimaryKey != nil && slices.Contains(t.PrimaryKey.Columns, column) {
		keys = append(keys, "PK")
	}

	for _, k := range t.ForeignKeys {
		if slices.Contains(k.Columns, column) {
			keys = append(keys, "FK")

			break
		}
	}

	for _, u := range t.Uniques {
		if slices.Contains(u.Columns, column) {
			keys = append(keys, "UK")

			break
		}
	}

	return keys
}

// DOTER renders a schema as a Graphviz digraph, a table per record and a foreign key per edge.
func DOTER(s schema.Snapshot) string {
	held := s.Canonical()

	var out strings.Builder

	out.WriteString("digraph schema {\n  rankdir=LR;\n  node [shape=record];\n")

	for _, t := range held.Tables {
		fields := []string{dotRecord(t.Name)}

		for _, c := range t.Columns {
			fields = append(fields, dotRecord(dotColumn(t, c)))
		}

		fmt.Fprintf(&out, "  %s [label=\"{%s}\"];\n", dotID(t.Name), strings.Join(fields, "|"))
	}

	for _, t := range held.Tables {
		for _, k := range t.ForeignKeys {
			fmt.Fprintf(&out, "  %s -> %s [label=%s];\n", dotID(t.Name), dotID(k.ReferencesTable),
				dotID(strings.Join(k.Columns, ", ")))
		}
	}

	out.WriteString("}\n")

	return out.String()
}

// dotColumn is one record field: the column, its type and nullability, and its keys.
func dotColumn(t schema.Table, c schema.Column) string {
	field := c.Name + " : " + c.Type.String()

	if c.Nullable {
		field += " null"
	}

	if keys := keysOf(t, c.Name); len(keys) > 0 {
		field += " [" + strings.Join(keys, ",") + "]"
	}

	return field
}

// dotRecord escapes text for a field of a Graphviz record label, where braces, bars and angles are syntax.
func dotRecord(text string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "{", `\{`, "}", `\}`, "|", `\|`, "<", `\<`, ">", `\>`,
		" ", `\ `).Replace(text)
}
