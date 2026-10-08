package fold_test

import (
	"testing"

	"github.com/emrebdr/migr/internal/diff"
	"github.com/emrebdr/migr/internal/schema"
)

// comparison is one pair of schemas the round trip holds over, and what it is about.
type comparison struct {
	// name says what differs, which is what a failure names.
	name string
	// from is the schema being changed.
	from schema.Snapshot
	// to is the schema being changed into.
	to schema.Snapshot
	// confirmed is what an operator confirmed, which is empty for every case that is not a rename.
	confirmed diff.Renames
}

// exact returns a column type a catalog answered precisely, which every column here carries.
func exact(t *testing.T, spelling string) schema.ColumnType {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	built, err := schema.ExactType(parsed, spelling)
	if err != nil {
		t.Fatalf("ExactType(%q): %v", spelling, err)
	}

	return built
}

// literal returns a default holding one value, which is what distinguishes empty from absent.
func literal(v string) *schema.ColumnDefault {
	return &schema.ColumnDefault{Literal: &v}
}

// empty is the schema of a namespace holding nothing, which an unmigrated database is.
func empty() schema.Snapshot {
	return schema.Snapshot{}
}

// users is the one-table schema most of these cases change exactly one thing about.
func users(t *testing.T) schema.Snapshot {
	t.Helper()

	return schema.Snapshot{Tables: []schema.Table{usersTable(t)}}
}

// usersTable is the referenced table, keyed on the column every other fixture points at.
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

// ordersTable carries one of every constraint the operations can express.
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

// both is the two-table schema, which is where every constraint operation comes from.
func both(t *testing.T) schema.Snapshot {
	t.Helper()

	return schema.Snapshot{Tables: []schema.Table{usersTable(t), ordersTable(t)}}
}

// withView returns a schema holding one view as well, whose body is engine text.
func withView(t *testing.T, body string) schema.Snapshot {
	t.Helper()

	snapshot := users(t)
	snapshot.Views = []schema.View{{Name: "active_users", Query: body}}

	return snapshot
}

// changed returns one schema with one table rewritten, which is how these cases state a difference.
func changed(snapshot schema.Snapshot, name string,
	rewrite func(schema.Table) schema.Table,
) schema.Snapshot {
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
func rewriting(table schema.Table, name string,
	rewrite func(schema.Column) schema.Column,
) schema.Table {
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

// tableRenamedTo is the two-table schema with the referenced table under another name.
func tableRenamedTo(t *testing.T, name string) schema.Snapshot {
	t.Helper()

	renamed := both(t)

	for i := range renamed.Tables {
		if renamed.Tables[i].Name == "users" {
			renamed.Tables[i].Name = name
			renamed.Tables[i].PrimaryKey = &schema.PrimaryKey{Name: name + "_pkey",
				Columns: []string{"id"}}
		}

		for j := range renamed.Tables[i].ForeignKeys {
			if renamed.Tables[i].ForeignKeys[j].ReferencesTable == "users" {
				renamed.Tables[i].ForeignKeys[j].ReferencesTable = name
			}
		}
	}

	return renamed.Canonical()
}

// The one case that reaches the projection's repointing of another table's foreign key, which every
// engine does by itself (M11-T4) and which a rename of an unreferenced column cannot show.

// referencedRenamed is the two-table schema with the referenced key column under another name.
func referencedRenamed(t *testing.T) schema.Snapshot {
	t.Helper()

	renamed := both(t).Canonical()

	for i := range renamed.Tables {
		switch renamed.Tables[i].Name {
		case "users":
			renamed.Tables[i] = rewriting(renamed.Tables[i], "id",
				func(column schema.Column) schema.Column {
					column.Name = "user_id"

					return column
				})
			renamed.Tables[i].PrimaryKey = &schema.PrimaryKey{Name: "users_pkey",
				Columns: []string{"user_id"}}
		case "orders":
			renamed.Tables[i].ForeignKeys = []schema.ForeignKey{{
				Name: "orders_user_fk", Columns: []string{"user_id"},
				ReferencesTable: "users", ReferencesColumns: []string{"user_id"},
				OnDelete: schema.ActionCascade, OnUpdate: schema.ActionNoAction,
			}}
		}
	}

	return renamed.Canonical()
}

// comparisons are every pair the round trip in this package is asserted over.
func comparisons(t *testing.T) []comparison {
	t.Helper()

	return []comparison{
		{"two tables arrive", empty(), both(t), diff.Renames{}},
		{"two tables go", both(t), empty(), diff.Renames{}},
		{"a column arrives and another goes",
			users(t),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return withColumn(withoutColumn(table, "note"),
					schema.Column{Name: "phone", Type: exact(t, "string(32)"), Nullable: true})
			}), diff.Renames{}},
		{"a column is tightened",
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return rewriting(table, "note", func(column schema.Column) schema.Column {
					column.Nullable = true

					return column
				})
			}),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return rewriting(table, "note", func(column schema.Column) schema.Column {
					column.Nullable = false

					return column
				})
			}), diff.Renames{}},
		{"a column is retyped",
			users(t),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return rewriting(table, "email", func(column schema.Column) schema.Column {
					column.Type = exact(t, "text")

					return column
				})
			}), diff.Renames{}},
		{"a default arrives and another goes",
			users(t),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				table = rewriting(table, "note", func(column schema.Column) schema.Column {
					column.Default = nil

					return column
				})

				return rewriting(table, "email", func(column schema.Column) schema.Column {
					column.Default = literal("nobody@example.com")

					return column
				})
			}), diff.Renames{}},
		{"the key moves",
			users(t),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				table.PrimaryKey = &schema.PrimaryKey{Columns: []string{"id", "email"}}

				return table
			}), diff.Renames{}},
		{"every constraint on one table goes", both(t),
			changed(both(t), "orders", func(table schema.Table) schema.Table {
				table.Indexes, table.Uniques, table.Checks, table.ForeignKeys = nil, nil, nil, nil

				return table
			}), diff.Renames{}},
		{"a view arrives", users(t), withView(t, "SELECT id FROM users"), diff.Renames{}},
		{"a view goes", withView(t, "SELECT id FROM users"), users(t), diff.Renames{}},
		{"a confirmed table rename", both(t), tableRenamedTo(t, "clients"),
			diff.Renames{Tables: []diff.TableRename{{From: "users", To: "clients"}}}},
		{"a confirmed rename of a column another table references", both(t), referencedRenamed(t),
			diff.Renames{Columns: []diff.ColumnRename{{Table: "users", From: "id", To: "user_id"}}}},
		{"a confirmed column rename", both(t),
			changed(both(t), "users", func(table schema.Table) schema.Table {
				return rewriting(table, "note", func(column schema.Column) schema.Column {
					column.Name = "memo"

					return column
				})
			}),
			diff.Renames{Columns: []diff.ColumnRename{{Table: "users", From: "note", To: "memo"}}}},
	}
}
