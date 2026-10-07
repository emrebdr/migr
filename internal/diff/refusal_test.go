package diff

import (
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// refusalCase is one difference the differ must not propose, and what it must say about it.
type refusalCase struct {
	// name says what the difference is.
	name string
	// from is the schema being changed.
	from schema.Snapshot
	// to is the schema being changed into.
	to schema.Snapshot
	// kinds are the operations it must refuse, and the only ones it may refuse.
	kinds []ir.Kind
	// says is what the reasons together must mention, so a refusal is one an operator can act on.
	says string
}

func TestItRefusesWhatTheOperationsCannotExpress(t *testing.T) {
	for _, one := range slices.Concat(markerCases(t), vocabularyCases(t), dependentCases(t)) {
		t.Run(one.name, func(t *testing.T) {
			ops, refused := Diff(one.from, one.to)

			checkRefused(t, refused, one)
			checkNotEmitted(t, ops, refused)
		})
	}
}

// markerCases are the elements a driver marked as beyond the operation set, one per element.
func markerCases(t *testing.T) []refusalCase {
	t.Helper()

	detail := "something nothing here can express"

	return []refusalCase{
		{"a marked table arrives", empty(), marking(t, func(table *schema.Table) {
			table.Beyond = marked(detail)
		}), []ir.Kind{ir.KindCreateTable}, "nothing else on that table is proposed either"},

		{"a marked table goes", marking(t, func(table *schema.Table) {
			table.Beyond = marked(detail)
		}), empty(), []ir.Kind{ir.KindDropTable}, detail},

		{"a marked column arrives", users(t), changed(users(t), "users",
			func(table schema.Table) schema.Table {
				return withColumn(table, schema.Column{Name: "doubled",
					Type: exact(t, "int64"), Nullable: true, Beyond: marked(detail)})
			}), []ir.Kind{ir.KindAddColumn}, detail},

		{"a marked column goes", changed(users(t), "users",
			func(table schema.Table) schema.Table {
				return withColumn(table, schema.Column{Name: "doubled",
					Type: exact(t, "int64"), Nullable: true, Beyond: marked(detail)})
			}), users(t), []ir.Kind{ir.KindDropColumn}, detail},

		{"a marked column changes", changed(users(t), "users",
			func(table schema.Table) schema.Table {
				return rewriting(table, "note", func(column schema.Column) schema.Column {
					column.Beyond, column.Nullable = marked(detail), true

					return column
				})
			}), changed(users(t), "users", func(table schema.Table) schema.Table {
			return rewriting(table, "note", func(column schema.Column) schema.Column {
				column.Beyond = marked(detail)

				return tightened(column)
			})
		}), []ir.Kind{ir.KindAlterColumn}, detail},

		{"a marker appears on a column", users(t), changed(users(t), "users",
			func(table schema.Table) schema.Table {
				return rewriting(table, "note", func(column schema.Column) schema.Column {
					column.Beyond = marked(detail)

					return column
				})
			}), []ir.Kind{ir.KindAlterColumn}, detail},

		{"a marked index arrives", both(t), changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.Indexes = []schema.Index{{Name: "orders_partial_idx",
					Columns: []string{"total"}, Beyond: marked(detail)}}

				return table
			}), []ir.Kind{ir.KindAddIndex}, detail},

		{"a marked unique goes", changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.Uniques[0].Beyond = marked(detail)

				return table
			}), both(t), []ir.Kind{ir.KindDropUnique}, detail},

		{"a marked check arrives", both(t), changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.Checks[0].Beyond = marked(detail)

				return table
			}), []ir.Kind{ir.KindAddCheck}, detail},

		{"a marked foreign key goes", changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.ForeignKeys[0].Beyond = marked(detail)

				return table
			}), both(t), []ir.Kind{ir.KindDropForeignKey}, detail},

		{"a marked primary key moves", changed(users(t), "users",
			func(table schema.Table) schema.Table {
				table.PrimaryKey.Beyond = marked(detail)

				return table
			}), changed(users(t), "users", func(table schema.Table) schema.Table {
			table.PrimaryKey = &schema.PrimaryKey{Columns: []string{"id", "email"}}

			return table
		}), []ir.Kind{ir.KindDropPrimaryKey, ir.KindAddPrimaryKey}, detail},

		{"a marked index goes", changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.Indexes[0].Beyond = marked(detail)

				return table
			}), withoutIndexes(both(t)), []ir.Kind{ir.KindDropIndex}, detail},

		{"a marked unique arrives", withoutUniques(both(t)), changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.Uniques[0].Beyond = marked(detail)

				return table
			}), []ir.Kind{ir.KindAddUnique}, detail},

		{"a marked check goes", changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.Checks[0].Beyond = marked(detail)

				return table
			}), withoutChecks(both(t)), []ir.Kind{ir.KindDropCheck}, detail},

		{"a marked foreign key arrives", withoutKeys(both(t)), changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.ForeignKeys[0].Beyond = marked(detail)

				return table
			}), []ir.Kind{ir.KindAddForeignKey}, detail},

		{"a marked view's body moves", markedView(t, detail),
			markedViewBody(t, detail, "SELECT id, email FROM users"),
			[]ir.Kind{ir.KindCreateView}, detail},

		{"a view's body moves onto something nothing can express",
			withView(t, "SELECT id FROM users"),
			markedViewBody(t, detail, "SELECT id, email FROM users"),
			[]ir.Kind{ir.KindCreateView}, detail},

		{"a marked view arrives", users(t), markedView(t, detail), []ir.Kind{ir.KindCreateView},
			detail},

		{"a marked view goes", markedView(t, detail), users(t), []ir.Kind{ir.KindDropView}, detail},
	}
}

