package rule

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// column returns one NOT NULL column of the named logical type, as a migration would declare it.
func column(t *testing.T, name, spelling string) ir.Column {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("schema.Parse(%q): %v", spelling, err)
	}

	return ir.Column{Name: name, Type: parsed}
}

// judging returns what one shipped rule makes of one up half, by asking that rule alone.
func judging(t *testing.T, id ID, up Half) []Finding {
	t.Helper()

	for _, r := range All() {
		if r.ID == id {
			return r.Judge(Subject{Migration: subject(t).Migration, Up: up, Undo: UndoDerived})
		}
	}

	t.Fatalf("no rule is called %q", id)

	return nil
}

// upOf returns an up half holding the given operations and no statements of its own.
func upOf(ops ...ir.Operation) Half {
	return Half{Operations: ops}
}

func TestAKeylessTableIsReported(t *testing.T) {
	found := judging(t, MissingPrimaryKey, upOf(ir.CreateTable{Table: "users",
		Columns: []ir.Column{column(t, "id", "int64")}}))

	if len(found) != 1 {
		t.Fatalf("found %d findings, want 1: %v", len(found), found)
	}

	if found[0].Direction != ir.Up || found[0].Operation != 1 ||
		found[0].Kind != ir.KindCreateTable {
		t.Errorf("the finding is %+v, want up operation 1 (create_table)", found[0])
	}

	for _, phrase := range []string{`"users"`, "no primary key", "this migration"} {
		if !strings.Contains(found[0].Detail, phrase) {
			t.Errorf("detail %q does not say %q", found[0].Detail, phrase)
		}
	}
}

func TestAKeyedTableIsNotReportedHoweverTheKeyArrives(t *testing.T) {
	columns := []ir.Column{column(t, "id", "int64")}

	for _, c := range []struct {
		name string
		up   Half
	}{
		{"declared inline", upOf(ir.CreateTable{Table: "users", Columns: columns,
			PrimaryKey: []string{"id"}})},
		{"added by an operation of its own", upOf(
			ir.CreateTable{Table: "users", Columns: columns},
			ir.AddPrimaryKey{Table: "users", Columns: []string{"id"}})},
		{"added before the table is even mentioned", upOf(
			ir.AddPrimaryKey{Table: "users", Columns: []string{"id"}},
			ir.CreateTable{Table: "users", Columns: columns})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if found := judging(t, MissingPrimaryKey, c.up); len(found) != 0 {
				t.Errorf("found %d findings about a keyed table: %v", len(found), found)
			}
		})
	}
}

func TestAKeyOnAnotherTableDoesNotCountAsThisTablesKey(t *testing.T) {
	found := judging(t, MissingPrimaryKey, upOf(
		ir.CreateTable{Table: "users", Columns: []ir.Column{column(t, "id", "int64")}},
		ir.AddPrimaryKey{Table: "orders", Columns: []string{"id"}}))

	if len(found) != 1 {
		t.Fatalf("found %d findings, want 1 about users: %v", len(found), found)
	}
}

func TestEveryKeylessTableInOneMigrationIsReported(t *testing.T) {
	columns := []ir.Column{column(t, "id", "int64")}

	found := judging(t, MissingPrimaryKey, upOf(
		ir.CreateTable{Table: "users", Columns: columns},
		ir.CreateTable{Table: "orders", Columns: columns, PrimaryKey: []string{"id"}},
		ir.CreateTable{Table: "events", Columns: columns}))

	if len(found) != 2 {
		t.Fatalf("found %d findings, want 2: %v", len(found), found)
	}

	if found[0].Operation != 1 || found[1].Operation != 3 {
		t.Errorf("the findings name operations %d and %d, want 1 and 3",
			found[0].Operation, found[1].Operation)
	}
}

func TestAnUnboundedColumnIsReportedHoweverItArrives(t *testing.T) {
	text, err := schema.Parse("text")
	if err != nil {
		t.Fatalf("schema.Parse: %v", err)
	}

	for _, c := range []struct {
		name string
		up   Half
	}{
		{"declared by a new table", upOf(ir.CreateTable{Table: "posts",
			Columns:    []ir.Column{column(t, "id", "int64"), column(t, "body", "text")},
			PrimaryKey: []string{"id"}})},
		{"added to an existing one", upOf(ir.AddColumn{Table: "posts",
			Column: column(t, "body", "text")})},
		{"altered into one", upOf(ir.AlterColumn{Table: "posts", Column: "body", Type: &text})},
	} {
		t.Run(c.name, func(t *testing.T) {
			found := judging(t, UnboundedString, c.up)
			if len(found) != 1 {
				t.Fatalf("found %d findings, want 1: %v", len(found), found)
			}

			for _, phrase := range []string{`"body"`, "text", "string(n)"} {
				if !strings.Contains(found[0].Detail, phrase) {
					t.Errorf("detail %q does not say %q", found[0].Detail, phrase)
				}
			}
		})
	}
}

func TestABoundedColumnIsNotReported(t *testing.T) {
	nullable := true

	for _, c := range []struct {
		name string
		up   Half
	}{
		{"a string of a declared length", upOf(ir.AddColumn{Table: "posts",
			Column: column(t, "body", "string(255)")})},
		{"a column of another type entirely", upOf(ir.AddColumn{Table: "posts",
			Column: column(t, "views", "int64")})},
		{"an alter that changes no type", upOf(ir.AlterColumn{Table: "posts", Column: "body",
			Nullable: &nullable})},
		{"an operation that declares no column", upOf(ir.DropColumn{Table: "posts",
			Column: "body"})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if found := judging(t, UnboundedString, c.up); len(found) != 0 {
				t.Errorf("found %d findings: %v", len(found), found)
			}
		})
	}
}

func TestNeitherRuleJudgesWhatUndoesAMigration(t *testing.T) {
	keyless := ir.CreateTable{Table: "users", Columns: []ir.Column{column(t, "id", "int64")}}
	unbounded := ir.AddColumn{Table: "posts", Column: column(t, "body", "text")}

	down := Half{Operations: ir.Operations{keyless, unbounded}}

	set, err := NewSet(All(), nil)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	judged := set.Judge(Subject{Migration: subject(t).Migration, Down: down, Undo: UndoAuthored})
	if len(judged) != 0 {
		t.Errorf("a down half was judged, reporting %d findings: %v", len(judged), judged)
	}
}

func TestNeitherRuleReadsAMigrationsOwnStatements(t *testing.T) {
	up := Half{Direct: []driver.Query{{SQL: "CREATE TABLE audit (note text)"}}}

	set, err := NewSet(All(), nil)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	if judged := set.Judge(Subject{Migration: subject(t).Migration, Up: up}); len(judged) != 0 {
		t.Errorf("a raw statement was judged as though it were an operation: %v", judged)
	}
}

func TestARawOperationCarriesNothingEitherRuleCanRead(t *testing.T) {
	set, err := NewSet(All(), nil)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	up := upOf(ir.Raw{Driver: "postgres", SQL: "CREATE TABLE users (id bigint)"})

	if judged := set.Judge(Subject{Migration: subject(t).Migration, Up: up}); len(judged) != 0 {
		t.Errorf("a raw operation was judged, so a .sql project would be linted on text it "+
			"cannot read: %v", judged)
	}
}
