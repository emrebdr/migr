package fold_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/fold"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// What the round trip cannot reach is tested here, and the two kinds are worth telling apart. A
// refusal is one half: internal/diff never emits an operation that would draw one, so nothing in
// the round trip can. The other half is what every engine does on its own that no payload records --
// the NOT NULL a primary key implies, the constraints a dropped column takes with it -- which the
// differ cannot emit either, because it refuses to propose a key over a nullable column at all.

func TestEveryOperationFoldsOrSaysWhyItCannot(t *testing.T) {
	for _, kind := range ir.Kinds() {
		op, held := representative(t, kind)
		if !held {
			t.Errorf("nothing here folds a %s and nothing says why", kind)

			continue
		}

		_, refused := fold.Forward(foldable(t), ir.Operations{op})

		if kind == ir.KindRaw {
			checkUnfoldable(t, refused)

			continue
		}

		for _, problem := range refused {
			t.Errorf("folding a %s onto a schema that supports it refused it: %s",
				kind, problem.Describe())
		}
	}

	t.Log("Coverage is derived from ir.Kinds(), so a twenty-first operation fails this until")
	t.Log("somebody folds it or says in representative why it cannot be folded.")
}

// checkUnfoldable holds engine SQL to being the one operation that ends a projection.
func checkUnfoldable(t *testing.T, refused []fold.Refusal) {
	t.Helper()

	if len(refused) != 1 {
		t.Fatalf("folding engine SQL produced %d refusals, want one", len(refused))
	}

	if !refused[0].Unfoldable() {
		t.Error("engine SQL is refused and does not report itself unfoldable")
	}

	if !strings.Contains(refused[0].Reason, "not knowable by folding") {
		t.Errorf("the refusal does not say the schema is unknowable: %s", refused[0].Describe())
	}

	t.Log("A statement's effect on the schema is not readable, so a list carrying one leaves the")
	t.Log("schema from that point on unknowable -- which is why a caller refuses such a project")
	t.Log("whole rather than diffing against a partial fold that would propose dropping whatever")
	t.Log("the SQL built.")
}

func TestAPrimaryKeyTightensTheColumnsItCovers(t *testing.T) {
	held, refused := fold.Forward(foldable(t), ir.Operations{
		ir.AddPrimaryKey{Table: "loose_things", Name: "loose_pkey", Columns: []string{"id"}},
	})

	if len(refused) != 0 {
		t.Fatalf("keying a nullable column refused it: %v", describe(refused))
	}

	table, _ := held.Table("loose_things")

	column, carries := table.Column("id")
	if !carries {
		t.Fatal("the keyed column is gone")
	}

	if column.Nullable {
		t.Error("the column the key covers still accepts null")
		t.Log("Every engine makes a primary key's columns NOT NULL whether the migration said so")
		t.Log("or not -- M9-T7 measured it, and engine.Unrestorable refuses over it. A projection")
		t.Log("that left them nullable would describe a schema no server produces, and would have")
		t.Log("migr diff propose tightening columns the engine had already tightened.")
	}

	t.Log("internal/diff refuses to propose a key over a column the target schema calls nullable,")
	t.Log("so the round trip cannot reach this at all -- it is reachable from M10-T3's direction,")
	t.Log("where the operations come from a migration rather than from a differ.")
}

func TestADroppedColumnTakesEveryConstraintThatCoveredItWithIt(t *testing.T) {
	held, refused := fold.Forward(foldable(t), ir.Operations{
		ir.DropColumn{Table: "things", Column: "code"},
	})

	if len(refused) != 0 {
		t.Fatalf("dropping a covered column refused it: %v", describe(refused))
	}

	table, _ := held.Table("things")

	if len(table.Indexes) != 0 || len(table.Uniques) != 0 {
		t.Errorf("the index and the unique over the dropped column survived it: %+v", table)
		t.Log("Every engine drops them with the column, which is M3-T7's own measurement.")
	}

	if names := keyNames(table.ForeignKeys); !slices.Equal(names, []string{"things_id_fk"}) {
		t.Errorf("the foreign keys read %v, want only the one over a column that is still there",
			names)
	}

	if table.PrimaryKey == nil {
		t.Error("the key over a column that is still there went with an unrelated drop")
	}

	keyed, _ := fold.Forward(foldable(t), ir.Operations{
		ir.DropColumn{Table: "things", Column: "id"},
	})

	table, _ = keyed.Table("things")

	if table.PrimaryKey != nil {
		t.Error("the primary key over the dropped column survived it")
	}

	if names := keyNames(table.ForeignKeys); !slices.Equal(names, []string{"things_code_fk"}) {
		t.Errorf("the foreign keys read %v, want only the one over a column that is still there",
			names)
	}
}

