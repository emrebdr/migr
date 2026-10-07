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
		ops, _ := Diff(one.from, one.to)

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
			ops, _ := Diff(one.from, one.to)

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

func TestTheOrderIsItsOwnMirror(t *testing.T) {
	for _, one := range comparisons(t) {
		t.Run(one.name, func(t *testing.T) {
			ops, _ := Diff(one.from, one.to)

			reversed, err := ir.ReverseAll(ops)
			if err != nil {
				t.Fatalf("nothing could undo what this diff proposes: %v", err)
			}

			highest := -1

			for i, op := range reversed {
				place := slices.Index(order, op.Kind())

				if place < highest {
					t.Errorf("the inverse is out of order at operation %d (%s): %v",
						i+1, op.Kind(), kindsOf(reversed))
					t.Log("The pairs sit at opposite ends of the order on purpose, so reversing a")
					t.Log("list reverses the dependencies with it -- which is what makes a derived")
					t.Log("down of a generated migration runnable rather than merely well formed.")
				}

				highest = max(highest, place)
			}
		})
	}
}

func TestEveryOperationSitsOppositeItsOwnInverse(t *testing.T) {
	for i, kind := range order {
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

		mirror := len(order) - 1 - i

		if place := slices.Index(order, inverse.Kind()); place != mirror {
			t.Errorf("%s is at %d and its inverse %s is at %d, where the mirror of %d is %d",
				kind, i, inverse.Kind(), place, i, mirror)
			t.Log("The order has to be exactly palindromic for a reversed list to still be in it,")
			t.Log("which is what a derived down of a generated migration depends on.")
		}
	}

	if len(order)%2 == 0 {
		t.Error("an even order has no middle, and alter_column inverts to itself")
	}
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
