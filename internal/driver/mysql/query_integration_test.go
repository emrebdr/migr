//go:build integration

package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

// seeded creates a small table a read can be aimed at, and removes it when the test ends.
func seeded(t *testing.T, c *mysqlConn) string {
	t.Helper()

	table := uniqueName(t) + "_rows"

	dropLater(t, c, table)
	drop(t.Context(), c, table)
	exec(t, c, "CREATE TABLE "+quote(table)+" (id int PRIMARY KEY, n int)")
	exec(t, c, "INSERT INTO "+quote(table)+" VALUES (1, 1), (2, 1)")

	return table
}

func TestAReadThatWritesIsRefusedByTheServerRatherThanByAKeywordCheck(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := seeded(t, c)

		dropLater(t, c, table+"_ddl")

		for name, sql := range map[string]string{
			"SELECT ... FOR UPDATE": "SELECT n FROM " + quote(table) + " WHERE id = 1 FOR UPDATE",
			"an INSERT":             "INSERT INTO " + quote(table) + " VALUES (9, 9)",
			"an UPDATE":             "UPDATE " + quote(table) + " SET n = 9",
			"a DELETE":              "DELETE FROM " + quote(table),
			"a CREATE TABLE":        "CREATE TABLE " + quote(table+"_ddl") + " (a int)",
			"a TRUNCATE":            "TRUNCATE TABLE " + quote(table),
		} {
			t.Run(name, func(t *testing.T) {
				_, err := c.Query(t.Context(), driver.Query{SQL: sql})
				if !errors.Is(err, driver.ErrNotReadOnly) {
					t.Fatalf("Query returned %v, want %v", err, driver.ErrNotReadOnly)
				}
			})
		}

		if got := scalar(t, c, "SELECT count(*) FROM "+quote(table)); got != "2" {
			t.Errorf("the table holds %s rows, so one of those reads wrote", got)
		}

		t.Log("SELECT ... FOR UPDATE is this engine's answer to ADR 0032 B. MySQL has no")
		t.Log("data-modifying CTE, so PostgreSQL's fixture does not port -- but FOR UPDATE begins")
		t.Log("with SELECT, passes any check on the leading keyword, and takes exclusive row locks")
		t.Log("that block every writer on those rows.")
		t.Log("")
		t.Log("The DDL cases are why the session variable is set as well as the transaction begun.")
		t.Log("START TRANSACTION READ ONLY alone lets CREATE TABLE, ALTER TABLE, CREATE INDEX,")
		t.Log("TRUNCATE and DROP TABLE straight through -- each one implicitly commits, which ends")
		t.Log("the read-only transaction before the server checks it, and the change survives the")
		t.Log("rollback. Measured on both servers. transaction_read_only closes it.")
	})
}

func TestAReadIsStillARead(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := seeded(t, c)

		rows, err := c.Query(t.Context(), driver.Query{
			SQL:  "SELECT id, n FROM " + quote(table) + " WHERE id = ? ORDER BY id",
			Args: []string{"1"},
		})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}

		if err := rows.Validate(); err != nil {
			t.Fatalf("the result is not the shape its own columns describe: %v", err)
		}

		if len(rows.Values) != 1 || *rows.Values[0][0] != "1" {
			t.Fatalf("Query returned %v", rows.Values)
		}

		t.Log("A text argument compared against an int column still matches, because MySQL converts")
		t.Log("the operand. Measured, and it is what lets every argument cross the wire as text.")
	})
}

func TestTheSessionTakesWritesAgainAfterAReadAndAfterAFailedOne(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := seeded(t, c)

		if _, err := c.Query(t.Context(), driver.Query{SQL: "SELECT 1"}); err != nil {
			t.Fatalf("Query: %v", err)
		}

		if _, err := c.Query(t.Context(), driver.Query{
			SQL: "SELECT n FROM " + quote(table) + " FOR UPDATE",
		}); err == nil {
			t.Fatal("a read that writes was allowed")
		}

		if got := scalar(t, c, "SELECT @@session."+varReadOnly); got != variableOff {
			t.Fatalf("%s is %q after a failed read, and the session would refuse every write",
				varReadOnly, got)
		}

		changed, err := c.Perform(t.Context(), driver.Query{
			SQL: "UPDATE " + quote(table) + " SET n = 2 WHERE id = ?", Args: []string{"1"},
		})
		if err != nil {
			t.Fatalf("Perform after a read: %v", err)
		}

		if changed != 1 {
			t.Errorf("Perform changed %d rows, want 1", changed)
		}
	})
}

