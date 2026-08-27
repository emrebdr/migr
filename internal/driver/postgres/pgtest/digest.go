package pgtest

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
)

// relationQuery names the tables and views one schema holds, whatever those relations contain.
//
// Every other probe describes contents, so a table with no columns left would be invisible without
// this one -- and drop_column can reach that state, since PostgreSQL allows a relation with zero
// columns even though ir.CreateTable refuses to declare one.
const relationQuery = `
SELECT c.relkind::text || ' ' || c.relname
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relkind IN ('r', 'v')
 ORDER BY c.relname`

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

// probes are the five questions one digest is built from, in the order it renders them.
var probes = []string{relationQuery, columnQuery, constraintQuery, indexQuery, viewQuery}

// Ask answers one probe for one schema, however its caller is able to reach the server.
type Ask func(probe string) ([]string, error)

// Digest renders everything one schema holds as deterministic text, for comparing two moments.
func Digest(ask Ask) (string, error) {
	var lines []string

	for _, probe := range probes {
		found, err := ask(probe)
		if err != nil {
			return "", err
		}

		slices.Sort(found)
		lines = append(lines, found...)
	}

	return strings.Join(lines, "\n"), nil
}

// Over answers the probes through a driver session, which runs each in a read-only transaction.
func Over(ctx context.Context, conn driver.Conn, schema string) Ask {
	return func(probe string) ([]string, error) {
		rows, err := conn.Query(ctx, driver.Query{SQL: probe, Args: []string{schema}})
		if err != nil {
			return nil, fmt.Errorf("describing schema %q: %w", schema, err)
		}

		lines := make([]string, 0, len(rows.Values))
		for _, row := range rows.Values {
			lines = append(lines, render(row))
		}

		return lines, nil
	}
}

// render reads the one column a probe selects, since a null there is a probe that changed shape.
func render(row []*string) string {
	if len(row) == 0 || row[0] == nil {
		return ""
	}

	return *row[0]
}
