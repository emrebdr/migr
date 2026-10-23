package diff

import (
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

func TestItProposesNothingForTwoSchemasThatAgree(t *testing.T) {
	for _, one := range []struct {
		name     string
		snapshot schema.Snapshot
	}{
		{"nothing at all", empty()},
		{"two tables and every constraint", both(t)},
		{"a view, whose body is engine text", withView(t, "SELECT id FROM users")},
		{"a column the catalog only guesses at",
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return rewriting(table, "id", func(column schema.Column) schema.Column {
					column.Type = ambiguous(t, "int64", "integer")

					return column
				})
			}),
		},
		{"a column no logical type names",
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return withColumn(table,
					schema.Column{Name: "search", Type: opaque(t, "tsvector"), Nullable: true})
			}),
		},
		{"a column defaulting to engine SQL",
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return rewriting(table, "id", func(column schema.Column) schema.Column {
					column.Default = &schema.ColumnDefault{Expression: "nextval('users_id_seq')"}

					return column
				})
			}),
		},
		{"a partial index nothing here can express",
			changed(both(t), "orders", func(table schema.Table) schema.Table {
				table.Indexes[0].Beyond = marked("a partial index, WHERE total > 0")

				return table
			}),
		},
		{"a generated column nothing here can express",
			changed(users(t), "users", func(table schema.Table) schema.Table {
				return withColumn(table, schema.Column{Name: "doubled",
					Type: exact(t, "int64"), Nullable: true,
					Beyond: marked("a generated column, (id * 2) STORED")})
			}),
		},
	} {
		t.Run(one.name, func(t *testing.T) {
			ops, refused := Diff(one.snapshot, one.snapshot, Renames{})

			for _, op := range ops {
				t.Errorf("a schema diffed against itself proposed %s", op.Kind())
			}

			for _, refusal := range refused {
				t.Errorf("a schema diffed against itself refused %s", refusal.Describe())
			}
		})
	}

	t.Log("The marker is read as a lookup rather than as a report, which is what makes this hold:")
	t.Log("an element nothing can express produces no operation, so it produces no refusal either.")
	t.Log("M11-T6's empty diff is a statement about the schema, not about the markers in it.")
}

func TestItCreatesATableBeforeTheKeyThatReferencesIt(t *testing.T) {
	ops, refused := Diff(empty(), both(t), Renames{})

	if len(refused) != 0 {
		t.Fatalf("creating two expressible tables refused %v", describeAll(refused))
	}

	created := indexOfKind(t, ops, ir.KindCreateTable)
	keyed := indexOfKind(t, ops, ir.KindAddForeignKey)

	if created > keyed {
		t.Errorf("create_table is at %d and add_foreign_key at %d, so the key is proposed first",
			created, keyed)
	}

	for _, kind := range []ir.Kind{ir.KindAddIndex, ir.KindAddUnique, ir.KindAddCheck} {
		if indexOfKind(t, ops, kind) < created {
			t.Errorf("%s is proposed before the table it is on", kind)
		}
	}

	t.Log("This is M11-T3's own done-when, and the conformance suite makes it a measurement: a")
	t.Log("wrong order there fails at the server rather than at this assertion.")
}

func TestItDropsEverythingOnATableBeforeTheTableItself(t *testing.T) {
	ops, refused := Diff(both(t), empty(), Renames{})

	if len(refused) != 0 {
		t.Fatalf("dropping two expressible tables refused %v", describeAll(refused))
	}

	dropped := indexOfKind(t, ops, ir.KindDropTable)

	for _, kind := range []ir.Kind{
		ir.KindDropForeignKey, ir.KindDropCheck, ir.KindDropIndex, ir.KindDropUnique,
	} {
		if indexOfKind(t, ops, kind) > dropped {
			t.Errorf("%s is proposed after the table it is on is dropped", kind)
		}
	}

	t.Log("Every engine takes them with the table, so these drops are not for the server: a")
	t.Log("recorded table definition holds columns and a primary key and nothing else, so a bare")
	t.Log("drop_table is a migration whose inverse rebuilds a different table.")
}

func TestItDropsAnIndexBeforeTheColumnItCovers(t *testing.T) {
	ops, refused := Diff(both(t), changed(both(t), "orders", func(table schema.Table) schema.Table {
		table.Indexes = nil

		return withoutColumn(table, "total")
	}), Renames{})

	if len(refused) != 0 {
		t.Fatalf("dropping an indexed column refused %v", describeAll(refused))
	}

	if indexOfKind(t, ops, ir.KindDropIndex) > indexOfKind(t, ops, ir.KindDropColumn) {
		t.Error("the column is proposed before the index over it")
	}

	t.Log("SQLite refuses DROP COLUMN for an indexed column outright, so this order is what makes")
	t.Log("a generated migration runnable there rather than merely tidy.")
}

func TestItEmitsEveryDropCarryingWhatItReplaced(t *testing.T) {
	for _, one := range comparisons(t) {
		t.Run(one.name, func(t *testing.T) {
			ops, _ := Diff(one.from, one.to, one.confirmed)

			for i, op := range ops {
				if recorded, carries := recordsPriorState(op); carries && !recorded {
					t.Errorf("operation %d (%s) records nothing of what it replaced", i+1, op.Kind())
				}
			}
		})
	}

	t.Log("A differ is the one producer that knows both states exactly, so it fills every from")
	t.Log("payload the IR defines -- which is what makes a migration migr diff writes reversible")
	t.Log("by derivation, and why a drop it cannot record is refused rather than emitted bare.")
}

