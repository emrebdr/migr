//go:build integration

package mysql

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/history"
)

// indexesOn names the indexes a table carries, which is how a half-built installation is visible.
func indexesOn(t *testing.T, c *mysqlConn, table string) []string {
	t.Helper()

	names := read(t, c, "SELECT DISTINCT INDEX_NAME FROM information_schema.STATISTICS "+
		"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY INDEX_NAME", table)

	slices.Sort(names)

	return names
}

func TestEnsureCreatesTheTableAndTheIndexItsLookupNeeds(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)

		if got := existing(t, c, held.table); got != "1" {
			t.Fatalf("Ensure left %s tables of that name", got)
		}

		want := []string{"PRIMARY", held.table + "_version_idx"}
		slices.Sort(want)

		if got := indexesOn(t, c, held.table); !slices.Equal(got, want) {
			t.Errorf("Ensure left the indexes %v, want %v", got, want)
		}
	})
}

func TestEnsureIsIdempotent(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)

		if err := held.Ensure(t.Context()); err != nil {
			t.Fatalf("the second Ensure: %v", err)
		}

		if err := held.Record(t.Context(), run(t, 1, 1)); err != nil {
			t.Fatalf("Record after two Ensures: %v", err)
		}
	})
}

func TestEnsureFinishesAnInstallationThatOnlyTheTableSurvived(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := store(t, c)

		stmts, err := held.compile(history.Schema(held.table))
		if err != nil {
			t.Fatalf("compiling the history schema: %v", err)
		}

		if len(stmts) < 2 {
			t.Fatalf("the schema is %d statements, so there is no half to interrupt", len(stmts))
		}

		if err := c.Exec(t.Context(), stmts[0]); err != nil {
			t.Fatalf("running the CREATE TABLE alone: %v", err)
		}

		if got := indexesOn(t, c, held.table); slices.Contains(got, held.table+"_version_idx") {
			t.Fatalf("the half-built table already carries its index: %v", got)
		}

		if err := held.Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure over a half-built installation: %v", err)
		}

		if got := indexesOn(t, c, held.table); !slices.Contains(got, held.table+"_version_idx") {
			t.Errorf("Ensure did not finish the installation: %v", got)
		}

		if err := held.Record(t.Context(), run(t, 1, 1)); err != nil {
			t.Errorf("Record after the installation was finished: %v", err)
		}

		t.Log("MySQL commits on every DDL statement, so a run interrupted between CREATE TABLE and")
		t.Log("CREATE INDEX leaves exactly this and nothing can roll it back. SQLite's Ensure runs")
		t.Log("its statements as one unit and a test there proves the table is gone; here the only")
		t.Log("honest answer is that Ensure is re-entrant, and this is that answer being kept.")
	})
}

func TestAHistoryNameHeldBySomethingOtherThanATableIsRefused(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := store(t, c)

		exec(t, c, "CREATE VIEW "+quote(held.table)+" AS SELECT 1 AS batch")

		if err := held.Ensure(t.Context()); !errors.Is(err, ErrHistoryNotATable) {
			t.Fatalf("Ensure returned %v, want %v", err, ErrHistoryNotATable)
		}

		if _, err := held.List(t.Context()); !errors.Is(err, ErrHistoryNotATable) {
			t.Errorf("List over a view returned %v, want %v", err, ErrHistoryNotATable)
		}

		if _, _, err := held.Latest(t.Context()); !errors.Is(err, ErrHistoryNotATable) {
			t.Errorf("Latest over a view returned %v, want %v", err, ErrHistoryNotATable)
		}

		if err := held.Record(t.Context(), run(t, 1, 1)); errors.Is(err, ErrAlreadyRecorded) {
			t.Errorf("Record over a view reported a duplicate run: %v", err)
		}

		t.Log("A view is selectable and no error number says it is one, which is why the catalog is")
		t.Log("asked on every read even though MySQL numbers the other causes apart. Absence needs")
		t.Log("no lookup: every statement that reaches a missing table fails 1146 and is classified")
		t.Log("there, so this refuses only what a statement would answer from.")
	})
}

func TestATableMissingAColumnThisBuildWritesIsNamedRatherThanWrittenTo(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := store(t, c)

		exec(t, c, "CREATE TABLE "+quote(held.table)+" (batch bigint, ordinal int)")

		err := held.Ensure(t.Context())
		if !errors.Is(err, ErrHistoryShape) {
			t.Fatalf("Ensure returned %v, want %v", err, ErrHistoryShape)
		}

		for _, column := range []string{"version", "checksum", "applied_at", "ir_version"} {
			if !strings.Contains(err.Error(), column) {
				t.Errorf("the refusal does not name the missing column %q: %v", column, err)
			}
		}
	})
}

func TestAnExtraColumnIsFineAndAGeneratedOneIsNot(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)

		exec(t, c, "ALTER TABLE "+quote(held.table)+" ADD COLUMN `note` varchar(8) NULL")

		if err := held.Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure refused a table with an extra column: %v", err)
		}

		exec(t, c, "ALTER TABLE "+quote(held.table)+" DROP COLUMN `ir_version`")
		exec(t, c, "ALTER TABLE "+quote(held.table)+
			" ADD COLUMN `ir_version` int GENERATED ALWAYS AS (1)")

		err := held.Ensure(t.Context())
		if !errors.Is(err, ErrHistoryShape) {
			t.Fatalf("Ensure accepted a generated column: %v", err)
		}

		if !strings.Contains(err.Error(), "ir_version") {
			t.Errorf("the refusal does not name the generated column: %v", err)
		}

		t.Log("A generated column is one nothing can INSERT into, so it must count as absent. EXTRA")
		t.Log("cannot say so: MySQL 8 sets it to DEFAULT_GENERATED for any column with an expression")
		t.Log("default, so filtering EXTRA on GENERATED would call five writable columns missing.")
		t.Log("GENERATION_EXPRESSION is the field that means it -- empty on MySQL and null on")
		t.Log("MariaDB for an ordinary column. Measured on both.")
	})
}

func TestTheHistoryTableIsFoundHoweverThisServerFoldsItsName(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := store(t, c)

		if err := held.Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		folding := scalar(t, c, caseQuery)

		if got := storedName(held.table, atoiOrZero(folding)); got != held.stored {
			t.Errorf("the catalog is asked for %q and this server stores %q", held.stored, got)
		}

		if err := held.requireTable(t.Context()); err != nil {
			t.Errorf("the table this session just created was not found: %v", err)
		}

		t.Logf("lower_case_table_names is %s here, and asking the catalog for the wrong spelling "+
			"means Ensure reports the table absent and tries to create it on every run, forever.",
			folding)
	})
}

// atoiOrZero reads a small number a server reported, treating anything unreadable as zero.
func atoiOrZero(s string) int {
	var out int

	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}

		out = out*10 + int(r-'0')
	}

	return out
}
