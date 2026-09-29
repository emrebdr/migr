package sqlitetest

import (
	"context"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
)

// objectQuery renders every object this database holds, which is the whole of what SQLite records.
//
// The replaces flatten a recorded statement onto one line, since a CREATE TABLE spans several and a
// digest compared line by line would otherwise interleave two objects.
//
// coalesce is load-bearing rather than defensive: an autoindex -- what a UNIQUE or a table-level
// PRIMARY KEY creates -- has a null sql, and SQLite's || propagates a null through the whole
// concatenation, so without it that object would render as an empty line and disappear from the
// digest. Autoindexes are deliberately included for that reason: a unique constraint that came or went
// is a schema change. Everything else SQLite names for itself -- sqlite_sequence, sqlite_stat1 -- is
// its own bookkeeping and is excluded.
//
// tbl_name is what excludes a table and everything attached to it in one predicate -- an index and a
// trigger both carry the table they belong to, and for a table it is the table's own name.
//
// ORDER BY is the only ordering, and it is total: no two objects in one schema share a name.
const objectQuery = `
SELECT type || ' ' || name || ' ' ||
       replace(replace(coalesce(sql, '(none)'), char(10), ' '), char(9), ' ')
  FROM sqlite_schema
 WHERE (name NOT LIKE 'sqlite_%' OR name LIKE 'sqlite_autoindex_%') AND tbl_name <> ?
 ORDER BY type, name`

// Ask answers the probe, however its caller is able to reach the database.
type Ask func(probe string) ([]string, error)

// Digest renders everything one database holds as deterministic text, for comparing two moments.
func Digest(ask Ask) (string, error) {
	found, err := ask(objectQuery)
	if err != nil {
		return "", err
	}

	return strings.Join(found, "\n"), nil
}

// Over answers the probe through a driver session, excluding one table and whatever belongs to it.
func Over(ctx context.Context, conn driver.Conn, except string) Ask {
	return func(probe string) ([]string, error) {
		rows, err := conn.Query(ctx, driver.Query{SQL: probe, Args: []string{except}})
		if err != nil {
			return nil, fmt.Errorf("describing the database: %w", err)
		}

		lines := make([]string, 0, len(rows.Values))
		for _, row := range rows.Values {
			lines = append(lines, render(row))
		}

		return lines, nil
	}
}

// render reads the one column the probe selects, since a null there is a probe that changed shape.
func render(row []*string) string {
	if len(row) == 0 || row[0] == nil {
		return ""
	}

	return *row[0]
}
