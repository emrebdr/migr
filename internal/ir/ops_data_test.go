package ir

import (
	"errors"
	"strings"
	"testing"
)

func TestADataOperationRefusesWhatItCouldNotPerform(t *testing.T) {
	text := mustType(t, "text")

	cases := []struct {
		name string
		op   Operation
		want string
	}{
		{"a copy naming no source", DataCopy{To: "b", Columns: []CopyColumn{{From: "id", To: "id"}}},
			"from is required"},
		{"a copy into itself", DataCopy{From: "a", To: "a", Columns: []CopyColumn{{From: "id",
			To: "id"}}}, "into itself"},
		{"a copy of no columns", DataCopy{From: "a", To: "b"}, "at least one column"},
		{"a copy writing one column twice", DataCopy{From: "a", To: "b", Columns: []CopyColumn{
			{From: "x", To: "id"}, {From: "y", To: "id"}}}, "already writes"},
		{"a copy pair naming one side", DataCopy{From: "a", To: "b", Columns: []CopyColumn{
			{From: "x"}}}, "both the column"},
		{"a backfill with no type", DataBackfill{Table: "t", Column: "c", Value: Literal("x")},
			"type is required"},
		{"a backfill with no value", DataBackfill{Table: "t", Column: "c", Type: text},
			"value is required"},
		{"a backfill of an unknown function", DataBackfill{Table: "t", Column: "c", Type: text,
			Value: Call("now")}, "unknown default function"},
		{"a transform with an unknown function", DataTransform{Table: "t", Column: "c", Type: text,
			From: "d", Function: "reverse"}, "unknown function"},
		{"a transform copying a column into itself", DataTransform{Table: "t", Column: "c",
			Type: text, From: "c", Function: TransformCopy}, "changes nothing"},
		{"a transform naming no source", DataTransform{Table: "t", Column: "c", Type: text,
			Function: TransformTrim}, "from is required"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.op.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate returned %v, want an error mentioning %q", err, tc.want)
			}
		})
	}

	trimmed := DataTransform{Table: "t", Column: "c", Type: text, From: "c", Function: TransformTrim}
	if err := trimmed.Validate(); err != nil {
		t.Errorf("trimming a column in place was refused: %v", err)
		t.Log("Only copy into itself is a no-op; every other function changes the column it reads.")
	}
}

func TestNoDataOperationHasAnInverse(t *testing.T) {
	for _, f := range fixtures(t) {
		if f.kind.Since() < 2 {
			continue
		}

		_, err := f.op.Reverse()
		if !errors.Is(err, ErrIrreversible) || !strings.Contains(err.Error(), "author a Down") {
			t.Errorf("%s reversed with %v, want an irreversible reason naming the remedy", f.kind, err)
			t.Log("Each overwrites or adds what nothing records, so a derived inverse would be a")
			t.Log("guess -- and invariant #10 makes a guessed rollback worse than none.")
		}
	}
}
