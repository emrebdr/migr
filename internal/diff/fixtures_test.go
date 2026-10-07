package diff

import (
	"testing"

	"github.com/emrebdr/migr/internal/schema"
)

// exact returns a column type a catalog answered precisely, which most of these fixtures carry.
func exact(t *testing.T, spelling string) schema.ColumnType {
	t.Helper()

	built, err := schema.ExactType(parse(t, spelling), spelling)
	if err != nil {
		t.Fatalf("ExactType(%q): %v", spelling, err)
	}

	return built
}

// ambiguous returns a column type whose native spelling names several logical types, best first.
func ambiguous(t *testing.T, spelling, native string) schema.ColumnType {
	t.Helper()

	built, err := schema.AmbiguousType(parse(t, spelling), native)
	if err != nil {
		t.Fatalf("AmbiguousType(%q, %q): %v", spelling, native, err)
	}

	return built
}

// opaque returns a column type naming no logical type at all, which is what a tsvector gets.
func opaque(t *testing.T, native string) schema.ColumnType {
	t.Helper()

	built, err := schema.UnknownType(native)
	if err != nil {
		t.Fatalf("UnknownType(%q): %v", native, err)
	}

	return built
}

// parse parses one of these fixtures' logical types, which are all written out in this package.
func parse(t *testing.T, spelling string) schema.Type {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	return parsed
}

// literal returns a default holding one value, which is what distinguishes empty from absent.
func literal(v string) *schema.ColumnDefault {
	return &schema.ColumnDefault{Literal: &v}
}

// marked returns the marker a driver puts on an element the operations cannot express.
func marked(detail string) *schema.Unrepresented {
	return &schema.Unrepresented{Detail: detail}
}

// empty is the schema of a namespace holding nothing, which an unmigrated database is.
func empty() schema.Snapshot {
	return schema.Snapshot{}
}

// users returns the one-table schema most of these cases change exactly one thing about.
func users(t *testing.T) schema.Snapshot {
	t.Helper()

	return schema.Snapshot{Tables: []schema.Table{usersTable(t)}}
}

// usersTable is the referenced table, keyed on a column every other fixture points at.
func usersTable(t *testing.T) schema.Table {
	t.Helper()

	return schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: exact(t, "int64")},
			{Name: "email", Type: exact(t, "string(255)")},
			{Name: "note", Type: exact(t, "text"), Nullable: true, Default: literal("")},
		},
		PrimaryKey: &schema.PrimaryKey{Name: "users_pkey", Columns: []string{"id"}},
	}
}

// ordersTable is the referencing table, carrying one of every constraint the operations can express.
func ordersTable(t *testing.T) schema.Table {
	t.Helper()

	return schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: exact(t, "int64")},
			{Name: "code", Type: exact(t, "string(32)")},
			{Name: "total", Type: exact(t, "int32"), Nullable: true},
			{Name: "user_id", Type: exact(t, "int64"), Nullable: true},
		},
		PrimaryKey: &schema.PrimaryKey{Name: "orders_pkey", Columns: []string{"id"}},
		Indexes:    []schema.Index{{Name: "orders_total_idx", Columns: []string{"total"}}},
		Uniques:    []schema.Unique{{Name: "orders_code_key", Columns: []string{"code"}}},
		Checks:     []schema.Check{{Name: "orders_total_positive", Expression: "total > 0"}},
		ForeignKeys: []schema.ForeignKey{{
			Name: "orders_user_fk", Columns: []string{"user_id"},
			ReferencesTable: "users", ReferencesColumns: []string{"id"},
			OnDelete: schema.ActionCascade, OnUpdate: schema.ActionNoAction,
		}},
	}
}

// both returns the two-table schema, which is where every constraint operation comes from.
func both(t *testing.T) schema.Snapshot {
	t.Helper()

	return schema.Snapshot{Tables: []schema.Table{usersTable(t), ordersTable(t)}}
}

// withView returns a schema holding one view as well, whose body is engine text and compares as it.
func withView(t *testing.T, body string) schema.Snapshot {
	t.Helper()

	snapshot := users(t)
	snapshot.Views = []schema.View{{Name: "active_users", Query: body}}

	return snapshot
}

// changed returns one schema with one table rewritten, which is how these cases state a difference.
func changed(snapshot schema.Snapshot, name string, rewrite func(schema.Table) schema.Table) schema.Snapshot {
	rewritten := snapshot.Canonical()

	for i := range rewritten.Tables {
		if rewritten.Tables[i].Name == name {
			rewritten.Tables[i] = rewrite(rewritten.Tables[i])
		}
	}

	return rewritten
}

// withColumn returns a table carrying one more column than it did.
func withColumn(table schema.Table, column schema.Column) schema.Table {
	table.Columns = append(table.Columns, column)

	return table
}

// withoutColumn returns a table with one of its columns gone.
func withoutColumn(table schema.Table, name string) schema.Table {
	kept := make([]schema.Column, 0, len(table.Columns))

	for _, column := range table.Columns {
		if column.Name != name {
			kept = append(kept, column)
		}
	}

	table.Columns = kept

	return table
}

// rewriting returns a table with one of its columns replaced by what a rewrite makes of it.
func rewriting(table schema.Table, name string, rewrite func(schema.Column) schema.Column) schema.Table {
	rewritten := make([]schema.Column, 0, len(table.Columns))

	for _, column := range table.Columns {
		if column.Name == name {
			column = rewrite(column)
		}

		rewritten = append(rewritten, column)
	}

	table.Columns = rewritten

	return table
}

// tightened returns a column that no longer accepts null.
func tightened(column schema.Column) schema.Column {
	column.Nullable = false

	return column
}

// comparison is one pair of schemas these properties hold over, and what it is about.
type comparison struct {
	// name says what differs, which is what a failure names.
	name string
	// from is the schema being changed.
	from schema.Snapshot
	// to is the schema being changed into.
	to schema.Snapshot
}

// Together these emit every operation this differ emits at all, which is what coverage is derived
// from -- so a twenty-first operation fails that test until it is either emitted here or named in
// neverEmitted with the reason.

// comparisons are every pair the properties in this package are asserted over.
func comparisons(t *testing.T) []comparison {
	t.Helper()

	return []comparison{
		{"two tables arrive", empty(), both(t)},
		{"two tables go", both(t), empty()},
		{"a column arrives and another goes",
			users(t),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return withColumn(withoutColumn(table, "note"),
					schema.Column{Name: "phone", Type: exact(t, "string(32)"), Nullable: true})
			}),
		},
		{"a column is tightened",
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return rewriting(table, "note", func(column schema.Column) schema.Column {
					column.Nullable = true

					return column
				})
			}),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return rewriting(table, "note", tightened)
			}),
		},
		{"a column is retyped",
			users(t),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return rewriting(table, "email", func(column schema.Column) schema.Column {
					column.Type = exact(t, "text")

					return column
				})
			}),
		},
		{"the key moves",
			users(t),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				table.PrimaryKey = &schema.PrimaryKey{Columns: []string{"id", "email"}}

				return table
			}),
		},
		{"a view arrives", users(t), withView(t, "SELECT id FROM users")},
		{"a view goes", withView(t, "SELECT id FROM users"), users(t)},
		{"a view's body moves",
			withView(t, "SELECT id FROM users"),
			withView(t, "SELECT id, email FROM users"),
		},
	}
}