// vocabularyCases are the differences the IR has no field for at all, marker or no marker.
func vocabularyCases(t *testing.T) []refusalCase {
	t.Helper()

	return []refusalCase{
		{"a table with no columns arrives", empty(),
			schema.Snapshot{Tables: []schema.Table{{Name: "bare"}}},
			[]ir.Kind{ir.KindCreateTable}, "at least one"},

		{"a column no logical type names arrives", users(t), changed(users(t), "users",
			func(table schema.Table) schema.Table {
				return withColumn(table, schema.Column{Name: "search",
					Type: opaque(t, "tsvector"), Nullable: true})
			}), []ir.Kind{ir.KindAddColumn}, "names no logical type at all"},

		{"a column defaulting to engine SQL arrives", users(t), changed(users(t), "users",
			func(table schema.Table) schema.Table {
				return withColumn(table, schema.Column{Name: "serial",
					Type: exact(t, "int64"), Default: &schema.ColumnDefault{
						Expression: "nextval('x')",
					}})
			}), []ir.Kind{ir.KindAddColumn}, "never engine SQL"},

		{"a retype one side only guesses at", users(t), changed(users(t), "users",
			func(table schema.Table) schema.Table {
				return rewriting(table, "id", func(column schema.Column) schema.Column {
					column.Type = ambiguous(t, "int32", "integer")

					return column
				})
			}), []ir.Kind{ir.KindAlterColumn}, "a rewrite proposed from a guess"},

		{"a retype where one side names no logical type", users(t), changed(users(t), "users",
			func(table schema.Table) schema.Table {
				return rewriting(table, "id", func(column schema.Column) schema.Column {
					column.Type = opaque(t, "tsvector")

					return column
				})
			}), []ir.Kind{ir.KindAlterColumn}, "names no logical type at all"},

		{"a default moving to engine SQL", users(t), changed(users(t), "users",
			func(table schema.Table) schema.Table {
				return rewriting(table, "note", func(column schema.Column) schema.Column {
					column.Default = &schema.ColumnDefault{Expression: "nextval('x')"}

					return column
				})
			}), []ir.Kind{ir.KindAlterColumn}, "engine SQL"},

		{"an index enforcing uniqueness arrives", both(t), changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.Indexes = []schema.Index{{Name: "orders_total_idx",
					Columns: []string{"total"}, Unique: true}}

				return table
			}), []ir.Kind{ir.KindAddIndex}, "a unique constraint here"},

		{"a check the engine names nothing arrives", both(t), changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.Checks = append(table.Checks, schema.Check{Expression: "code <> ''"})

				return table
			}), []ir.Kind{ir.KindAddCheck}, "no name"},

		{"a foreign key the engine names nothing arrives", withoutKeys(both(t)),
			changed(both(t), "orders", func(table schema.Table) schema.Table {
				table.ForeignKeys[0].Name = ""

				return table
			}), []ir.Kind{ir.KindAddForeignKey}, "no name"},

		{"a foreign key the engine names nothing goes", changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.ForeignKeys[0].Name = ""

				return table
			}), withoutKeys(both(t)), []ir.Kind{ir.KindDropForeignKey}, "no name"},

		{"a key the target states over a nullable column", users(t), changed(users(t), "users",
			func(table schema.Table) schema.Table {
				table.PrimaryKey = &schema.PrimaryKey{Columns: []string{"note"}}

				return table
			}), []ir.Kind{ir.KindDropPrimaryKey, ir.KindAddPrimaryKey}, "accepts null"},

		{"a key whose own half cannot be dropped", changed(users(t), "users",
			func(table schema.Table) schema.Table {
				table.PrimaryKey.Beyond = marked("a key nothing here can express")

				return table
			}), changed(users(t), "users", func(table schema.Table) schema.Table {
			table.PrimaryKey = &schema.PrimaryKey{Columns: []string{"id", "email"}}

			return table
		}), []ir.Kind{ir.KindDropPrimaryKey, ir.KindAddPrimaryKey}, "held back"},
	}
}

