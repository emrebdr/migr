package sqlite

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

// This is the one comparison in this repository that is still the engine's own text, and M11-T6 kept
// it here on purpose. A rebuild must leave alone everything it was not asked about, and two of those
// things a snapshot cannot hold at all: a trigger, which no operation creates, and a generated
// column, which schema.Unrepresented marks rather than describes. So what a rebuild must not have
// touched is asked for as SQLite recorded it.
//
// It lives inside the driver, where invariant #3 permits the SQL outright, and it is a test helper
// rather than the exported sqlitetest package it used to be. That package existed so cmd/migr could
// ask, since no test above a driver may compose SQL; Conn.Introspect answers that caller neutrally
// as of M11-T2, so the package went at M11-T6 and the probe came in here.
// See docs/adr/0074-the-neutral-round-trip.md.

// objectQuery renders every object this database holds, which is the whole of what SQLite records.
//
// The replaces flatten a recorded statement onto one line, since a CREATE TABLE spans several and a
// digest compared line by line would otherwise interleave two objects.
//
// coalesce is load-bearing rather than defensive: an autoindex -- what a UNIQUE or a table-level
// PRIMARY KEY creates -- has a null sql, and SQLite's || propagates a null through the whole
// concatenation, so without it that object would render as an empty line and disappear from the
// digest. Autoindexes are deliberately included for that reason: a unique constraint that came or
// went is a schema change. Everything else SQLite names for itself -- sqlite_sequence, sqlite_stat1
// -- is its own bookkeeping and is excluded.
//
// ORDER BY is the only ordering, and it is total: no two objects in one schema share a name.
const objectQuery = `
SELECT type || ' ' || name || ' ' ||
       replace(replace(coalesce(sql, '(none)'), char(10), ' '), char(9), ' ')
  FROM sqlite_schema
 WHERE name NOT LIKE 'sqlite_%' OR name LIKE 'sqlite_autoindex_%'
 ORDER BY type, name`

// recordedSQL renders every object this database holds, as the statement SQLite stored for it.
func recordedSQL(t *testing.T, c *sqliteConn) string {
	t.Helper()

	rows, err := c.Query(t.Context(), driver.Query{SQL: objectQuery})
	if err != nil {
		t.Fatalf("describing the database: %v", err)
	}

	lines := make([]string, 0, len(rows.Values))

	for _, row := range rows.Values {
		if len(row) == 0 || row[0] == nil {
			t.Fatalf("the probe answered a null, so it has changed shape")
		}

		lines = append(lines, *row[0])
	}

	return strings.Join(lines, "\n")
}

func TestAnObjectSQLiteRecordsNoStatementForStillNamesItself(t *testing.T) {
	c := openFile(t)

	exec(t, c, `CREATE TABLE users (id integer PRIMARY KEY, email text UNIQUE)`)

	lines := strings.Split(recordedSQL(t, c), "\n")

	var found bool

	for _, line := range lines {
		if line == "" {
			t.Errorf("the digest renders a blank line, so an object is invisible in it:\n%v", lines)
		}

		if strings.HasPrefix(line, "index sqlite_autoindex_users_1 ") {
			found = true

			if !strings.HasSuffix(line, "(none)") {
				t.Errorf("the autoindex renders as %q, and SQLite records no statement for one", line)
			}
		}
	}

	if !found {
		t.Fatalf("the digest does not name the autoindex the inline UNIQUE created:\n%v", lines)
	}

	t.Log("An inline UNIQUE creates sqlite_autoindex_users_1, whose sql is null -- measured. SQLite's")
	t.Log("|| propagates a null through the whole concatenation, so without the coalesce that object")
	t.Log("renders as an empty line and vanishes from the digest. A table-level PRIMARY KEY on an")
	t.Log("integer column makes no autoindex at all: it is still a rowid alias. Also measured.")
}

func TestTheRecordedTextHoldsWhatNoSnapshotCan(t *testing.T) {
	c := openFile(t)

	exec(t, c, `CREATE TABLE users (id integer PRIMARY KEY, email text)`)
	exec(t, c, `CREATE TRIGGER trg_users AFTER INSERT ON users BEGIN SELECT 1; END`)

	rendered := recordedSQL(t, c)

	if !strings.Contains(rendered, "trigger trg_users") {
		t.Fatalf("the recorded text does not name the trigger:\n%s", rendered)
	}

	held, err := c.Introspect(t.Context())
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}

	for _, table := range held.Tables {
		if strings.Contains(strings.ToLower(table.Name), "trg") {
			t.Errorf("a snapshot reported the trigger as a table: %q", table.Name)
		}
	}

	t.Log("This is why one text comparison survives M11-T6. A snapshot holds what the twenty")
	t.Log("operations can create, and no operation creates a trigger -- so a rebuild that dropped")
	t.Log("one would compare equal. Everything else moved onto the snapshot, where a rebuild's own")
	t.Log("re-wording is invisible and the comparison needs no exemption for the rebuilt table.")
}

func TestTheRecordedTextIsOrderedSoTwoMomentsRenderAlike(t *testing.T) {
	c := openFile(t)

	for _, statement := range []string{
		`CREATE TABLE zebra (id integer PRIMARY KEY)`,
		`CREATE TABLE alpha (id integer PRIMARY KEY)`,
		`CREATE INDEX zzz_idx ON alpha (id)`,
		`CREATE INDEX aaa_idx ON zebra (id)`,
		`CREATE VIEW listing AS SELECT id FROM alpha`,
	} {
		exec(t, c, statement)
	}

	lines := strings.Split(recordedSQL(t, c), "\n")

	for i := 1; i < len(lines); i++ {
		if lines[i-1] > lines[i] {
			t.Errorf("the digest is not in a stable order:\n%v", lines)
			t.Log("Two moments of one schema are compared as text, so an order that depends on the")
			t.Log("order the objects were created in makes a round trip differ for no reason at all.")

			break
		}
	}
}

// compile-time proof that a session is what the digest asks through, and nothing narrower.
var _ driver.Conn = (*sqliteConn)(nil)
