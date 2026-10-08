package diff

import (
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

func TestEveryOperationIsEmittedOrAccountedFor(t *testing.T) {
	for _, kind := range ir.Kinds() {
		inOrder := slices.Contains(order, kind)
		_, stated := neverEmitted[kind]

		if inOrder && stated {
			t.Errorf("%s is both in the emission order and named as never emitted", kind)
		}

		if !inOrder && !stated {
			t.Errorf("%s is neither in the emission order nor accounted for", kind)
			t.Log("A twenty-first operation fails this until somebody decides which half it is in.")
		}
	}

	for kind, reason := range neverEmitted {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is named as never emitted without saying why", kind)
		}
	}
}

func TestEveryOperationInTheOrderIsOneSomeComparisonEmits(t *testing.T) {
	emitted := make(map[ir.Kind]bool, len(order))

	for _, one := range comparisons(t) {
		ops, _ := Diff(one.from, one.to, one.confirmed)

		for _, op := range ops {
			emitted[op.Kind()] = true
		}
	}

	for _, kind := range order {
		if !emitted[kind] {
			t.Errorf("nothing in this package's fixtures emits %s, so its place in the order is "+
				"held by nothing", kind)
		}
	}

	for kind := range neverEmitted {
		if emitted[kind] {
			t.Errorf("%s is named as never emitted and one of these fixtures emits it", kind)
		}
	}
}

func TestEveryProposedListIsInTheOneOrder(t *testing.T) {
	for _, one := range comparisons(t) {
		t.Run(one.name, func(t *testing.T) {
			ops, _ := Diff(one.from, one.to, one.confirmed)

			highest := -1

			for i, op := range ops {
				place := slices.Index(order, op.Kind())
				if place < 0 {
					t.Fatalf("operation %d is a %s, which the order does not place", i+1, op.Kind())
				}

				if place < highest {
					t.Errorf("operation %d (%s) is out of order: %v",
						i+1, op.Kind(), kindsOf(ops))
				}

				highest = max(highest, place)
			}
		})
	}

	t.Log("Structural rather than asserted: emit files an operation under its own kind, so the")
	t.Log("order is the one literal in order.go and an operation cannot be put in the wrong place.")
	t.Log("What holds that literal is the named cases in diff_test.go and the engines in")
	t.Log("test/conformance/driver.")
}

func TestAReversedListPutsEveryRenameWhereARollbackNeedsIt(t *testing.T) {
	for _, one := range comparisons(t) {
		t.Run(one.name, func(t *testing.T) {
			ops, _ := Diff(one.from, one.to, one.confirmed)

			reversed, err := ir.ReverseAll(ops)
			if err != nil {
				t.Fatalf("nothing could undo what this diff proposes: %v", err)
			}

			checkReversedOrder(t, reversed)
		})
	}

	t.Log("A rename establishes the name every operation after it speaks, so it is first going")
	t.Log("forward and last coming back. Everything else sits opposite its own inverse, so")
	t.Log("reversing a list of those gives a list in the same order.")
}

// checkReversedOrder holds a reversed list to the one order its two halves each have to be in.
func checkReversedOrder(t *testing.T, reversed ir.Operations) {
	t.Helper()

	highest := -1
	renamed := -1

	for i, op := range reversed {
		place := slices.Index(core, op.Kind())
		if place < 0 {
			renamed = i

			continue
		}

		if place < highest {
			t.Errorf("the inverse is out of order at operation %d (%s): %v",
				i+1, op.Kind(), kindsOf(reversed))
		}

		if renamed >= 0 && renamesAfter(reversed[renamed].Kind(), op.Kind()) {
			t.Errorf("operation %d is a %s and operation %d renames before it: %v",
				i+1, op.Kind(), renamed+1, kindsOf(reversed))
		}

		highest = max(highest, place)
	}
}

// renamesAfter reports a reversed list speaking a name after the rename that was to restore it.
func renamesAfter(rename, kind ir.Kind) bool {
	if rename == ir.KindRenameTable {
		return kind != ir.KindCreateView
	}

	return kind != ir.KindAddCheck && kind != ir.KindRenameTable && kind != ir.KindCreateView
}

