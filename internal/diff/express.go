package diff

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// The two things in the snapshot model whose whole purpose is to be refused on are read here and
// nowhere else: the Unrepresented marker every element can carry, and a column type's certainty.
// Each predicate answers with the reason it cannot be expressed, and the empty string for one that
// can -- these are not failures, so none of them is an error.
//
// A marker is read as a lookup rather than as a report: an element that carries one and is identical
// in both schemas produces no operation, so it produces no refusal either. That is what makes
// diffing a database against itself empty however much of it migr could not have created.

// A marker's detail is passed through verbatim rather than framed, because it is written to follow a
// colon -- schema.Finding.Describe renders one that way and so does Refusal.Describe. Measured against
// a real PostgreSQL schema, wrapping it produced "the schema holds it carries index "x", whose key
// covers something that is not a column, which nothing here can represent, which no operation can
// express": broken grammar and the same clause twice, since every driver's detail already ends in one.

// markedBeyond reports a marker's own words, and nothing at all where there is no marker.
func markedBeyond(beyond *schema.Unrepresented) string {
	if beyond == nil {
		return ""
	}

	return beyond.Detail
}

// columnProblem reports what stops one column from being carried by an operation.
func columnProblem(column schema.Column) string {
	if marked := markedBeyond(column.Beyond); marked != "" {
		return marked
	}

	if logical, _ := column.Type.Logical(); logical.Zero() {
		return fmt.Sprintf("its type reads back as %s, which names no logical type at all, so no "+
			"operation can carry it", column.Type)
	}

	return defaultProblem(column.Default)
}

// defaultProblem reports a default an operation cannot carry, which is one that is engine SQL.
func defaultProblem(value *schema.ColumnDefault) string {
	if value == nil || strings.TrimSpace(value.Expression) == "" {
		return ""
	}

	return fmt.Sprintf("it defaults to the engine expression %s, and an operation's default carries "+
		"a literal or one of the neutral functions and never engine SQL", value.Expression)
}

// tableProblem reports what stops a whole table from being created or recreated as recorded.
func tableProblem(table schema.Table) string {
	if marked := markedBeyond(table.Beyond); marked != "" {
		return marked
	}

	if len(table.Columns) == 0 {
		return "it holds no columns, and a table definition must name at least one"
	}

	for _, column := range table.Columns {
		if problem := columnProblem(column); problem != "" {
			return fmt.Sprintf("its column %q cannot be carried: %s", column.Name, problem)
		}
	}

	return markedBeyond(primaryKeyBeyond(table.PrimaryKey))
}

// indexProblem reports what stops an index from being expressed by an operation.
func indexProblem(index schema.Index) string {
	if marked := markedBeyond(index.Beyond); marked != "" {
		return marked
	}

	if index.Unique {
		return "it enforces uniqueness, and add_index has no field for that: a unique element is a " +
			"unique constraint here, which is what every driver reports one as"
	}

	return ""
}

// uniqueProblem reports what stops a unique constraint from being expressed by an operation.
func uniqueProblem(unique schema.Unique) string {
	return markedBeyond(unique.Beyond)
}

// checkProblem reports what stops a check constraint from being expressed by an operation.
func checkProblem(check schema.Check) string {
	if marked := markedBeyond(check.Beyond); marked != "" {
		return marked
	}

	return unnamed(check.Name, "check constraint")
}

// foreignKeyProblem reports what stops a foreign key from being expressed by an operation.
func foreignKeyProblem(key schema.ForeignKey) string {
	if marked := markedBeyond(key.Beyond); marked != "" {
		return marked
	}

	return unnamed(key.Name, "foreign key")
}

// unnamed reports an element the engine names nothing, where the operation requires a name.
func unnamed(name, element string) string {
	if strings.TrimSpace(name) != "" {
		return ""
	}

	return fmt.Sprintf("this schema gives the %s no name, and the operation requires one: naming it "+
		"is what lets a later migration find it again", element)
}

// irColumn renders one snapshot column as the column an operation carries, problems aside.
func irColumn(column schema.Column) ir.Column {
	logical, _ := column.Type.Logical()

	return ir.Column{
		Name:     column.Name,
		Type:     logical,
		Nullable: column.Nullable,
		Default:  irDefault(column.Default),
	}
}

// irColumns renders a whole column list the same way, in the order the table holds them.
func irColumns(columns []schema.Column) []ir.Column {
	rendered := make([]ir.Column, 0, len(columns))
	for _, column := range columns {
		rendered = append(rendered, irColumn(column))
	}

	return rendered
}

// irDefault renders a default as an operation carries one, which engine SQL is never one of.
func irDefault(value *schema.ColumnDefault) *ir.Default {
	switch {
	case value == nil:
		return nil
	case value.Literal != nil:
		return ir.Literal(*value.Literal)
	case value.Function != "":
		return ir.Call(value.Function)
	default:
		return nil
	}
}

// irTableDef renders what a dropped table has to record for its own inverse to rebuild it.
func irTableDef(table schema.Table) *ir.TableDef {
	return &ir.TableDef{Columns: irColumns(table.Columns), PrimaryKey: keyColumns(table.PrimaryKey)}
}

// keyColumns names a primary key's columns, a table without one having none.
func keyColumns(key *schema.PrimaryKey) []string {
	if key == nil {
		return nil
	}

	return key.Columns
}

// primaryKeyBeyond returns a primary key's marker, a table without one having none.
func primaryKeyBeyond(key *schema.PrimaryKey) *schema.Unrepresented {
	if key == nil {
		return nil
	}

	return key.Beyond
}
