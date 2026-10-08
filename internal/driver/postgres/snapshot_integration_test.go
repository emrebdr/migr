//go:build integration

package postgres

import (
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// M11-T6 replaced M3-T5's stand-in with the comparison that task's done-when actually asked for.
//
// M3-T5 said "verified by introspection snapshot comparison" and there was no such thing to verify
// with: internal/schema's Snapshot was M11-T1 and per-driver introspection M11-T2, both eight
// milestones ahead. So the comparison was a catalog digest in pgtest -- five probes asking
// PostgreSQL to describe itself, reassembled into sorted text.
//
// Conn.Introspect is now on the contract, so the question has a neutral answer and the digest is
// the second one. What describeSchema renders is a snapshot, which is what the suites above
// compare; nothing here composes SQL any more, and the Ask seam pgtest needed went with it, since
// probe already reads through this session's open transaction when it has one.

// describeSchema renders what this session's namespace holds, as the snapshot the drivers agree on.
func describeSchema(t *testing.T, session *pgConn) string {
	t.Helper()

	held, err := session.Introspect(t.Context())
	if err != nil {
		t.Fatalf("introspecting the schema: %v", err)
	}

	return schema.Render(*held)
}

// loneTable is a table whose only column drop_column takes away, leaving the relation behind.
const loneTable = "migr_probe_lone"

func TestASnapshotTellsAnEmptiedTableFromADroppedOne(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)
			session := sessionIn(t, dsn, target)

			only := ir.Column{Name: "id", Type: probeType(t, "int64")}

			apply(t, session, ir.Operations{
				ir.CreateTable{Table: loneTable, Columns: []ir.Column{only}},
			})

			built, err := session.Introspect(t.Context())
			if err != nil {
				t.Fatalf("introspecting the built schema: %v", err)
			}

			apply(t, session, ir.Operations{
				ir.DropColumn{Table: loneTable, Column: only.Name, From: &only},
			})

			emptied, err := session.Introspect(t.Context())
			if err != nil {
				t.Fatalf("introspecting the emptied schema: %v", err)
			}

			held, there := emptied.Table(loneTable)
			if !there {
				t.Fatalf("a table with no columns left is not in the snapshot:\n%s",
					schema.Render(*emptied))
			}

			if len(held.Columns) != 0 {
				t.Errorf("the emptied table still reports %d columns", len(held.Columns))
			}

			if emptied.Equal(*built) {
				t.Error("emptying the table compares equal to leaving it alone")
			}

			apply(t, session, ir.Operations{ir.DropTable{Table: loneTable}})

			dropped, err := session.Introspect(t.Context())
			if err != nil {
				t.Fatalf("introspecting the dropped schema: %v", err)
			}

			if dropped.Equal(*emptied) {
				t.Error("dropping the table compares equal to emptying it")
				t.Log("Every other line of a rendering describes contents, and PostgreSQL holds a")
				t.Log("relation with no columns -- so without the table's own entry a rollback that")
				t.Log("dropped a table it was meant to empty would compare equal to a correct one.")
			}
		})
	}
}
