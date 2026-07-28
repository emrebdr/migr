//go:build integration

package postgres

import (
	"slices"
	"strings"
	"testing"
)

// columnQuery renders every column of every table in one schema, with its type and its default.
//
// pg_get_expr renders a default as text rather than as the node tree it is stored as, so no oid
// reaches the digest and two runs of the same schema render identically. attisdropped columns are
// excluded because PostgreSQL keeps a dropped column in pg_attribute under a generated name.
const columnQuery = `
SELECT c.relname || '.' || a.attname
    || ' ' || format_type(a.atttypid, a.atttypmod)
    || CASE WHEN a.attnotnull THEN ' not null' ELSE '' END
    || coalesce(' default ' || pg_get_expr(d.adbin, d.adrelid), '')
  FROM pg_attribute a
  JOIN pg_class c ON c.oid = a.attrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
  LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
 WHERE n.nspname = $1 AND a.attnum > 0 AND NOT a.attisdropped AND c.relkind IN ('r', 'v')
 ORDER BY c.relname, a.attnum`

// constraintQuery renders every constraint in one schema through PostgreSQL's own printer.
const constraintQuery = `
SELECT c.conname || ' ON ' || t.relname || ' ' || pg_get_constraintdef(c.oid)
  FROM pg_constraint c
  JOIN pg_class t ON t.oid = c.conrelid
  JOIN pg_namespace n ON n.oid = c.connamespace
 WHERE n.nspname = $1
 ORDER BY t.relname, c.conname`

// indexQuery renders every index in one schema, schema-qualified names and all.
const indexQuery = `
SELECT indexname || ' ' || indexdef
  FROM pg_indexes
 WHERE schemaname = $1
 ORDER BY indexname`

// viewQuery renders every view definition in one schema.
const viewQuery = `
SELECT viewname || ' ' || definition
  FROM pg_views
 WHERE schemaname = $1
 ORDER BY viewname`

// relationQuery lists the tables and views one schema holds, so an empty schema still renders.
const relationQuery = `
SELECT c.relkind::text || ' ' || c.relname
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relkind IN ('r', 'v')
 ORDER BY c.relname`

// snapshotSupersededBy names the task that replaces this probe with the comparison M3-T5 asked for.
//
// M3-T5's done-when says "verified by introspection snapshot comparison", and no such thing exists
// to verify with: internal/schema's Snapshot is M11-T1 and per-driver introspection is M11-T2, both
// eight milestones ahead and both depending on M3-T1 rather than the other way round. Building
// either early would be doing another milestone's work under this task's name.
//
// So the comparison is a test-local catalog digest, the same shape M3-T3's relfilenode probe took:
// a question asked in the driver's own dialect, which invariant #3 permits inside this package and
// nowhere above it. Everything it renders comes from PostgreSQL's own printers -- pg_get_expr,
// pg_get_indexdef, pg_get_constraintdef -- so no oid or relfilenode reaches the text and two runs
// of one schema render identically.
const snapshotSupersededBy = "M11-T6"

// describeSchema renders everything one schema holds as deterministic text, for comparison.
func describeSchema(t *testing.T, session *pgConn, schema string) string {
	t.Helper()

	var lines []string

	for _, query := range []string{relationQuery, columnQuery, constraintQuery, indexQuery, viewQuery} {
		lines = append(lines, queryLines(t, session, query, schema)...)
	}

	return strings.Join(lines, "\n")
}

// queryLines runs one probe and returns its rows, which the query itself has already ordered.
func queryLines(t *testing.T, session *pgConn, query, schema string) []string {
	t.Helper()

	rows, err := session.conn.Query(t.Context(), query, schema)
	if err != nil {
		t.Fatalf("describing schema %q: %v", schema, err)
	}
	defer rows.Close()

	var lines []string

	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("describing schema %q: %v", schema, err)
		}

		lines = append(lines, line)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("describing schema %q: %v", schema, err)
	}

	slices.Sort(lines)

	return lines
}
