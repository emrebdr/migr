//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver/postgres/pgtest"
	"github.com/emrebdr/migr/internal/ir"
)

// snapshotSupersededBy names the task that replaces this probe with the comparison M3-T5 asked for.
//
// M3-T5's done-when says "verified by introspection snapshot comparison", and no such thing exists
// to verify with: internal/schema's Snapshot is M11-T1 and per-driver introspection is M11-T2, both
// eight milestones ahead and both depending on M3-T1 rather than the other way round. Building
// either early would be doing another milestone's work under this task's name.
//
// So the comparison is a catalog digest, the same shape M3-T3's relfilenode probe took: a question
// asked in the driver's own dialect, which invariant #3 permits inside this subtree and nowhere
// above it. It lives in pgtest because M6-T10 needed to ask it from cmd/migr, the one package that
// may drive the whole stack -- see that package's doc for why that is the only home it can have.
const snapshotSupersededBy = "M11-T6"

// loneTable is a table whose only column drop_column takes away, leaving the relation behind.
const loneTable = "migr_probe_lone"

func TestTheDigestTellsAnEmptiedTableFromADroppedOne(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)
			session := sessionIn(t, dsn, target)

			only := ir.Column{Name: "id", Type: probeType(t, "int64")}

			apply(t, session, ir.Operations{
				ir.CreateTable{Table: loneTable, Columns: []ir.Column{only}},
				ir.DropColumn{Table: loneTable, Column: only.Name, From: &only},
			})

			if digest := describeSchema(t, session, target); !strings.Contains(digest, loneTable) {
				t.Errorf("a table with no columns left renders as nothing:\n%q", digest)
				t.Log("Every probe but relationQuery describes contents, so an emptied table would")
				t.Log("be indistinguishable from one that was dropped -- and a rollback that dropped")
				t.Log("a table it was meant to empty would compare equal to one that did neither.")
			}
		})
	}
}

// describeSchema renders one schema as text, asking over this package's own connection.
func describeSchema(t *testing.T, session *pgConn, schema string) string {
	t.Helper()

	digest, err := pgtest.Digest(func(probe string) ([]string, error) {
		return queryLines(session, probe, schema)
	})
	if err != nil {
		t.Fatalf("describing schema %q: %v", schema, err)
	}

	return digest
}

// queryLines runs one probe and returns its rows, which the digest orders for itself.
func queryLines(session *pgConn, probe, schema string) ([]string, error) {
	rows, err := session.conn.Query(context.Background(), probe, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lines []string

	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}

		lines = append(lines, line)
	}

	return lines, rows.Err()
}
