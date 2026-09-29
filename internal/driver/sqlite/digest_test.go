package sqlite

import (
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/sqlite/sqlitetest"
)

// described renders what one database holds, excluding whatever belongs to one table.
func described(t *testing.T, c *sqliteConn, except string) []string {
	t.Helper()

	rendered, err := sqlitetest.Digest(sqlitetest.Over(t.Context(), c, except))
	if err != nil {
		t.Fatalf("describing the database: %v", err)
	}

	if rendered == "" {
		return nil
	}

	return strings.Split(rendered, "\n")
}

func TestAnObjectSQLiteRecordsNoStatementForStillNamesItself(t *testing.T) {
	c := openFile(t)

	exec(t, c, `CREATE TABLE users (id integer PRIMARY KEY, email text UNIQUE)`)

	lines := described(t, c, "")

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

func TestTheDigestIsOrderedSoTwoRunsOfOneSchemaRenderAlike(t *testing.T) {
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

	lines := described(t, c, "")

	if !slices.IsSorted(lines) {
		t.Errorf("the digest is not in a stable order:\n%v", lines)
		t.Log("Two moments of one schema are compared as text, so an order that depends on the order")
		t.Log("the objects were created in makes a round trip differ for no reason at all.")
	}
}

func TestTheDigestLeavesOutOneTableAndEverythingAttachedToIt(t *testing.T) {
	c := openFile(t)

	for _, statement := range []string{
		`CREATE TABLE keep (id integer PRIMARY KEY)`,
		`CREATE TABLE runs (id integer PRIMARY KEY, version text)`,
		`CREATE INDEX runs_version_idx ON runs (version)`,
		`CREATE TRIGGER runs_trg AFTER INSERT ON runs BEGIN SELECT 1; END`,
	} {
		exec(t, c, statement)
	}

	described := strings.Join(described(t, c, "runs"), "\n")

	for _, gone := range []string{"table runs", "index runs_version_idx", "trigger runs_trg"} {
		if strings.Contains(described, gone) {
			t.Errorf("the digest still holds %q after excluding its table:\n%s", gone, described)
		}
	}

	if !strings.Contains(described, "table keep") {
		t.Errorf("excluding one table removed another:\n%s", described)
	}

	t.Log("tbl_name is what does it in one predicate: an index and a trigger both carry the table")
	t.Log("they belong to, and for a table it is its own name. A round trip needs this, because")
	t.Log("migr up creates the history table and a digest taken before it would differ by it.")
}

func TestExcludingNothingDescribesEverything(t *testing.T) {
	c := openFile(t)

	exec(t, c, `CREATE TABLE users (id integer PRIMARY KEY)`)

	if got := described(t, c, ""); len(got) != 1 {
		t.Errorf("excluding nothing rendered %v, want the one table", got)
	}
}

// compile-time proof that a session is what the digest asks through, and nothing narrower.
var _ driver.Conn = (*sqliteConn)(nil)
