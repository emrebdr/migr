package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

// value returns the one cell a single-row, single-column read came back with.
func value(t *testing.T, rows driver.Rows) string {
	t.Helper()

	if err := rows.Validate(); err != nil {
		t.Fatalf("the result is not the shape its own columns describe: %v", err)
	}

	if len(rows.Values) != 1 || len(rows.Values[0]) != 1 {
		t.Fatalf("the read came back with %d rows", len(rows.Values))
	}

	cell := rows.Values[0][0]
	if cell == nil {
		t.Fatal("the read came back null where a value was expected")
	}

	return *cell
}

// read runs one statement through the read path, failing the test rather than the driver.
func read(t *testing.T, c *sqliteConn, sql string) driver.Rows {
	t.Helper()

	rows, err := c.Query(t.Context(), driver.Query{SQL: sql})
	if err != nil {
		t.Fatalf("Query(%s): %v", sql, err)
	}

	return rows
}

func TestAWriteSentThroughTheReadPathIsRefusedByTheEngine(t *testing.T) {
	statements := map[string]string{
		"a plain write":      `INSERT INTO users (id) VALUES (2)`,
		"a returning delete": `DELETE FROM users RETURNING id`,
		"a writing pragma":   `PRAGMA user_version = 5`,
	}

	for name, statement := range statements {
		t.Run(name, func(t *testing.T) {
			c := openMemory(t)
			exec(t, c, `CREATE TABLE users (id INTEGER PRIMARY KEY)`)
			exec(t, c, `INSERT INTO users (id) VALUES (1)`)

			_, err := c.Query(t.Context(), driver.Query{SQL: statement})
			if !errors.Is(err, driver.ErrNotReadOnly) {
				t.Fatalf("Query(%s) returned %v, want %v", statement, err, driver.ErrNotReadOnly)
			}

			if !errors.Is(err, ErrQuery) {
				t.Errorf("the refusal does not wrap %v: %v", ErrQuery, err)
			}

			if got := value(t, read(t, c, `SELECT count(*) AS n FROM users`)); got != "1" {
				t.Errorf("the refused statement left %s rows, want 1", got)
			}
		})
	}

	t.Log("A writing PRAGMA is the case that decides this. It begins with PRAGMA, so a host-side")
	t.Log("check on the leading keyword passes it, and it writes the database header -- which is")
	t.Log("what the data-modifying CTE is for PostgreSQL, an engine SQLite has no CTE for.")
}

func TestTheSessionCanWriteAgainAfterARead(t *testing.T) {
	c := openMemory(t)
	exec(t, c, `CREATE TABLE users (id INTEGER PRIMARY KEY)`)

	read(t, c, `SELECT 1 AS one`)

	if got := pragma(t, c, pragmaQueryOnly); got != pragmaOff {
		t.Errorf("query_only is %q after a read, want %q", got, pragmaOff)
	}

	exec(t, c, `INSERT INTO users (id) VALUES (1)`)
}

func TestAReadIsRefusedInsideATransaction(t *testing.T) {
	c := openMemory(t)

	err := c.Transact(t.Context(), func(ctx context.Context) error {
		_, err := c.Query(ctx, driver.Query{SQL: `SELECT 1`})

		return err
	})

	if !errors.Is(err, ErrTransactionOpen) {
		t.Fatalf("a read inside a transaction returned %v, want %v", err, ErrTransactionOpen)
	}

	if _, err := c.Perform(t.Context(), driver.Query{SQL: `SELECT 1`}); err != nil {
		t.Errorf("Perform on an idle session: %v", err)
	}
}

func TestEveryStorageClassComesBackAsTheTextTheProtocolCarries(t *testing.T) {
	c := openMemory(t)

	exec(t, c, `CREATE TABLE t (i INTEGER, r REAL, s TEXT, b BLOB, n TEXT)`)
	exec(t, c, `INSERT INTO t VALUES (9007199254740993, 0.1, 'hé', x'48656c6c6f', NULL)`)

	rows := read(t, c, `SELECT i, r, s, b, n FROM t`)

	if err := rows.Validate(); err != nil {
		t.Fatalf("the result is not the shape its own columns describe: %v", err)
	}

	want := []*string{
		text("9007199254740993"),
		text("0.1"),
		text("hé"),
		text(`\x48656c6c6f`),
		nil,
	}

	for i, cell := range rows.Values[0] {
		if !sameText(cell, want[i]) {
			t.Errorf("column %q came back as %s, want %s", rows.Columns[i], describeText(cell),
				describeText(want[i]))
		}
	}

	t.Log("The integer is past 2^53, which is where a JSON number stops being exact and why every")
	t.Log("value crosses the wire as text. The blob renders the way PostgreSQL renders a bytea, so")
	t.Log("one migration reading one column gets the same answer from either engine.")
}

