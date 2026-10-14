//go:build integration

package postgres

import (
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// column reads one column of every row of a table, in key order, as the server renders each value.
func column(t *testing.T, session *pgConn, table, name string) []string {
	t.Helper()

	rows, err := session.Query(t.Context(), driver.Query{SQL: "SELECT " + quote(name) + "::text FROM " +
		quote(table) + " ORDER BY " + quote("id")})
	if err != nil {
		t.Fatalf("reading %s.%s: %v", table, name, err)
	}

	values := make([]string, 0, len(rows.Values))
	for _, row := range rows.Values {
		if row[0] == nil {
			values = append(values, "NULL")

			continue
		}

		values = append(values, *row[0])
	}

	return values
}

func TestTheDataOperationsWriteTheRowsTheyName(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		apply(t, session, ir.Operations{
			ir.CreateTable{Table: "people", Columns: []ir.Column{
				{Name: "id", Type: probeType(t, "int64")},
				{Name: "email", Type: probeType(t, "string(255)")},
				{Name: "nickname", Type: probeType(t, "string(64)"), Nullable: true},
				{Name: "email_key", Type: probeType(t, "string(255)"), Nullable: true},
				{Name: "age", Type: probeType(t, "text"), Nullable: true},
				{Name: "age_n", Type: probeType(t, "int32"), Nullable: true},
			}, PrimaryKey: []string{"id"}},
			ir.CreateTable{Table: "archive", Columns: []ir.Column{
				{Name: "id", Type: probeType(t, "int64")},
				{Name: "address", Type: probeType(t, "string(255)")},
			}, PrimaryKey: []string{"id"}},
			ir.Raw{Driver: Name, SQL: `INSERT INTO "people" ("id", "email", "nickname", "age") ` +
				`VALUES (1, '  Ada@Example.COM', NULL, ' 41 '), (2, 'bob@example.com', 'bobby', '7')`},
		})

		apply(t, session, ir.Operations{
			ir.DataBackfill{Table: "people", Column: "nickname", Type: probeType(t, "string(64)"),
				Value: ir.Literal("anonymous")},
			ir.DataTransform{Table: "people", Column: "email_key", Type: probeType(t, "string(255)"),
				From: "email", Function: ir.TransformLower},
			ir.DataTransform{Table: "people", Column: "email_key", Type: probeType(t, "string(255)"),
				From: "email_key", Function: ir.TransformTrim},
			ir.DataTransform{Table: "people", Column: "age_n", Type: probeType(t, "int32"),
				From: "age", Function: ir.TransformTrim},
			ir.DataCopy{From: "people", To: "archive", Columns: []ir.CopyColumn{
				{From: "id", To: "id"}, {From: "email", To: "address"}}},
		})

		expect := map[[2]string][]string{
			{"people", "nickname"}:  {"anonymous", "bobby"},
			{"people", "email_key"}: {"ada@example.com", "bob@example.com"},
			{"people", "age_n"}:     {"41", "7"},
			{"archive", "address"}:  {"  Ada@Example.COM", "bob@example.com"},
		}

		for at, want := range expect {
			if got := column(t, session, at[0], at[1]); !slices.Equal(got, want) {
				t.Errorf("%s.%s holds %q, want %q", at[0], at[1], got, want)
			}
		}

		t.Log("age_n is the case this engine needed type for: a text expression is not assigned to")
		t.Log("an integer column at all here, so the transform casts to the column's native type.")
	})
}
