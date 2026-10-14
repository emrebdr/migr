package source_test

import (
	"errors"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
	"github.com/emrebdr/migr/internal/source"
)

func TestEveryOperationIsAChainOrSaysNoSourceExpressesIt(t *testing.T) {
	for _, kind := range ir.Kinds() {
		op, held := representative(t, kind)
		if !held {
			t.Errorf("nothing here chains a %s and nothing says why", kind)

			continue
		}

		chains, err := source.Chains(ir.Operations{op})

		if kind == ir.KindRaw || kind.Since() == 2 {
			if !errors.Is(err, source.ErrNotRenderable) {
				t.Errorf("chaining a %s reported %v, want ErrNotRenderable", kind, err)
			}

			continue
		}

		if err != nil {
			t.Errorf("chaining a %s: %v", kind, err)

			continue
		}

		if len(chains) != 1 || chains[0].Root.Method == "" || len(chains[0].Links) == 0 {
			t.Errorf("a %s chained to %+v, which nothing could write", kind, chains)
		}
	}

	t.Log("Coverage is derived from ir.Kinds(), so a twenty-first operation fails this until a")
	t.Log("chain exists for it or representative says why one cannot.")
	t.Log("raw is one: a schema builder has no call for engine SQL, and a generated migration")
	t.Log("that reached for one would abandon both claims it is supposed to keep. The data")
	t.Log("operations are the rest: a chain exists to write what migr diff proposes, and a")
	t.Log("differ compares two schemas, which hold no rows to propose writing.")
}

// sample returns one logical type of each kind, parameterised ones included.
func sample(t *testing.T, kind schema.Kind) schema.Type {
	t.Helper()

	spellings := map[schema.Kind]string{
		schema.KindString:  "string(32)",
		schema.KindDecimal: "decimal(10,2)",
		schema.KindEnum:    "enum(up,down)",
		schema.KindArray:   "array(int64)",
	}

	spelling, parameterised := spellings[kind]
	if !parameterised {
		spelling = string(kind)
	}

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	return parsed
}

// representative returns one operation of each kind for the coverage check.
func representative(t *testing.T, kind ir.Kind) (ir.Operation, bool) {
	t.Helper()

	held := map[ir.Kind]ir.Operation{
		ir.KindCreateTable: ir.CreateTable{Table: "t", Columns: []ir.Column{
			{Name: "id", Type: sample(t, schema.KindInt64)}}, PrimaryKey: []string{"id"}},
		ir.KindDropTable:   ir.DropTable{Table: "t"},
		ir.KindRenameTable: ir.RenameTable{From: "t", To: "u"},
		ir.KindAddColumn: ir.AddColumn{Table: "t",
			Column: ir.Column{Name: "c", Type: sample(t, schema.KindText), Nullable: true}},
		ir.KindDropColumn:   ir.DropColumn{Table: "t", Column: "c"},
		ir.KindRenameColumn: ir.RenameColumn{Table: "t", From: "c", To: "d"},
		ir.KindAlterColumn: ir.AlterColumn{Table: "t", Column: "c",
			Nullable: pointerTo(false)},
		ir.KindAddIndex:       ir.AddIndex{Table: "t", Name: "i", Columns: []string{"c"}},
		ir.KindDropIndex:      ir.DropIndex{Table: "t", Name: "i"},
		ir.KindAddUnique:      ir.AddUnique{Table: "t", Name: "u", Columns: []string{"c"}},
		ir.KindDropUnique:     ir.DropUnique{Table: "t", Name: "u"},
		ir.KindAddCheck:       ir.AddCheck{Table: "t", Name: "k", Expression: "c > 0"},
		ir.KindDropCheck:      ir.DropCheck{Table: "t", Name: "k"},
		ir.KindAddPrimaryKey:  ir.AddPrimaryKey{Table: "t", Name: "p", Columns: []string{"c"}},
		ir.KindDropPrimaryKey: ir.DropPrimaryKey{Table: "t", Name: "p"},
		ir.KindAddForeignKey: ir.AddForeignKey{Table: "t", Name: "f", Columns: []string{"c"},
			ReferencesTable: "u", ReferencesColumns: []string{"id"}},
		ir.KindDropForeignKey: ir.DropForeignKey{Table: "t", Name: "f"},
		ir.KindCreateView:     ir.CreateView{View: "v", Query: "SELECT 1"},
		ir.KindDropView:       ir.DropView{View: "v"},
		ir.KindRaw:            ir.Raw{Driver: "sqlite", SQL: "ANALYZE"},
		ir.KindDataCopy: ir.DataCopy{From: "t", To: "u", Columns: []ir.CopyColumn{
			{From: "id", To: "id"}}},
		ir.KindDataBackfill: ir.DataBackfill{Table: "t", Column: "c",
			Type: sample(t, schema.KindText), Value: ir.Literal("x")},
		ir.KindDataTransform: ir.DataTransform{Table: "t", Column: "c",
			Type: sample(t, schema.KindText), From: "d", Function: ir.TransformTrim},
	}

	op, ok := held[kind]

	return op, ok
}

// pointerTo returns a pointer to a value, which is how an alteration states a changed attribute.
func pointerTo[T any](v T) *T { return &v }
