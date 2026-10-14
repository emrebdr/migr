package plan

import (
	"errors"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// fill is the data operation the chunking tests plan, backfilling one column.
func fill(t *testing.T) ir.DataBackfill {
	t.Helper()

	text, err := schema.Parse("text")
	if err != nil {
		t.Fatalf("parsing a type: %v", err)
	}

	return ir.DataBackfill{Table: "users", Column: "plan", Type: text, Value: ir.Literal("free")}
}

// chunking compiles both kinds to one statement each, through a planner that runs chunks of ten.
func chunking() Planner {
	return Planner{Compiler: byKind(map[ir.Kind]string{
		ir.KindDataBackfill: "u",
		ir.KindAddColumn:    "a",
	}), ChunkRows: 10}
}

func TestADataOnlyMigrationIsMarkedToRunInChunksAndAnyOtherIsNot(t *testing.T) {
	column := ir.AddColumn{Table: "users", Column: ir.Column{Name: "plan", Type: fill(t).Type,
		Nullable: true}}

	cases := []struct {
		name  string
		ops   ir.Operations
		rows  int
		marks int32
	}{
		{"data alone", ir.Operations{fill(t), fill(t)}, 10, 2},
		{"data beside a schema change", ir.Operations{column, fill(t)}, 10, 0},
		{"chunking switched off", ir.Operations{fill(t)}, 0, 0},
	}

	for _, c := range cases {
		planner := chunking()
		planner.ChunkRows = c.rows

		compiled, err := planner.Compile(added(t), ir.Up, c.ops)
		if err != nil {
			t.Fatalf("%s: Compile: %v", c.name, err)
		}

		if got := compiled.Chunked(); got != c.marks {
			t.Errorf("%s: %d operations are marked to run in chunks, want %d", c.name, got, c.marks)
			t.Log("Each chunk commits on its own, so a migration that also changed the schema could")
			t.Log("no longer be the one transaction it runs in -- splitting one is M13-T4's.")
		}

		if compiled.Len() != int32(len(c.ops)) {
			t.Errorf("%s: the plan compiled %d statements, want one per operation still", c.name,
				compiled.Len())
		}
	}
}

func TestAPlannerWhoseDriverCannotRenderAChunkSaysSo(t *testing.T) {
	_, err := chunking().CompileChunk(Step{Op: fill(t)}, []string{"id"})
	if !errors.Is(err, ErrNoChunkCompiler) {
		t.Errorf("rendering a chunk through a driver with no ChunkCompiler reported %v", err)
	}
}
