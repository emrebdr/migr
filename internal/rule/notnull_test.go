package rule

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

// nullable returns a column of the named type that accepts nothing, which is the safe shape.
func nullable(t *testing.T, name, spelling string) ir.Column {
	t.Helper()

	c := column(t, name, spelling)
	c.Nullable = true

	return c
}

// defaulted returns a NOT NULL column carrying a literal, which is what fills the rows already there.
func defaulted(t *testing.T, name, spelling, value string) ir.Column {
	t.Helper()

	c := column(t, name, spelling)
	c.Default = ir.Literal(value)

	return c
}

func TestANotNullColumnAddedWithNoDefaultIsReported(t *testing.T) {
	found := judging(t, NotNullWithoutDefault, upOf(ir.AddColumn{Table: "users",
		Column: column(t, "verified", "bool")}))

	if len(found) != 1 {
		t.Fatalf("found %d findings, want 1: %v", len(found), found)
	}

	if found[0].Direction != ir.Up || found[0].Operation != 1 ||
		found[0].Kind != ir.KindAddColumn {
		t.Errorf("the finding is %+v, want up operation 1 (add_column)", found[0])
	}

	for _, phrase := range []string{`"verified"`, `"users"`, "bool", "NOT NULL", "no default",
		"opened no database", "nullable"} {
		if !strings.Contains(found[0].Detail, phrase) {
			t.Errorf("detail %q does not say %q", found[0].Detail, phrase)
		}
	}
}

func TestTheWarningSaysWhatItCouldNotCheckRatherThanClaimingTheTableHasRows(t *testing.T) {
	found := judging(t, NotNullWithoutDefault, upOf(ir.AddColumn{Table: "users",
		Column: column(t, "verified", "bool")}))

	if len(found) != 1 {
		t.Fatalf("found %d findings, want 1: %v", len(found), found)
	}

	if !strings.Contains(found[0].Detail, "count that table's rows") {
		t.Errorf("detail %q does not say that the row count is the part nothing asked: ADR 0064 "+
			"settled that migr validate holds no connection for the migrations, so this is a "+
			"warning about what it cannot know", found[0].Detail)
	}
}

func TestAColumnThatCanBeFilledOrLeftEmptyIsNotReported(t *testing.T) {
	no := false

	for _, c := range []struct {
		name string
		up   Half
	}{
		{"it is nullable", upOf(ir.AddColumn{Table: "users",
			Column: nullable(t, "verified", "bool")})},
		{"it carries a default", upOf(ir.AddColumn{Table: "users",
			Column: defaulted(t, "verified", "bool", "false")})},
		{"an operation declaring no column at all", upOf(ir.DropColumn{Table: "users",
			Column: "verified"})},
		{"an alter tightening a column that already exists", upOf(ir.AlterColumn{Table: "users",
			Column: "verified", Nullable: &no})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if found := judging(t, NotNullWithoutDefault, c.up); len(found) != 0 {
				t.Errorf("found %d findings: %v", len(found), found)
			}
		})
	}
}

func TestATableThisMigrationCreatesHasNoRowsForAColumnToFill(t *testing.T) {
	created := ir.CreateTable{Table: "users",
		Columns:    []ir.Column{column(t, "id", "int64")},
		PrimaryKey: []string{"id"}}
	added := ir.AddColumn{Table: "users", Column: column(t, "verified", "bool")}

	for _, c := range []struct {
		name string
		up   Half
	}{
		{"created and then added to", upOf(created, added)},
		{"added to and then created", upOf(added, created)},
		{"declared inline by the table itself", upOf(ir.CreateTable{Table: "users",
			Columns:    []ir.Column{column(t, "id", "int64"), column(t, "verified", "bool")},
			PrimaryKey: []string{"id"}})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if found := judging(t, NotNullWithoutDefault, c.up); len(found) != 0 {
				t.Errorf("found %d findings about a table that cannot hold a row yet: %v",
					len(found), found)
			}
		})
	}

	t.Log("This is the one thing the rule can know about a table's rows with no database open: a")
	t.Log("table this migration creates has none, so the constraint has nothing to fail on.")
}

func TestEveryUnfillableColumnInOneMigrationIsReported(t *testing.T) {
	found := judging(t, NotNullWithoutDefault, upOf(
		ir.AddColumn{Table: "users", Column: column(t, "verified", "bool")},
		ir.AddColumn{Table: "users", Column: nullable(t, "nickname", "string(64)")},
		ir.AddColumn{Table: "orders", Column: column(t, "total", "decimal(12,2)")}))

	if len(found) != 2 {
		t.Fatalf("found %d findings, want 2: %v", len(found), found)
	}

	if found[0].Operation != 1 || found[1].Operation != 3 {
		t.Errorf("the findings name operations %d and %d, want 1 and 3", found[0].Operation,
			found[1].Operation)
	}
}