func TestATextValueThatIsNotUTF8IsRefusedNamingItsColumn(t *testing.T) {
	c := openMemory(t)

	exec(t, c, `CREATE TABLE t (s TEXT)`)
	exec(t, c, `INSERT INTO t VALUES (CAST(x'ff01' AS TEXT))`)

	_, err := c.Query(t.Context(), driver.Query{SQL: `SELECT s AS label FROM t`})
	if !errors.Is(err, ErrNotText) {
		t.Fatalf("Query returned %v, want %v", err, ErrNotText)
	}

	if !strings.Contains(err.Error(), `"label"`) {
		t.Errorf("the refusal does not name the column: %v", err)
	}

	t.Log("SQLite permits arbitrary bytes in a TEXT column, and encoding/json would replace each")
	t.Log("invalid one with U+FFFD -- so a migration would read a value that is not in the")
	t.Log("database and nothing would say so. Refusing names the column instead.")
}

func TestPerformReportsWhatItChanged(t *testing.T) {
	c := openMemory(t)

	exec(t, c, `CREATE TABLE users (id INTEGER PRIMARY KEY, tier TEXT)`)
	exec(t, c, `INSERT INTO users (id, tier) VALUES (1, 'free'), (2, 'free'), (3, 'paid')`)

	changed, err := c.Perform(t.Context(), driver.Query{
		SQL:  `UPDATE users SET tier = ? WHERE tier = ?`,
		Args: []string{"trial", "free"},
	})
	if err != nil {
		t.Fatalf("Perform: %v", err)
	}

	if changed != 2 {
		t.Errorf("Perform reported %d rows changed, want 2", changed)
	}
}

func TestABoundArgumentIsSentRatherThanInterpolated(t *testing.T) {
	c := openMemory(t)

	exec(t, c, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`)

	if _, err := c.Perform(t.Context(), driver.Query{
		SQL:  `INSERT INTO users (id, name) VALUES (1, ?)`,
		Args: []string{"'); DROP TABLE users; --"},
	}); err != nil {
		t.Fatalf("Perform: %v", err)
	}

	if got := value(t, read(t, c, `SELECT name FROM users WHERE id = 1`)); !strings.Contains(got,
		"DROP TABLE") {
		t.Errorf("the argument came back as %q", got)
	}

	if got := value(t, read(t, c, `SELECT count(*) AS n FROM users`)); got != "1" {
		t.Errorf("the table holds %s rows, want 1", got)
	}
}

func TestAnIntegerColumnMatchesATextArgument(t *testing.T) {
	c := openMemory(t)

	exec(t, c, `CREATE TABLE users (id INTEGER PRIMARY KEY)`)
	exec(t, c, `INSERT INTO users (id) VALUES (7)`)

	rows := read(t, c, `SELECT count(*) AS n FROM users WHERE id = 7`)
	if got := value(t, rows); got != "1" {
		t.Fatalf("the unbound comparison found %s rows", got)
	}

	bound, err := c.Query(t.Context(), driver.Query{
		SQL:  `SELECT count(*) AS n FROM users WHERE id = ?`,
		Args: []string{"7"},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	if got := value(t, bound); got != "1" {
		t.Errorf("a text argument matched %s rows of an INTEGER column, want 1", got)
		t.Log("Every argument crosses the wire as text (docs/protocol.md §5), and SQLite is")
		t.Log("dynamically typed -- so this only works because column affinity converts the")
		t.Log("operand. A migration comparing against an expression rather than a column does not")
		t.Log("get that conversion, which is stated in doc.go rather than left to be discovered.")
	}
}

// sameText reports whether two rendered values are the same, treating null as a value.
func sameText(got, want *string) bool {
	if got == nil || want == nil {
		return got == want
	}

	return *got == *want
}

// describeText renders a value for a failure message, saying null where there was none.
func describeText(value *string) string {
	if value == nil {
		return "null"
	}

	return `"` + *value + `"`
}