// keyNames names a table's foreign keys, since what a drop leaves is which of them is still there.
func keyNames(held []schema.ForeignKey) []string {
	names := make([]string, 0, len(held))
	for _, one := range held {
		names = append(names, one.Name)
	}

	return names
}

func TestADroppedTableTakesEveryReferenceToItWithIt(t *testing.T) {
	held, refused := fold.Forward(referencing(t), ir.Operations{
		ir.DropTable{Table: "parents"},
	})

	if len(refused) != 0 {
		t.Fatalf("dropping a referenced table refused it: %v", describe(refused))
	}

	if _, still := held.Table("parents"); still {
		t.Fatal("the dropped table is still there")
	}

	table, _ := held.Table("children")
	if len(table.ForeignKeys) != 0 {
		t.Errorf("a foreign key to the dropped table survived it: %+v", table.ForeignKeys)
	}
}

func TestAnOperationNamingWhatIsNotThereIsRefusedRatherThanGuessedAt(t *testing.T) {
	cases := []struct {
		name string
		op   ir.Operation
		says string
	}{
		{"a column onto a table nothing created",
			ir.AddColumn{Table: "ghosts", Column: ir.Column{Name: "c", Type: logical(t, "text")}},
			"holds no such table"},
		{"a column off a table nothing created",
			ir.DropColumn{Table: "ghosts", Column: "c"}, "holds no such table"},
		{"a column the table does not hold",
			ir.DropColumn{Table: "things", Column: "ghost"}, `holds no column "ghost"`},
		{"a column the table already holds",
			ir.AddColumn{Table: "things", Column: ir.Column{Name: "loose",
				Type: logical(t, "text"), Nullable: true}}, `already holds a column "loose"`},
		{"a check the table already carries",
			ir.AddCheck{Table: "things", Name: "things_positive", Expression: "id > 1"},
			"already carries a check constraint"},
		{"a unique the table already carries",
			ir.AddUnique{Table: "things", Name: "things_code_key", Columns: []string{"loose"}},
			"already carries a unique constraint"},
		{"a reference the table already carries",
			ir.AddForeignKey{Table: "things", Name: "things_id_fk", Columns: []string{"loose"},
				ReferencesTable: "loose_things", ReferencesColumns: []string{"id"}},
			"already carries a foreign key"},
		{"an alteration of a column the table does not hold",
			ir.AlterColumn{Table: "things", Column: "ghost", Nullable: pointerTo(true)},
			`holds no column "ghost"`},
		{"an index over a column nothing created",
			ir.AddIndex{Table: "things", Name: "i", Columns: []string{"ghost"}},
			`covers column "things.ghost"`},
		{"an index name the table already carries",
			ir.AddIndex{Table: "things", Name: "things_code_idx", Columns: []string{"loose"}},
			"already carries an index"},
		{"an index the table does not carry",
			ir.DropIndex{Table: "things", Name: "ghost"}, `carries no index "ghost"`},
		{"a unique over a column nothing created",
			ir.AddUnique{Table: "things", Name: "u", Columns: []string{"ghost"}},
			`covers column "things.ghost"`},
		{"a unique the table does not carry",
			ir.DropUnique{Table: "things", Name: "ghost"}, `carries no unique constraint "ghost"`},
		{"a check the table does not carry",
			ir.DropCheck{Table: "things", Name: "ghost"}, `carries no check constraint "ghost"`},
		{"a second primary key",
			ir.AddPrimaryKey{Table: "things", Name: "k", Columns: []string{"id"}},
			"already has a primary key"},
		{"a key off a table that has none",
			ir.DropPrimaryKey{Table: "loose_things", Name: "k"}, "has no primary key to drop"},
		{"a column onto a table whose type no schema can hold",
			ir.AddColumn{Table: "things", Column: ir.Column{Name: "broken"}},
			"not a usable operation"},
		{"a reference to a table nothing created",
			ir.AddForeignKey{Table: "things", Name: "f", Columns: []string{"loose"},
				ReferencesTable: "ghosts", ReferencesColumns: []string{"id"}},
			`references table "ghosts"`},
		{"a reference to a column nothing created",
			ir.AddForeignKey{Table: "things", Name: "f", Columns: []string{"loose"},
				ReferencesTable: "things", ReferencesColumns: []string{"ghost"}},
			`references column "things.ghost"`},
		{"a reference the table does not carry",
			ir.DropForeignKey{Table: "things", Name: "ghost"}, `carries no foreign key "ghost"`},
		{"a table that is already there",
			ir.CreateTable{Table: "things", Columns: []ir.Column{
				{Name: "id", Type: logical(t, "int64")}}}, "already holds a table of that name"},
		{"a table nothing created",
			ir.DropTable{Table: "ghosts"}, "holds no such table"},
		{"a rename of a table nothing created",
			ir.RenameTable{From: "ghosts", To: "spirits"}, "holds no such table"},
		{"a rename onto a name that is taken",
			ir.RenameTable{From: "things", To: "loose_things"},
			"already holds a table of that name"},
		{"a rename of a column the table does not hold",
			ir.RenameColumn{Table: "things", From: "ghost", To: "spirit"},
			`holds no column "ghost"`},
		{"a rename of a column onto a name that is taken",
			ir.RenameColumn{Table: "things", From: "loose", To: "id"},
			`already holds a column "id"`},
		{"a view nothing created",
			ir.DropView{View: "ghosts"}, "holds no such view"},
		{"a view that is already there and is not being replaced",
			ir.CreateView{View: "seen", Query: "SELECT 1"},
			"already holds a view of that name"},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			before := constrained(t)

			after, refused := fold.Forward(before, ir.Operations{one.op})

			if len(refused) != 1 {
				t.Fatalf("folding it produced %d refusals, want one", len(refused))
			}

			if !strings.Contains(refused[0].Reason, one.says) {
				t.Errorf("the refusal does not say %q:\n%s", one.says, refused[0].Describe())
			}

			if refused[0].Kind != one.op.Kind() {
				t.Errorf("the refusal names a %s, want a %s", refused[0].Kind, one.op.Kind())
			}

			if !after.Equal(before.Canonical()) {
				t.Errorf("a refused operation changed the schema:\n%s", render(t, after))
			}
		})
	}

	t.Log("It folds and it does not validate: an operation naming what is not there is a refusal")
	t.Log("rather than a panic or a silent create, which is what makes the same fold readable as")
	t.Log("M10-T3's reference-integrity check -- there, these refusals are the findings.")
}