func TestPerformReportsTheServersOwnCountOfWhatItChanged(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := seeded(t, c)

		noop, err := c.Perform(t.Context(), driver.Query{SQL: "UPDATE " + quote(table) + " SET n = 1"})
		if err != nil {
			t.Fatalf("Perform: %v", err)
		}

		if noop != 0 {
			t.Errorf("an update that changed nothing reports %d rows", noop)
		}

		changed, err := c.Perform(t.Context(),
			driver.Query{SQL: "UPDATE " + quote(table) + " SET n = 7"})
		if err != nil {
			t.Fatalf("Perform: %v", err)
		}

		if changed != 2 {
			t.Errorf("an update that changed two rows reports %d", changed)
		}

		t.Log("MySQL counts rows changed where PostgreSQL's command tag counts rows matched, so the")
		t.Log("same no-op UPDATE is 0 here and 1 there. clientFoundRows would make them agree and is")
		t.Log("refused in the connection string instead: every value a read returns is the server's")
		t.Log("own, and this number is one of them.")
	})
}

func TestEveryValueComesBackAsTheServersOwnText(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_values"

		dropLater(t, c, table)
		exec(t, c, "DROP TABLE IF EXISTS "+quote(table))
		exec(t, c, "CREATE TABLE "+quote(table)+" ("+
			"i bigint, u bigint unsigned, d decimal(12,4), f double, "+
			"s varchar(16), b blob, j json, at datetime(6), nothing int)")
		exec(t, c, "INSERT INTO "+quote(table)+" VALUES ("+
			"9007199254740993, 18446744073709551615, 12.3400, 1.5, "+
			"'hello', 0x48656C6C6F, '{\"a\":1}', '2026-09-29 11:22:33.123456', NULL)")

		rows, err := c.Query(t.Context(), driver.Query{SQL: "SELECT * FROM " + quote(table)})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}

		want := map[string]string{
			"i":  "9007199254740993",
			"u":  "18446744073709551615",
			"d":  "12.3400",
			"f":  "1.5",
			"s":  "hello",
			"b":  `\x48656c6c6f`,
			"at": "2026-09-29 11:22:33.123456",
		}

		for i, column := range rows.Columns {
			value := rows.Values[0][i]

			if column == "nothing" {
				if value != nil {
					t.Errorf("a SQL null came back as %q rather than as nothing at all", *value)
				}

				continue
			}

			if column == "j" {
				if !strings.Contains(*value, `"a"`) {
					t.Errorf("the json column reads back as %q", *value)
				}

				continue
			}

			if *value != want[column] {
				t.Errorf("%s reads back as %q, want %q", column, *value, want[column])
			}
		}

		t.Log("A bigint past 2^53 survives because nothing turns it into a float, and an unsigned")
		t.Log("bigint past 2^63 survives because go-sql-driver hands it over as a uint64.")
		t.Log("")
		t.Log("Bytes render as PostgreSQL's bytea does, so one migration reading one column gets the")
		t.Log("same answer from either engine -- and the only thing that can say a column is bytes is")
		t.Log("the column: MySQL sends text and bytes as the same Go type, where SQLite sends a")
		t.Log("storage class per value. Measured across twenty column types.")
	})
}

func TestWhichSideOfTheRenderingACharacterSetPutsAColumnOn(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_bytes"

		dropLater(t, c, table)
		exec(t, c, "DROP TABLE IF EXISTS "+quote(table))
		exec(t, c, "CREATE TABLE "+quote(table)+
			" (b varbinary(8), l varchar(8) CHARACTER SET latin1)")
		exec(t, c, "INSERT INTO "+quote(table)+" VALUES (0x00FF, 0xFF)")

		rows, err := c.Query(t.Context(), driver.Query{
			SQL: "SELECT b, l FROM " + quote(table),
		})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}

		if got := *rows.Values[0][0]; got != `\x00ff` {
			t.Errorf("a varbinary reads back as %q, want hex", got)
		}

		if got := *rows.Values[0][1]; got != "ÿ" {
			t.Errorf("a latin1 column reads back as %q", got)
		}

		t.Log("Measured, and it is what makes the UTF-8 check a forward-compatibility guard rather")
		t.Log("than a routine one: the server transcodes every text column to the connection's")
		t.Log("utf8mb4 on the way out, so a latin1 0xFF arrives as the two bytes of U+00FF and an")
		t.Log("invalid byte cast to CHAR is replaced by the server itself -- dropped on 8.0.46 and")
		t.Log("substituted with '?' on 11.4.13. Bytes reach this driver unconverted only through a")
		t.Log("column whose character set is the binary one, which the library reports as VARBINARY")
		t.Log("and which goes down the hex path. What is left for checkUTF8 is a field type this")
		t.Log("build of the library cannot name, which query_test.go drives directly.")
	})
}

func TestAMigrationsOwnSQLIsRefusedInsideATransaction(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		err := c.Transact(t.Context(), func(ctx context.Context) error {
			if _, err := c.Query(ctx, driver.Query{SQL: "SELECT 1"}); !errors.Is(err, ErrQuery) {
				t.Errorf("Query inside a transaction returned %v", err)
			}

			if _, err := c.Perform(ctx, driver.Query{SQL: "SELECT 1"}); !errors.Is(err, ErrPerform) {
				t.Errorf("Perform inside a transaction returned %v", err)
			}

			return nil
		})
		if err != nil {
			t.Fatalf("Transact: %v", err)
		}
	})
}
