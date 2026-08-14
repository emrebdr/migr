//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

func TestAReadThatWritesIsRefusedByTheServerAndChangesNothing(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			conn := open(t, dsnFor(server.env, server.dsn), driver.Options{})
			table := readTable(t, conn)

			cte := fmt.Sprintf(
				`WITH gone AS (DELETE FROM %s RETURNING *) SELECT count(*) AS n FROM gone`, table)

			_, err := conn.Query(t.Context(), driver.Query{SQL: cte})
			if !errors.Is(err, driver.ErrNotReadOnly) {
				t.Logf("the statement was: %s", cte)
				t.Log("It begins with WITH and deletes every row, so a host-side check on the")
				t.Log("leading keyword would have let it through. The read-only transaction is")
				t.Log("what makes the guarantee a guarantee (ADR 0032, B).")
				t.Fatalf("the driver answered %v, want driver.ErrNotReadOnly", err)
			}

			if rows := count(t, conn, table); rows != "3" {
				t.Errorf("the table holds %s rows, and it held 3 before the read", rows)
				t.Log("A refusal that reports an error after the rows are gone is not a refusal.")
			}
		})
	}
}

func TestAReadReturnsTheServersOwnTextForEveryType(t *testing.T) {
	conn := open(t, dsnFor(servers[0].env, servers[0].dsn), driver.Options{})

	statement := `SELECT 9007199254740993::bigint AS big, NULL::text AS missing, ` +
		`1.10::numeric AS exact, ARRAY[1,2]::int[] AS list, true AS flag`

	rows, err := conn.Query(t.Context(), driver.Query{SQL: statement})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	want := []string{"big", "missing", "exact", "list", "flag"}
	if fmt.Sprint(rows.Columns) != fmt.Sprint(want) {
		t.Errorf("the columns came back as %v, want %v", rows.Columns, want)
	}

	if len(rows.Values) != 1 {
		t.Fatalf("the read returned %d rows", len(rows.Values))
	}

	values := rows.Values[0]
	if len(values) != len(want) {
		t.Fatalf("the row is %d values wide and the columns are %d", len(values), len(want))
	}

	for i, expected := range []string{"9007199254740993", "", "1.10", "{1,2}", "t"} {
		if i == 1 {
			if values[i] != nil {
				t.Errorf("a SQL null came back as %q, and a null is not an empty string", *values[i])
			}

			continue
		}

		if values[i] == nil {
			t.Errorf("%s came back as null", want[i])

			continue
		}

		if *values[i] != expected {
			t.Errorf("%s came back as %q, want %q", want[i], *values[i], expected)
			t.Log("The values are the server's own text output, so a numeric keeps its scale")
			t.Log("and a bigint past 2^53 survives, which a JSON number would not.")
		}
	}
}

func TestABoundParameterCrossesAsTextAndTheServerCastsIt(t *testing.T) {
	conn := open(t, dsnFor(servers[0].env, servers[0].dsn), driver.Options{})

	rows, err := conn.Query(t.Context(), driver.Query{
		SQL:  "SELECT $1::bigint + 1 AS n",
		Args: []string{"9007199254740992"},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	if len(rows.Values) != 1 || rows.Values[0][0] == nil {
		t.Fatalf("the read returned %#v", rows.Values)
	}

	if got := *rows.Values[0][0]; got != "9007199254740993" {
		t.Errorf("the server computed %q", got)
		t.Log("§5 makes every argument a string in the target engine's placeholder style, so")
		t.Log("this proves the driver hands the text over rather than parsing it first.")
	}
}

func TestAReadReturningNoRowsStillNamesItsColumns(t *testing.T) {
	conn := open(t, dsnFor(servers[0].env, servers[0].dsn), driver.Options{})

	rows, err := conn.Query(t.Context(), driver.Query{SQL: "SELECT 1 AS n WHERE false"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	if len(rows.Columns) != 1 || rows.Columns[0] != "n" {
		t.Errorf("the columns came back as %v", rows.Columns)
	}

	if len(rows.Values) != 0 {
		t.Errorf("the read returned %d rows", len(rows.Values))
	}
}

func TestPerformReportsWhatTheStatementChanged(t *testing.T) {
	conn := open(t, dsnFor(servers[0].env, servers[0].dsn), driver.Options{})
	table := readTable(t, conn)

	affected, err := conn.Perform(t.Context(),
		driver.Query{SQL: fmt.Sprintf("UPDATE %s SET note = $1 WHERE id < 3", table), Args: []string{"seen"}})
	if err != nil {
		t.Fatalf("Perform: %v", err)
	}

	if affected != 2 {
		t.Errorf("the statement reported %d rows, want 2", affected)
		t.Log("A migration may branch on rowsAffected, so a fabricated count is a wrong branch.")
	}
}

func TestAMigrationsOwnSQLIsRefusedInsideATransaction(t *testing.T) {
	session := openSession(t, dsnFor(servers[0].env, servers[0].dsn))

	err := session.Transact(t.Context(), func(ctx context.Context) error {
		if _, err := session.Query(ctx, driver.Query{SQL: "SELECT 1"}); !errors.Is(err, ErrQuery) {
			t.Errorf("Query inside a transaction returned %v", err)
		}

		if _, err := session.Perform(ctx, driver.Query{SQL: "SELECT 1"}); !errors.Is(err, ErrPerform) {
			t.Errorf("Perform inside a transaction returned %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
}

// readTable creates a three-row table for a test to read, and drops it when the test ends.
func readTable(t *testing.T, conn driver.Conn) string {
	t.Helper()

	const name = "migr_query_probe"

	perform(t, conn, fmt.Sprintf("DROP TABLE IF EXISTS %s", name))
	perform(t, conn, fmt.Sprintf("CREATE TABLE %s (id bigint PRIMARY KEY, note text)", name))
	perform(t, conn, fmt.Sprintf("INSERT INTO %s (id, note) VALUES (1, 'a'), (2, 'b'), (3, NULL)", name))

	t.Cleanup(func() {
		perform(t, conn, fmt.Sprintf("DROP TABLE IF EXISTS %s", name))
	})

	return name
}

// perform runs one statement through the escape hatch, which is what this file is testing with.
func perform(t *testing.T, conn driver.Conn, sql string) {
	t.Helper()

	if _, err := conn.Perform(context.WithoutCancel(t.Context()), driver.Query{SQL: sql}); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// count reports how many rows a table holds, as the server's own text.
func count(t *testing.T, conn driver.Conn, table string) string {
	t.Helper()

	rows, err := conn.Query(t.Context(), driver.Query{SQL: "SELECT count(*) AS n FROM " + table})
	if err != nil {
		t.Fatalf("counting: %v", err)
	}

	if len(rows.Values) != 1 || rows.Values[0][0] == nil {
		t.Fatalf("counting returned %#v", rows.Values)
	}

	return *rows.Values[0][0]
}
