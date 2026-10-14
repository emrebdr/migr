package sqlite

import (
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

// stagedRows is a table of two rows, one whose nickname is NULL and one whose nickname is set.
func stagedRows(t *testing.T, c *sqliteConn) {
	t.Helper()

	apply(t, c, ir.CreateTable{Table: "people", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
		{Name: "email", Type: mustType(t, "string(255)")},
		{Name: "nickname", Type: mustType(t, "string(64)"), Nullable: true},
		{Name: "email_key", Type: mustType(t, "string(255)"), Nullable: true},
		{Name: "active", Type: mustType(t, "bool"), Nullable: true},
	}, PrimaryKey: []string{"id"}})
	apply(t, c, ir.Raw{Driver: Name, SQL: `INSERT INTO "people" ("id", "email", "nickname") ` +
		`VALUES (1, '  Ada@Example.COM', NULL), (2, 'bob@example.com', 'bobby')`})
}

// column returns one column of every row of a table, in key order, as SQLite quotes each value.
func column(t *testing.T, c *sqliteConn, table, name string) []string {
	t.Helper()

	var values []string

	for _, row := range read(t, c, `SELECT quote("`+name+`") FROM "`+table+`" ORDER BY "id"`).Values {
		values = append(values, *row[0])
	}

	return values
}

func TestABackfillFillsTheNullsAndNothingElse(t *testing.T) {
	c := openFile(t)
	stagedRows(t, c)

	apply(t, c, ir.DataBackfill{Table: "people", Column: "nickname", Type: mustType(t, "string(64)"),
		Value: ir.Literal("anonymous")})

	if got := column(t, c, "people", "nickname"); !slices.Equal(got, []string{"'anonymous'", "'bobby'"}) {
		t.Errorf("nickname is %v after the backfill, want the NULL filled and bobby kept", got)
		t.Log("A backfill that overwrote set values would be the destructive operation the IR says")
		t.Log("it is not -- which is what lets a project run it without permitting destruction.")
	}
}

func TestATransformRewritesTheTextOfEveryRowAndAssignsRatherThanCasts(t *testing.T) {
	c := openFile(t)
	stagedRows(t, c)

	apply(t, c, ir.DataTransform{Table: "people", Column: "email_key", Type: mustType(t,
		"string(255)"), From: "email", Function: ir.TransformLower})
	apply(t, c, ir.DataTransform{Table: "people", Column: "email_key", Type: mustType(t,
		"string(255)"), From: "email_key", Function: ir.TransformTrim})

	want := []string{"'ada@example.com'", "'bob@example.com'"}
	if got := column(t, c, "people", "email_key"); !slices.Equal(got, want) {
		t.Errorf("email_key is %v, want %v", got, want)
	}

	apply(t, c, ir.DataBackfill{Table: "people", Column: "active", Type: mustType(t, "bool"),
		Value: ir.Literal("true")})

	if got := column(t, c, "people", "active"); !slices.Equal(got, []string{"'true'", "'true'"}) {
		t.Errorf("active is %v, want the literal as an insert would store it", got)
		t.Log("CAST('true' AS boolean) is the integer 0 on this engine, because boolean has NUMERIC")
		t.Log("affinity -- so a cast would silently store the opposite of what was written.")
	}
}

func TestAMisspelledColumnFailsTheStatementRatherThanTheData(t *testing.T) {
	c := openFile(t)
	stagedRows(t, c)

	cases := []ir.Operation{
		ir.DataTransform{Table: "people", Column: "email_key", Type: mustType(t, "string(255)"),
			From: "emial", Function: ir.TransformLower},
		ir.DataCopy{From: "people", To: "people", Columns: []ir.CopyColumn{{From: "emial",
			To: "email"}}},
	}

	for _, op := range cases {
		stmts, err := compile(op)
		if err != nil {
			continue
		}

		if err := c.Exec(t.Context(), stmts[0]); err == nil {
			t.Errorf("%s naming a column that is not there ran: %s", op.Kind(), stmts[0].SQL)
			t.Log("SQLite reads a double-quoted name that resolves to nothing as a string literal,")
			t.Log("so an unqualified \"emial\" would write the text 'emial' into every row.")
		}
	}

	if got := column(t, c, "people", "email_key"); got[0] != "NULL" {
		t.Errorf("email_key is %v after two refused statements, want it untouched", got)
	}
}

func TestACopyInsertsEveryRowIntoTheColumnsItNames(t *testing.T) {
	c := openFile(t)
	stagedRows(t, c)

	apply(t, c, ir.CreateTable{Table: "archive", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
		{Name: "address", Type: mustType(t, "string(255)")},
	}, PrimaryKey: []string{"id"}})
	apply(t, c, ir.DataCopy{From: "people", To: "archive", Columns: []ir.CopyColumn{
		{From: "id", To: "id"}, {From: "email", To: "address"},
	}})

	want := []string{"'  Ada@Example.COM'", "'bob@example.com'"}
	if got := column(t, c, "archive", "address"); !slices.Equal(got, want) {
		t.Errorf("the archive holds %v, want every email under the column it was copied into", got)
	}
}