// dependentCases are the operations a refusal elsewhere must take with it.
func dependentCases(t *testing.T) []refusalCase {
	t.Helper()

	detail := "something nothing here can express"

	return []refusalCase{
		{"a table whose column cannot be carried, with an index over it", empty(),
			schema.Snapshot{Tables: []schema.Table{{
				Name: "events",
				Columns: []schema.Column{
					{Name: "id", Type: exact(t, "int64")},
					{Name: "search", Type: opaque(t, "tsvector"), Nullable: true},
				},
				Indexes: []schema.Index{{Name: "events_search_idx", Columns: []string{"search"}}},
			}}}, []ir.Kind{ir.KindCreateTable}, "nothing else on that table is proposed either"},

		{"a column that cannot be added, with an index over it", users(t),
			changed(users(t), "users", func(table schema.Table) schema.Table {
				table.Indexes = []schema.Index{{Name: "users_doubled_idx",
					Columns: []string{"doubled"}}}

				return withColumn(table, schema.Column{Name: "doubled",
					Type: exact(t, "int64"), Nullable: true, Beyond: marked(detail)})
			}), []ir.Kind{ir.KindAddColumn, ir.KindAddIndex}, "this diff is not adding"},

		{"a table whose index cannot be dropped goes", changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				table.Indexes[0].Beyond = marked(detail)

				return table
			}), users(t), []ir.Kind{ir.KindDropTable}, "has to be dropped by name first"},

		{"a table with a column nothing can carry goes", changed(both(t), "orders",
			func(table schema.Table) schema.Table {
				return rewriting(table, "total", func(column schema.Column) schema.Column {
					column.Type = opaque(t, "tsvector")

					return column
				})
			}), users(t), []ir.Kind{ir.KindDropTable}, "names no logical type at all"},

		{"a foreign key over a column that cannot be added",
			schema.Snapshot{Tables: []schema.Table{
				{Name: "archive", Columns: []schema.Column{{Name: "id", Type: exact(t, "int64")}}},
				{Name: "events", Columns: []schema.Column{{Name: "id", Type: exact(t, "int64")}}},
			}},
			schema.Snapshot{Tables: []schema.Table{
				{Name: "archive", Columns: []schema.Column{{Name: "id", Type: exact(t, "int64")}}},
				{
					Name: "events",
					Columns: []schema.Column{
						{Name: "id", Type: exact(t, "int64")},
						{Name: "tag", Type: exact(t, "int64"), Nullable: true,
							Beyond: marked(detail)},
					},
					ForeignKeys: []schema.ForeignKey{{
						Name: "events_archive_fk", Columns: []string{"tag"},
						ReferencesTable: "archive", ReferencesColumns: []string{"id"},
					}},
				},
			}}, []ir.Kind{ir.KindAddColumn, ir.KindAddForeignKey},
			`it covers column "events.tag", which this diff is not adding`},

		{"a foreign key to a table that cannot be created", empty(),
			schema.Snapshot{Tables: []schema.Table{
				{
					Name:    "archive",
					Columns: []schema.Column{{Name: "id", Type: exact(t, "int64")}},
					Beyond:  marked(detail),
				},
				{
					Name: "events",
					Columns: []schema.Column{
						{Name: "id", Type: exact(t, "int64")},
						{Name: "archive_id", Type: exact(t, "int64"), Nullable: true},
					},
					ForeignKeys: []schema.ForeignKey{{
						Name: "events_archive_fk", Columns: []string{"archive_id"},
						ReferencesTable: "archive", ReferencesColumns: []string{"id"},
					}},
				},
			}}, []ir.Kind{ir.KindCreateTable, ir.KindAddForeignKey},
			"which this diff is not creating"},
	}
}