func TestEveryOperationSitsOppositeItsOwnInverse(t *testing.T) {
	for i, kind := range core {
		op, held := representative(t, kind)
		if !held {
			t.Errorf("there is no representative %s to take the inverse of", kind)

			continue
		}

		inverse, err := op.Reverse()
		if err != nil {
			t.Errorf("%s records enough to be inverted and was not: %v", kind, err)

			continue
		}

		mirror := len(core) - 1 - i

		if place := slices.Index(core, inverse.Kind()); place != mirror {
			t.Errorf("%s is at %d and its inverse %s is at %d, where the mirror of %d is %d",
				kind, i, inverse.Kind(), place, i, mirror)
			t.Log("Everything but a rename has to be exactly palindromic for a reversed list to")
			t.Log("still be in that order, which a derived down of a generated migration depends on.")
		}
	}

	if len(core)%2 == 0 {
		t.Error("an even core has no middle, and alter_column inverts to itself")
	}

	for _, kind := range []ir.Kind{ir.KindRenameTable, ir.KindRenameColumn} {
		if slices.Contains(core, kind) {
			t.Errorf("%s is in the mirrored core and it is self-inverse, which only the middle can "+
				"be -- and alter_column is the middle", kind)
		}
	}

	t.Log("The two renames are the one asymmetry, and the asymmetry is what makes the derived down")
	t.Log("runnable: ir.ReverseAll moves them to the end, which is where a rollback needs them.")
}

func TestARenameComesBeforeEveryOperationThatSpeaksItsNewName(t *testing.T) {
	cases := []struct {
		rename ir.Kind
		after  []ir.Kind
	}{
		{ir.KindRenameTable, []ir.Kind{ir.KindDropView}},
		{ir.KindRenameColumn, []ir.Kind{ir.KindDropView, ir.KindRenameTable, ir.KindDropCheck}},
	}

	for _, one := range cases {
		place := slices.Index(order, one.rename)
		if place < 0 {
			t.Fatalf("%s is not in the order at all", one.rename)
		}

		for i, kind := range order {
			if i >= place || slices.Contains(one.after, kind) {
				continue
			}

			t.Errorf("%s is at %d, before the %s at %d, and it speaks a name that rename "+
				"establishes", kind, i, one.rename, place)
		}
	}

	t.Log("Only two operations may precede a rename, and each records something a rename would")
	t.Log("otherwise have moved out from under it: a dropped view's recorded body names the old")
	t.Log("table, and a dropped check's recorded expression names the old column. MySQL 8.0 also")
	t.Log("refuses to rename a column a check names at all (error 3959, measured).")
}

// representative returns one operation of a kind, carrying enough of a payload to be inverted.
func representative(t *testing.T, kind ir.Kind) (ir.Operation, bool) {
	t.Helper()

	columns := &ir.ColumnsDef{Columns: []string{"id"}}
	column := ir.Column{Name: "id", Type: parse(t, "int64")}

	switch kind {
	case ir.KindCreateTable:
		return ir.CreateTable{Table: "t", Columns: []ir.Column{column}}, true
	case ir.KindDropTable:
		return ir.DropTable{Table: "t", From: &ir.TableDef{Columns: []ir.Column{column}}}, true
	case ir.KindAddColumn:
		return ir.AddColumn{Table: "t", Column: column}, true
	case ir.KindDropColumn:
		return ir.DropColumn{Table: "t", Column: "id", From: &column}, true
	case ir.KindAlterColumn:
		return ir.AlterColumn{Table: "t", Column: "id", Nullable: boolAt(true),
			From: &ir.ColumnState{Nullable: boolAt(false)}}, true
	case ir.KindAddIndex:
		return ir.AddIndex{Table: "t", Name: "i", Columns: []string{"id"}}, true
	case ir.KindDropIndex:
		return ir.DropIndex{Table: "t", Name: "i", From: columns}, true
	case ir.KindAddUnique:
		return ir.AddUnique{Table: "t", Name: "u", Columns: []string{"id"}}, true
	case ir.KindDropUnique:
		return ir.DropUnique{Table: "t", Name: "u", From: columns}, true
	case ir.KindAddCheck:
		return ir.AddCheck{Table: "t", Name: "c", Expression: "id > 0"}, true
	case ir.KindDropCheck:
		return ir.DropCheck{Table: "t", Name: "c", From: &ir.CheckDef{Expression: "id > 0"}}, true
	case ir.KindAddPrimaryKey:
		return ir.AddPrimaryKey{Table: "t", Columns: []string{"id"}}, true
	case ir.KindDropPrimaryKey:
		return ir.DropPrimaryKey{Table: "t", From: columns}, true
	case ir.KindAddForeignKey:
		return ir.AddForeignKey{Table: "t", Name: "f", Columns: []string{"id"},
			ReferencesTable: "o", ReferencesColumns: []string{"id"}}, true
	case ir.KindDropForeignKey:
		return ir.DropForeignKey{Table: "t", Name: "f", From: &ir.ForeignKeyDef{
			Columns: []string{"id"}, ReferencesTable: "o", ReferencesColumns: []string{"id"},
		}}, true
	case ir.KindCreateView:
		return ir.CreateView{View: "v", Query: "SELECT 1"}, true
	case ir.KindDropView:
		return ir.DropView{View: "v", From: &ir.ViewDef{Query: "SELECT 1"}}, true
	default:
		return nil, false
	}
}

// boolAt returns a pointer to a nullability, which is how an alter_column states one.
func boolAt(v bool) *bool { return &v }
