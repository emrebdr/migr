package fold_test

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/fold"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

func TestADataOperationChangesNoSchema(t *testing.T) {
	for _, kind := range ir.DataKinds() {
		op, _ := representative(t, kind)

		folded, refused := fold.Forward(foldable(t), ir.Operations{op})
		if len(refused) != 0 || !folded.Equal(foldable(t)) {
			t.Errorf("folding a %s changed the schema or refused it: %v", kind, refused)
			t.Log("A data operation writes rows; a projection that moved under one would have")
			t.Log("every later migration judged against a schema nothing created.")
		}
	}
}

func TestADataOperationNamingWhatIsNotThereIsRefused(t *testing.T) {
	cases := []struct {
		name string
		op   ir.Operation
		want string
	}{
		{"a copy from a table nothing created", ir.DataCopy{From: "ghosts", To: "things",
			Columns: []ir.CopyColumn{{From: "id", To: "id"}}}, "ghosts"},
		{"a copy into a column nothing added", ir.DataCopy{From: "things", To: "loose_things",
			Columns: []ir.CopyColumn{{From: "id", To: "absent"}}}, "loose_things.absent"},
		{"a backfill of a column nothing added", ir.DataBackfill{Table: "things", Column: "absent",
			Type: logical(t, "text"), Value: ir.Literal("x")}, "things.absent"},
		{"a transform reading a column nothing added", ir.DataTransform{Table: "things",
			Column: "loose", Type: logical(t, "string(16)"), From: "absent",
			Function: ir.TransformCopy}, "things.absent"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, refused := fold.Forward(foldable(t), ir.Operations{tc.op})
			if len(refused) != 1 || !strings.Contains(refused[0].Describe(), tc.want) {
				t.Fatalf("folding refused %v, want one refusal naming %q", refused, tc.want)
			}
		})
	}
}

func TestADataWriteMustStateTheTypeItsColumnHolds(t *testing.T) {
	_, refused := fold.Forward(foldable(t), ir.Operations{ir.DataBackfill{Table: "things",
		Column: "loose", Type: logical(t, "int32"), Value: ir.Literal("7")}})

	if len(refused) != 1 || !strings.Contains(refused[0].Reason, "holds it as string(16)") {
		t.Errorf("a backfill stating the wrong type folded with %v", refused)
		t.Log("The type is what the value is rendered in, so one that contradicts the column")
		t.Log("renders a value the column was never declared to hold.")
	}

	ambiguous, err := schema.AmbiguousType(logical(t, "int64"), "integer")
	if err != nil {
		t.Fatalf("AmbiguousType: %v", err)
	}

	guessed := foldable(t)
	guessed.Tables[0].Columns[2].Type = ambiguous

	if _, refused := fold.Forward(guessed, ir.Operations{ir.DataBackfill{Table: "things",
		Column: "loose", Type: logical(t, "int32"), Value: ir.Literal("7")}}); len(refused) != 0 {
		t.Errorf("a type the catalog could only guess at was held against the stated one: %v",
			refused)
		t.Log("A guess never equals a fact, and it never contradicts one either.")
	}
}