func TestARefusalStopsThatOperationAndNotTheOnesAfterIt(t *testing.T) {
	held, refused := fold.Forward(foldable(t), ir.Operations{
		ir.AddColumn{Table: "ghosts", Column: ir.Column{Name: "c", Type: logical(t, "text")}},
		ir.AddColumn{Table: "things", Column: ir.Column{Name: "tag",
			Type: logical(t, "string(8)"), Nullable: true}},
	})

	if len(refused) != 1 {
		t.Fatalf("want one refusal, got %v", describe(refused))
	}

	if refused[0].Index != 1 {
		t.Errorf("the refusal names operation %d, want 1", refused[0].Index)
	}

	table, _ := held.Table("things")
	if _, carries := table.Column("tag"); !carries {
		t.Error("the operation after the refused one was dropped with it")
	}
}

func TestTheSameListFoldsToTheSameSchemaTwice(t *testing.T) {
	ops := ir.Operations{
		ir.AddColumn{Table: "things", Column: ir.Column{Name: "a",
			Type: logical(t, "text"), Nullable: true}},
		ir.AddColumn{Table: "things", Column: ir.Column{Name: "b",
			Type: logical(t, "text"), Nullable: true}},
		ir.AddIndex{Table: "things", Name: "i", Columns: []string{"a", "b"}},
	}

	first, _ := fold.Forward(foldable(t), ops)
	second, _ := fold.Forward(foldable(t), ops)

	if render(t, first) != render(t, second) {
		t.Errorf("the same list folded two ways:\n%s\n%s", render(t, first), render(t, second))
	}
}

func TestFoldingChangesNothingAboutTheSchemaItWasGiven(t *testing.T) {
	before := constrained(t)
	kept := render(t, before)

	if _, refused := fold.Forward(before, ir.Operations{
		ir.DropColumn{Table: "things", Column: "code"},
		ir.DropTable{Table: "things"},
	}); len(refused) != 0 {
		t.Fatalf("the fixture refused: %v", describe(refused))
	}

	if now := render(t, before); now != kept {
		t.Errorf("folding wrote through to its input:\n%s\n%s", kept, now)
	}
}

