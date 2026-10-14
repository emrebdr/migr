package history

import (
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

func TestACursorSurvivesItsTextWhateverItsKeyHolds(t *testing.T) {
	for _, cursor := range [][]string{nil, {}, {"42"}, {"a, \"b\"", "\\", "ü", ""}} {
		encoded, err := EncodeCursor(cursor)
		if err != nil {
			t.Fatalf("EncodeCursor(%q): %v", cursor, err)
		}

		decoded, err := DecodeCursor(encoded)
		if err != nil {
			t.Fatalf("DecodeCursor(%q): %v", encoded, err)
		}

		if len(cursor) == 0 && len(decoded) == 0 {
			continue
		}

		if !slices.Equal(decoded, cursor) {
			t.Errorf("%q came back as %q through %q", cursor, decoded, encoded)
			t.Log("A key value is the server's own text and may hold a comma, a quote or nothing at")
			t.Log("all; a cursor that split on a separator would resume after the wrong row.")
		}
	}
}

func TestACheckpointNoCommittedChunkCouldLeaveIsRefused(t *testing.T) {
	valid := Checkpoint{Version: "20260727T153000", Direction: ir.Up, Step: 0, Chunks: 1}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a checkpoint of one committed chunk was refused: %v", err)
	}

	for name, cp := range map[string]Checkpoint{
		"no version":     {Direction: ir.Up, Chunks: 1},
		"no direction":   {Version: "20260727T153000", Chunks: 1},
		"a step before":  {Version: "20260727T153000", Direction: ir.Up, Step: -1, Chunks: 1},
		"negative count": {Version: "20260727T153000", Direction: ir.Up, Chunks: -1},
	} {
		if err := cp.Validate(); err == nil {
			t.Errorf("a checkpoint with %s was accepted", name)
		}
	}
}

func TestTheCheckpointTableIsNamedAfterItsHistoryAndKeyedByMigrationAndDirection(t *testing.T) {
	ops := ProgressSchema("migr_migrations")

	created, ok := ops[0].(ir.CreateTable)
	if len(ops) != 1 || !ok || created.Table != "migr_migrations_progress" ||
		!slices.Equal(created.PrimaryKey, []string{"version", "direction"}) {
		t.Fatalf("the checkpoint schema is %+v", ops)
	}

	for _, op := range ops {
		if err := op.Validate(); err != nil {
			t.Errorf("the checkpoint schema does not validate: %v", err)
		}
	}
}