// marking returns the one-table schema with that table rewritten, for a case about the table itself.
func marking(t *testing.T, rewrite func(*schema.Table)) schema.Snapshot {
	t.Helper()

	snapshot := users(t)
	rewrite(&snapshot.Tables[0])

	return snapshot
}

// markedView returns a schema holding one view a driver marked as beyond the operation set.
func markedView(t *testing.T, detail string) schema.Snapshot {
	t.Helper()

	snapshot := withView(t, "SELECT id FROM users")
	snapshot.Views[0].Beyond = marked(detail)

	return snapshot
}

// withoutKeys returns a schema holding no foreign key, which is what an added one is diffed from.
func withoutKeys(snapshot schema.Snapshot) schema.Snapshot {
	stripped := snapshot.Canonical()

	for i := range stripped.Tables {
		stripped.Tables[i].ForeignKeys = nil
	}

	return stripped
}

// withoutIndexes returns a schema holding no index, which is what an added one is diffed from.
func withoutIndexes(snapshot schema.Snapshot) schema.Snapshot {
	stripped := snapshot.Canonical()

	for i := range stripped.Tables {
		stripped.Tables[i].Indexes = nil
	}

	return stripped
}

// withoutUniques returns a schema holding no unique constraint.
func withoutUniques(snapshot schema.Snapshot) schema.Snapshot {
	stripped := snapshot.Canonical()

	for i := range stripped.Tables {
		stripped.Tables[i].Uniques = nil
	}

	return stripped
}

// withoutChecks returns a schema holding no check constraint.
func withoutChecks(snapshot schema.Snapshot) schema.Snapshot {
	stripped := snapshot.Canonical()

	for i := range stripped.Tables {
		stripped.Tables[i].Checks = nil
	}

	return stripped
}

// markedViewBody returns a marked view whose body is the one given, for a case about a replacement.
func markedViewBody(t *testing.T, detail, body string) schema.Snapshot {
	t.Helper()

	snapshot := withView(t, body)
	snapshot.Views[0].Beyond = marked(detail)

	return snapshot
}

// checkNotEmitted reports an operation that was emitted for the very thing it was refused for.
func checkNotEmitted(t *testing.T, ops ir.Operations, refused []Refusal) {
	t.Helper()

	for _, op := range ops {
		for _, refusal := range refused {
			if op.Kind() == refusal.Kind && engine.Subject(op) == refusal.Subject {
				t.Errorf("it refused %s and emitted one as well", refusal.Describe())
			}
		}
	}
}

// checkRefused reports every way one case's refusals are not the ones it asked for.
func checkRefused(t *testing.T, refused []Refusal, one refusalCase) {
	t.Helper()

	if len(refused) == 0 {
		t.Fatalf("nothing was refused, and %v should have been", one.kinds)
	}

	for _, refusal := range refused {
		if !slices.Contains(one.kinds, refusal.Kind) {
			t.Errorf("it refused %s as well: %s", refusal.Kind, refusal.Describe())
		}

		if strings.TrimSpace(refusal.Subject) == "" {
			t.Errorf("the refusal %q names nothing it is about", refusal.Reason)
		}
	}

	for _, kind := range one.kinds {
		if !slices.ContainsFunc(refused, func(r Refusal) bool { return r.Kind == kind }) {
			t.Errorf("it did not refuse %s", kind)
		}
	}

	if !strings.Contains(describeAll(refused), one.says) {
		t.Errorf("the refusals %q do not say %q", describeAll(refused), one.says)
	}
}

func TestARefusalReadsAsALine(t *testing.T) {
	refusal := Refusal{Kind: ir.KindAddIndex, Subject: "orders_total_idx", Reason: "it is partial"}

	if line := refusal.Describe(); line != `add_index on "orders_total_idx": it is partial` {
		t.Errorf("Describe() is %q", line)
	}
}