// representative returns one operation of each kind for the coverage check, and the kind it is not.
func representative(t *testing.T, kind ir.Kind) (ir.Operation, bool) {
	t.Helper()

	held := map[ir.Kind]ir.Operation{
		ir.KindCreateTable: ir.CreateTable{Table: "fresh", Columns: []ir.Column{
			{Name: "id", Type: logical(t, "int64")}}, PrimaryKey: []string{"id"}},
		ir.KindDropTable:   ir.DropTable{Table: "things"},
		ir.KindRenameTable: ir.RenameTable{From: "things", To: "stuff"},
		ir.KindAddColumn: ir.AddColumn{Table: "things", Column: ir.Column{Name: "tag",
			Type: logical(t, "string(8)"), Nullable: true}},
		ir.KindDropColumn:   ir.DropColumn{Table: "things", Column: "loose"},
		ir.KindRenameColumn: ir.RenameColumn{Table: "things", From: "loose", To: "slack"},
		ir.KindAlterColumn: ir.AlterColumn{Table: "things", Column: "loose",
			Nullable: pointerTo(false)},
		ir.KindAddIndex:   ir.AddIndex{Table: "things", Name: "i", Columns: []string{"loose"}},
		ir.KindDropIndex:  ir.DropIndex{Table: "things", Name: "things_code_idx"},
		ir.KindAddUnique:  ir.AddUnique{Table: "things", Name: "u", Columns: []string{"loose"}},
		ir.KindDropUnique: ir.DropUnique{Table: "things", Name: "things_code_key"},
		ir.KindAddCheck:   ir.AddCheck{Table: "things", Name: "c", Expression: "id > 0"},
		ir.KindDropCheck:  ir.DropCheck{Table: "things", Name: "things_positive"},
		ir.KindAddPrimaryKey: ir.AddPrimaryKey{Table: "loose_things", Name: "k",
			Columns: []string{"id"}},
		ir.KindDropPrimaryKey: ir.DropPrimaryKey{Table: "things", Name: "things_pkey"},
		ir.KindAddForeignKey: ir.AddForeignKey{Table: "things", Name: "f",
			Columns: []string{"loose"}, ReferencesTable: "loose_things",
			ReferencesColumns: []string{"id"}},
		ir.KindDropForeignKey: ir.DropForeignKey{Table: "things", Name: "things_id_fk"},
		ir.KindCreateView:     ir.CreateView{View: "fresh_view", Query: "SELECT 1"},
		ir.KindDropView:       ir.DropView{View: "seen"},
		ir.KindRaw:            ir.Raw{Driver: "sqlite", SQL: "ANALYZE"},
	}

	op, ok := held[kind]

	return op, ok
}

// This one fixture carries everything the cases here need: a table with a key, a column three
// constraints cover, a nullable column, two references to a second table, and a view. Every
// operation in representative folds onto it, and dropping one of its columns takes three with it.

// foldable is the schema the coverage check folds every operation onto, which supports all of them.
func foldable(t *testing.T) schema.Snapshot {
	t.Helper()

	return schema.Snapshot{
		Tables: []schema.Table{
			{
				Name: "things",
				Columns: []schema.Column{
					{Name: "id", Type: exact(t, "int64")},
					{Name: "code", Type: exact(t, "string(32)")},
					{Name: "loose", Type: exact(t, "string(16)"), Nullable: true},
				},
				PrimaryKey: &schema.PrimaryKey{Name: "things_pkey", Columns: []string{"id"}},
				Indexes:    []schema.Index{{Name: "things_code_idx", Columns: []string{"code"}}},
				Uniques:    []schema.Unique{{Name: "things_code_key", Columns: []string{"code"}}},
				Checks:     []schema.Check{{Name: "things_positive", Expression: "id > 0"}},
				ForeignKeys: []schema.ForeignKey{
					{Name: "things_id_fk", Columns: []string{"id"},
						ReferencesTable: "loose_things", ReferencesColumns: []string{"id"},
						OnDelete: schema.ActionNoAction, OnUpdate: schema.ActionNoAction},
					{Name: "things_code_fk", Columns: []string{"code"},
						ReferencesTable: "loose_things", ReferencesColumns: []string{"tag"},
						OnDelete: schema.ActionCascade, OnUpdate: schema.ActionNoAction},
				},
			},
			{
				Name: "loose_things",
				Columns: []schema.Column{
					{Name: "id", Type: exact(t, "int64"), Nullable: true},
					{Name: "tag", Type: exact(t, "string(32)"), Nullable: true},
				},
			},
		},
		Views: []schema.View{{Name: "seen", Query: "SELECT 1"}},
	}
}

