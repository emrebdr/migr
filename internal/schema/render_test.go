package schema

import (
	"slices"
	"strings"
	"testing"
)

// rendered renders a snapshot as the lines a comparison of two moments is made of.
func rendered(t *testing.T, s Snapshot) []string {
	t.Helper()

	out := Render(s)
	if out == "" {
		return nil
	}

	return strings.Split(out, "\n")
}

// exact builds a column whose type the engine answered precisely, which most of them are.
func exact(t *testing.T, name, logical, native string, nullable bool) Column {
	t.Helper()

	parsed, err := Parse(logical)
	if err != nil {
		t.Fatalf("Parse(%q): %v", logical, err)
	}

	held, err := ExactType(parsed, native)
	if err != nil {
		t.Fatalf("ExactType(%q): %v", logical, err)
	}

	return Column{Name: name, Type: held, Nullable: nullable}
}

func TestATableWithNoColumnsLeftStillRenders(t *testing.T) {
	lines := rendered(t, Snapshot{Tables: []Table{{Name: "emptied"}}})

	if len(lines) != 1 || !strings.Contains(lines[0], "emptied") {
		t.Fatalf("a table drop_column emptied renders as %v", lines)
	}

	if Render(Snapshot{Tables: []Table{{Name: "emptied"}}}) == Render(Snapshot{}) {
		t.Error("an emptied table renders the same as no table at all")
		t.Log("Every other line here describes contents, and PostgreSQL holds a relation with no")
		t.Log("columns -- measured. Without the table's own line a rollback that dropped a table it")
		t.Log("was meant to empty would compare equal to one that did neither.")
	}
}

func TestTheRenderingIsSortedSoTwoMomentsOfOneSchemaRenderAlike(t *testing.T) {
	lines := rendered(t, Snapshot{
		Tables: []Table{
			{Name: "zebra", Columns: []Column{exact(t, "id", "int64", "bigint", false)}},
			{Name: "alpha", Columns: []Column{exact(t, "id", "int64", "bigint", false)}},
		},
		Views: []View{{Name: "listing", Query: "SELECT id FROM alpha"}},
	})

	if !slices.IsSorted(lines) {
		t.Errorf("the rendering is not in a stable order:\n%v", lines)
	}
}

func TestTheOrderColumnsWereWrittenInDoesNotChangeTheRendering(t *testing.T) {
	id, email := exact(t, "id", "int64", "bigint", false), exact(t, "email", "text", "text", true)

	one := Snapshot{Tables: []Table{{Name: "users", Columns: []Column{id, email}}}}
	other := Snapshot{Tables: []Table{{Name: "users", Columns: []Column{email, id}}}}

	if Render(one) != Render(other) {
		t.Errorf("two orderings of one table render differently:\n%s\n---\n%s",
			Render(one), Render(other))
		t.Log("Canonical first is what makes this a rendering of a snapshot rather than of a")
		t.Log("catalog: a reversed drop_column appends the column and no v1 operation can put it")
		t.Log("back where it was, which is what MySQL's digest had to sort its own lines to survive.")
	}
}

