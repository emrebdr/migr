//go:build integration

package mysql

import (
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

func TestTheDataOperationsWriteTheRowsTheyName(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		people, archive := owned(t, "people"), owned(t, "archive")
		dropped(t, c, people, archive)

		applied(t, c, ir.Operations{
			ir.CreateTable{Table: people, Columns: []ir.Column{
				{Name: "id", Type: liveType(t, "int64")},
				{Name: "email", Type: liveType(t, "string(255)")},
				{Name: "nickname", Type: liveType(t, "string(64)"), Nullable: true},
				{Name: "email_key", Type: liveType(t, "string(255)"), Nullable: true},
				{Name: "active", Type: liveType(t, "bool"), Nullable: true},
			}, PrimaryKey: []string{"id"}},
			ir.CreateTable{Table: archive, Columns: []ir.Column{
				{Name: "id", Type: liveType(t, "int64")},
				{Name: "address", Type: liveType(t, "string(255)")},
			}, PrimaryKey: []string{"id"}},
		})
		raw(t, c, "INSERT INTO "+quote(people)+" (`id`, `email`, `nickname`) VALUES "+
			"(1, '  Ada@Example.COM', NULL), (2, 'bob@example.com', 'bobby')")

		applied(t, c, ir.Operations{
			ir.DataBackfill{Table: people, Column: "nickname", Type: liveType(t, "string(64)"),
				Value: ir.Literal("anonymous")},
			ir.DataBackfill{Table: people, Column: "active", Type: liveType(t, "bool"),
				Value: ir.Literal("true")},
			ir.DataTransform{Table: people, Column: "email_key", Type: liveType(t, "string(255)"),
				From: "email", Function: ir.TransformLower},
			ir.DataTransform{Table: people, Column: "email_key", Type: liveType(t, "string(255)"),
				From: "email_key", Function: ir.TransformTrim},
			ir.DataCopy{From: people, To: archive, Columns: []ir.CopyColumn{
				{From: "id", To: "id"}, {From: "email", To: "address"}}},
		})

		expect := map[string][]string{
			"SELECT `nickname` FROM " + quote(people) + " ORDER BY `id`":  {"anonymous", "bobby"},
			"SELECT `active` FROM " + quote(people) + " ORDER BY `id`":    {"1", "1"},
			"SELECT `email_key` FROM " + quote(people) + " ORDER BY `id`": {"ada@example.com", "bob@example.com"},
			"SELECT `address` FROM " + quote(archive) + " ORDER BY `id`":  {"  Ada@Example.COM", "bob@example.com"},
		}

		for query, want := range expect {
			if got := read(t, c, query); !slices.Equal(got, want) {
				t.Errorf("%s returned %q, want %q", query, got, want)
			}
		}

		t.Log("The bool backfill is the case this engine needed type for: 'true' into the")
		t.Log("tinyint(1) a bool is is error 1366, so the literal is written as the keyword TRUE.")
	})
}