func TestItMatchesConstraintsAsASet(t *testing.T) {
	renamedKey := changed(both(t), "orders", func(table schema.Table) schema.Table {
		table.ForeignKeys[0].Name = "orders_user_id_fkey"

		return table
	})

	if ops, _ := Diff(both(t), renamedKey, Renames{}); len(ops) != 0 {
		t.Errorf("a foreign key whose name moved proposed %d operations, want none", len(ops))
		t.Log("ForeignKey.Equal ignores the name because no two engines agree on one,")
		t.Log("so a diff across engines must not propose rebuilding every key it can see.")
	}

	renamedIndex := changed(both(t), "orders", func(table schema.Table) schema.Table {
		table.Indexes[0].Name = "orders_total_index"

		return table
	})

	ops, _ := Diff(both(t), renamedIndex, Renames{})

	if kinds := kindsOf(ops); !slices.Equal(kinds,
		[]ir.Kind{ir.KindDropIndex, ir.KindAddIndex}) {
		t.Errorf("a renamed index proposed %v, want a drop and an add", kinds)
		t.Log("An index's name is its own, and there is no alter_index, so a changed one is a drop")
		t.Log("and an add -- which is also what the order has to get right.")
	}
}

func TestItReplacesAViewWhoseBodyMoved(t *testing.T) {
	ops, refused := Diff(withView(t, "SELECT id FROM users"),
		withView(t, "SELECT id, email FROM users"), Renames{})

	if len(refused) != 0 {
		t.Fatalf("replacing a view refused %v", describeAll(refused))
	}

	if len(ops) != 1 {
		t.Fatalf("replacing a view proposed %d operations, want one", len(ops))
	}

	replace, ok := ops[0].(ir.CreateView)
	if !ok {
		t.Fatalf("replacing a view proposed %T", ops[0])
	}

	if !replace.OrReplace {
		t.Error("the view is created rather than replaced, which every engine refuses for one that exists")
	}

	if replace.From == nil || replace.From.Query != "SELECT id FROM users" {
		t.Errorf("the replacement records %v of the view it overwrote, so nothing could put it back",
			replace.From)
	}
}

// recordsPriorState reports whether an operation can record what it replaced, and whether it did.
func recordsPriorState(op ir.Operation) (recorded, carries bool) {
	switch o := op.(type) {
	case ir.DropTable:
		return o.From != nil, true
	case ir.DropColumn:
		return o.From != nil, true
	case ir.AlterColumn:
		return !o.From.Empty(), true
	case ir.DropIndex:
		return o.From != nil, true
	case ir.DropUnique:
		return o.From != nil, true
	case ir.DropCheck:
		return o.From != nil, true
	case ir.DropPrimaryKey:
		return o.From != nil, true
	case ir.DropForeignKey:
		return o.From != nil, true
	case ir.DropView:
		return o.From != nil, true
	case ir.CreateView:
		return o.From != nil, o.OrReplace
	default:
		return false, false
	}
}

// indexOfKind returns where one kind sits in a list, failing the test when the list has none.
func indexOfKind(t *testing.T, ops ir.Operations, kind ir.Kind) int {
	t.Helper()

	for i, op := range ops {
		if op.Kind() == kind {
			return i
		}
	}

	t.Fatalf("the proposed operations %v hold no %s", kindsOf(ops), kind)

	return -1
}

// kindsOf lists what a proposed list is made of, which is what an ordering failure reads as.
func kindsOf(ops ir.Operations) []ir.Kind {
	kinds := make([]ir.Kind, 0, len(ops))
	for _, op := range ops {
		kinds = append(kinds, op.Kind())
	}

	return kinds
}

// describeAll renders every refusal as the lines they are, for a failure that did not expect any.
func describeAll(refused []Refusal) string {
	lines := make([]string, 0, len(refused))
	for _, one := range refused {
		lines = append(lines, one.Describe())
	}

	return strings.Join(lines, "; ")
}

func TestANewTableStatesItsKeyInline(t *testing.T) {
	ops, refused := Diff(empty(), users(t), Renames{})

	if len(refused) != 0 {
		t.Fatalf("creating one expressible table refused %v", describeAll(refused))
	}

	if kinds := kindsOf(ops); len(kinds) != 1 || kinds[0] != ir.KindCreateTable {
		t.Fatalf("creating one table proposed %v, want one create_table", kinds)
	}

	created, ok := ops[0].(ir.CreateTable)
	if !ok {
		t.Fatalf("it proposed a %T", ops[0])
	}

	if !slices.Equal(created.PrimaryKey, []string{"id"}) {
		t.Errorf("the table is created with the key %v, and the schema keys it on id",
			created.PrimaryKey)
	}

	t.Log("docs/ir.md §7.2: a new table states its key inline and an existing one states it as an")
	t.Log("operation. Both are valid documents, so nothing rejects the wrong choice -- but two")
	t.Log("producers disagreeing emit different bytes for one schema change.")
}

func TestADroppedTableRecordsItsKeyAsWellAsItsColumns(t *testing.T) {
	ops, _ := Diff(both(t), empty(), Renames{})

	dropped, ok := ops[indexOfKind(t, ops, ir.KindDropTable)].(ir.DropTable)
	if !ok {
		t.Fatalf("the drop is a %T", ops[indexOfKind(t, ops, ir.KindDropTable)])
	}

	if dropped.From == nil {
		t.Fatal("the drop records nothing of the table")
	}

	if !slices.Equal(dropped.From.PrimaryKey, []string{"id"}) {
		t.Errorf("it records the key %v, so recreating the table would leave it unkeyed",
			dropped.From.PrimaryKey)
	}

	if len(dropped.From.Columns) == 0 {
		t.Error("it records no columns, so nothing could recreate the table at all")
	}
}