func TestRenderNamesEveryThingAColumnHolds(t *testing.T) {
	literal := "false"

	column := exact(t, "verified", "bool", "boolean", false)
	column.Default = &ColumnDefault{Literal: &literal}

	got := Render(Snapshot{Tables: []Table{{Name: "users", Columns: []Column{column}}}})

	for _, want := range []string{"users.verified", "bool", "not null", `default "false"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendering does not name %q:\n%s", want, got)
		}
	}
}

func TestAGuessedTypeRendersAsAGuessAndAnExactOneAsTheLogicalTypeAlone(t *testing.T) {
	best, err := Parse("int32")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	guessed, err := AmbiguousType(best, "integer")
	if err != nil {
		t.Fatalf("AmbiguousType: %v", err)
	}

	got := Render(Snapshot{Tables: []Table{{Name: "t", Columns: []Column{
		{Name: "guessed", Type: guessed},
		exact(t, "known", "int64", "bigint", false),
	}}}})

	if !strings.Contains(got, "int32? (integer)") {
		t.Errorf("a guessed type does not render as a guess:\n%s", got)
	}

	if strings.Contains(got, "bigint") {
		t.Errorf("an exact type renders its native spelling:\n%s", got)
		t.Log("An exact type is the logical type alone, which is the vocabulary this milestone is")
		t.Log("about -- a native spelling would make two engines' renderings differ for no reason.")
	}
}

func TestWhatAnOperationCannotExpressIsRenderedRatherThanDropped(t *testing.T) {
	got := Render(Snapshot{Tables: []Table{{
		Name:    "users",
		Beyond:  &Unrepresented{Detail: "it is WITHOUT ROWID"},
		Indexes: []Index{{Name: "idx", Columns: []string{"id"}, Beyond: &Unrepresented{Detail: "it is partial"}}},
	}}})

	for _, want := range []string{"beyond: it is WITHOUT ROWID", "beyond: it is partial"} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendering does not carry %q:\n%s", want, got)
		}
	}

	t.Log("A marker is part of what the schema holds, so a round trip that lost one must show.")
}

func TestEngineTextStaysOneLineOfTheRendering(t *testing.T) {
	got := Render(Snapshot{Views: []View{{Name: "listing", Query: "SELECT id\n  FROM users"}}})

	if strings.Count(got, "\n") != 0 {
		t.Errorf("a body holding a newline renders as more than one line:\n%s", got)
		t.Log("Lines are sorted and compared one by one, so a body spanning several would interleave")
		t.Log("with other objects and reorder itself between two moments of one schema.")
	}
}

func TestAnUnnamedConstraintIsStillQualifiedByItsTable(t *testing.T) {
	got := Render(Snapshot{Tables: []Table{
		{Name: "a", Checks: []Check{{Expression: "id > 0"}}},
		{Name: "b", Checks: []Check{{Expression: "id > 0"}}},
	}})

	if lines := strings.Split(got, "\n"); len(lines) != 4 {
		t.Fatalf("two tables each holding one unnamed check render as %d lines:\n%s",
			len(lines), got)
	}

	for _, want := range []string{"a.<unnamed>", "b.<unnamed>"} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendering does not qualify %q by its table:\n%s", want, got)
		}
	}

	t.Log("An engine can name a check nothing, and the lines are sorted -- so two unnamed ones")
	t.Log("rendering identically would collapse into one place and hide which table held which.")
}

func TestDifferencePrintsOnlyWhatDisagreed(t *testing.T) {
	kept := Table{Name: "kept", Columns: []Column{exact(t, "id", "int64", "bigint", false)}}

	before := Render(Snapshot{Tables: []Table{kept,
		{Name: "gone", Columns: []Column{exact(t, "id", "int64", "bigint", false)}}}})
	after := Render(Snapshot{Tables: []Table{kept,
		{Name: "added", Columns: []Column{exact(t, "id", "int64", "bigint", false)}}}})

	got := Difference(before, after)

	if strings.Contains(got, "kept") {
		t.Errorf("the difference names an object both schemas hold:\n%s", got)
		t.Log("A failure that printed both schemas whole would make an operator find the one line")
		t.Log("themselves, and on a database several suites share that is most of a screen.")
	}

	for _, want := range []string{"- table       gone", "+ table       added"} {
		if !strings.Contains(got, want) {
			t.Errorf("the difference does not carry %q:\n%s", want, got)
		}
	}
}

func TestTwoEqualSchemasDifferInNothing(t *testing.T) {
	held := Snapshot{Tables: []Table{{Name: "users",
		Columns: []Column{exact(t, "id", "int64", "bigint", false)}}}}

	if got := Difference(Render(held), Render(held)); got != "" {
		t.Errorf("two renderings of one schema differ:\n%s", got)
	}
}

func TestTwoSnapshotsThatCompareEqualRenderIdentically(t *testing.T) {
	id := exact(t, "id", "int64", "bigint", false)

	keyed := func(onDelete ReferentialAction) Snapshot {
		return Snapshot{Tables: []Table{
			{Name: "users", Columns: []Column{id}},
			{Name: "orders", Columns: []Column{id}, ForeignKeys: []ForeignKey{{
				Name:              "fk_orders_user",
				Columns:           []string{"id"},
				ReferencesTable:   "users",
				ReferencesColumns: []string{"id"},
				OnDelete:          onDelete,
			}}},
		}}
	}

	omitted, stated := keyed(""), keyed(ActionNoAction)

	if !omitted.Equal(stated) {
		t.Fatal("an omitted referential action does not compare equal to an explicit no_action")
	}

	if Render(omitted) != Render(stated) {
		t.Errorf("two schemas that compare equal render differently:\n%s",
			Difference(Render(omitted), Render(stated)))
		t.Log("Canonical is what does this, and sorting the lines cannot: an omitted ON DELETE and")
		t.Log("an explicit no_action are the same key, so a round trip comparing two renderings")
		t.Log("would report a schema that was in fact restored as changed.")
	}
}