// constrained is the schema the refusals are measured against, which is the same one.
func constrained(t *testing.T) schema.Snapshot {
	t.Helper()

	return foldable(t)
}

// referencing is the two-table schema whose second table points at the first.
func referencing(t *testing.T) schema.Snapshot {
	t.Helper()

	return schema.Snapshot{Tables: []schema.Table{
		{
			Name:       "parents",
			Columns:    []schema.Column{{Name: "id", Type: exact(t, "int64")}},
			PrimaryKey: &schema.PrimaryKey{Name: "parents_pkey", Columns: []string{"id"}},
		},
		{
			Name: "children",
			Columns: []schema.Column{
				{Name: "id", Type: exact(t, "int64")},
				{Name: "parent_id", Type: exact(t, "int64"), Nullable: true},
			},
			ForeignKeys: []schema.ForeignKey{{Name: "children_parent_fk",
				Columns: []string{"parent_id"}, ReferencesTable: "parents",
				ReferencesColumns: []string{"id"},
				OnDelete:          schema.ActionCascade, OnUpdate: schema.ActionNoAction}},
		},
	}}
}

// logical parses one of these fixtures' logical types, which are all written out in this package.
func logical(t *testing.T, spelling string) schema.Type {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	return parsed
}

// pointerTo returns a pointer to a value, which is how an alteration states a changed attribute.
func pointerTo[T any](v T) *T {
	return &v
}

// describe renders refusals for a failure, which is the whole of what one of them says.
func describe(refused []fold.Refusal) []string {
	lines := make([]string, 0, len(refused))
	for _, one := range refused {
		lines = append(lines, one.Describe())
	}

	return lines
}

func TestAnEngineTextBodyIsComparedForPresenceAndNotForContent(t *testing.T) {
	live := schema.Snapshot{
		Tables: []schema.Table{{
			Name:    "things",
			Columns: []schema.Column{{Name: "id", Type: exact(t, "int64")}},
			Checks: []schema.Check{
				{Name: "things_positive", Expression: "((id > 0))"},
				{Name: "things_gone", Expression: "((id < 9))"},
			},
		}},
		Views: []schema.View{
			{Name: "seen", Query: " SELECT id\n   FROM things;"},
			{Name: "gone", Query: "SELECT 1"},
		},
	}

	desired := schema.Snapshot{
		Tables: []schema.Table{{
			Name:    "things",
			Columns: []schema.Column{{Name: "id", Type: exact(t, "int64")}},
			Checks: []schema.Check{
				{Name: "things_positive", Expression: "id > 0"},
				{Name: "things_new", Expression: "id < 5"},
			},
		}},
		Views: []schema.View{
			{Name: "seen", Query: "SELECT id FROM things"},
			{Name: "fresh", Query: "SELECT 2"},
		},
	}

	held := fold.Comparable(live, desired)

	table, _ := held.Table("things")

	for _, one := range table.Checks {
		switch one.Name {
		case "things_positive":
			if one.Expression != "((id > 0))" {
				t.Errorf("a check both schemas name reads %q, want the live spelling",
					one.Expression)
			}
		case "things_new":
			if one.Expression != "id < 5" {
				t.Errorf("a check only the desired schema names reads %q, want its own expression",
					one.Expression)
			}
		}
	}

	for _, one := range held.Views {
		switch one.Name {
		case "seen":
			if one.Query != " SELECT id\n   FROM things;" {
				t.Errorf("a view both schemas name reads %q, want the live body", one.Query)
			}
		case "fresh":
			if one.Query != "SELECT 2" {
				t.Errorf("a view only the desired schema names reads %q, want its own body",
					one.Query)
			}
		}
	}

	if _, held := held.View("gone"); held {
		t.Error("a view only the live schema holds was added to the desired one")
	}

	t.Log("Measured on four servers: one check expression `id > 0` reads back four ways and one")
	t.Log("view body reads back three. Compared as written, every migr diff over a database that is")
	t.Log("exactly up to date would propose replacing every view and every check it can see.")
	t.Log("An element only one side holds keeps its own text, which is what lets the operation that")
	t.Log("creates it carry the real body.")
}

func TestComparableChangesNothingAboutTheSchemasItWasGiven(t *testing.T) {
	live, desired := foldable(t), foldable(t)
	kept := render(t, desired)

	fold.Comparable(live, desired)

	if now := render(t, desired); now != kept {
		t.Errorf("Comparable wrote through to its input:\n%s\n%s", kept, now)
	}
}
