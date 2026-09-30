//go:build integration

package mysql

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// expandAndRun compiles a column change against the database and runs what it became.
func expandAndRun(t *testing.T, c *mysqlConn, op ir.AlterColumn) {
	t.Helper()

	stmts, err := c.Expand(t.Context(), op)
	if err != nil {
		t.Fatalf("expanding %s: %v", op.Kind(), err)
	}

	for _, stmt := range stmts {
		if err := c.Exec(t.Context(), stmt); err != nil {
			t.Fatalf("the expansion produced a statement the server refused:\n%s\n%v", stmt.SQL, err)
		}
	}
}

// definitionOf returns the column's line out of the server's own SHOW CREATE TABLE.
func definitionOf(t *testing.T, c *mysqlConn, table, column string) string {
	t.Helper()

	rows, err := c.runner().QueryContext(t.Context(), "SHOW CREATE TABLE "+quote(table))
	if err != nil {
		t.Fatalf("describing %s: %v", table, err)
	}

	defer rows.Close()

	if !rows.Next() {
		t.Fatalf("describing %s returned no rows", table)
	}

	var name, created string
	if err := rows.Scan(&name, &created); err != nil {
		t.Fatalf("describing %s: %v", table, err)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("describing %s: %v", table, err)
	}

	for line := range strings.SplitSeq(created, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, quote(column)) {
			return strings.TrimSuffix(trimmed, ",")
		}
	}

	t.Fatalf("%s has no column %s:\n%s", table, column, created)

	return ""
}

func TestARetypeKeepsTheNullabilityAndTheCollationItDidNotChange(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_r"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "email", Type: liveType(t, "string(255)")},
		}})

		expandAndRun(t, c, ir.AlterColumn{Table: table, Column: "email",
			Type: typePointerLive(t, "string(512)")})

		got := definitionOf(t, c, table, "email")

		if !strings.Contains(got, "varchar(512)") {
			t.Errorf("the column is %q, and the retype asked for varchar(512)", got)
		}

		if !strings.Contains(got, "NOT NULL") {
			t.Errorf("the column is %q, and it was NOT NULL before the retype", got)
		}

		if kept := collationOf(t, c, table, "email"); kept != collation {
			t.Errorf("the column compares under %q, and it was %q before the retype", kept, collation)
		}

		t.Log("This is the measurement the whole design rests on, from the other side: a MODIFY that")
		t.Log("restated only the type would have left this column nullable and uncollated, because")
		t.Log("MySQL carries forward nothing a redefinition does not say.")
		t.Log("")
		t.Log("The collation is read from the catalog rather than from SHOW CREATE TABLE, because")
		t.Log("MariaDB omits a column's collation from its rendering when it matches the table default")
		t.Log("and MySQL prints it either way. The column is collated on both.")
	})
}

func TestANullabilityChangeKeepsTheTypeTheServerItselfReports(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_n"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "n", Type: liveType(t, "int32")},
			{Name: "e", Type: liveType(t, "enum(red,green)")},
		}})

		expandAndRun(t, c, ir.AlterColumn{Table: table, Column: "n", Nullable: boolPointerLive(true)})
		expandAndRun(t, c, ir.AlterColumn{Table: table, Column: "e", Nullable: boolPointerLive(true)})

		for _, column := range []string{"n", "e"} {
			if got := definitionOf(t, c, table, column); strings.Contains(got, "NOT NULL") {
				t.Errorf("column %s is %q, and it was asked to accept null", column, got)
			}
		}

		if got := definitionOf(t, c, table, "e"); !strings.Contains(got, "'red'") {
			t.Errorf("the enum's labels are gone: %q", got)
		}

		t.Log("The kept type is the server's own COLUMN_TYPE, which is why an enum survives: its labels")
		t.Log("are part of its type, and nothing here re-derives them. MariaDB renders int(11) and")
		t.Log("MySQL int, and restating each server's own text cannot be wrong about it.")
	})
}

func TestADefaultTheOperationSuppliesIsSetInTheFormThatColumnAccepts(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_d"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "s", Type: liveType(t, "string(64)"), Nullable: true},
			{Name: "b", Type: liveType(t, "text"), Nullable: true},
		}})

		for _, column := range []string{"s", "b"} {
			expandAndRun(t, c, ir.AlterColumn{Table: table, Column: column,
				Default: ir.Literal("x")})
		}

		for _, column := range []string{"s", "b"} {
			if got := definitionOf(t, c, table, column); !strings.Contains(got, "DEFAULT") {
				t.Errorf("column %s is %q and was given a default", column, got)
			}
		}

		t.Log("The text column is why setting a default needs the database at all: ALTER COLUMN ... SET")
		t.Log("DEFAULT is error 1101 on a text, blob or json column on MySQL, bare and parenthesised")
		t.Log("alike, so only MODIFY COLUMN can do it -- and whether a literal must be parenthesised is")
		t.Log("a fact about the column that ir.AlterColumn does not carry. Measured on 8.0.46.")
		t.Log("")
		t.Log("A json column belongs to the MariaDB test below rather than here: MariaDB refuses to")
		t.Log("redefine one at all, its json_valid check being part of the column.")
	})
}

func TestADefaultTheOperationDoesNotReplaceStopsTheWholeThing(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_k"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "n", Type: liveType(t, "int32"), Nullable: true, Default: ir.Literal("7")},
		}})

		_, err := c.Expand(t.Context(), ir.AlterColumn{Table: table, Column: "n",
			Type: typePointerLive(t, "int64")})

		if !errors.Is(err, ErrRedefine) {
			t.Fatalf("expanding returned %v, want %v", err, ErrRedefine)
		}

		if !strings.Contains(err.Error(), "Set default") {
			t.Errorf("the refusal does not name the fix: %v", err)
		}

		if got := definitionOf(t, c, table, "n"); !strings.Contains(got, "7") {
			t.Errorf("the column is %q, and nothing should have run", got)
		}

		supplied := ir.AlterColumn{Table: table, Column: "n", Type: typePointerLive(t, "int64"),
			Default: ir.Literal("7")}

		expandAndRun(t, c, supplied)

		got := definitionOf(t, c, table, "n")

		if !strings.Contains(got, "bigint") || !strings.Contains(got, "7") {
			t.Errorf("the column is %q, want a bigint defaulting to 7", got)
		}

		t.Log("Both halves: the refusal leaves the column untouched, and stating the default on the")
		t.Log("operation is what the refusal told the author to do and it works. That is better than a")
		t.Log("guess -- the migration now says what the default should be rather than this driver")
		t.Log("inferring it from a catalog field that does not round trip.")
	})
}

func TestAColumnCarryingSomethingARedefinitionWouldLoseIsRefusedAgainstARealServer(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_x"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		exec(t, c, "CREATE TABLE "+quote(table)+" ("+
			"id int NOT NULL AUTO_INCREMENT PRIMARY KEY, "+
			"t datetime(6) NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6), "+
			"base int NULL, "+
			"v int AS (base + 1), "+
			"legacy varchar(10) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NULL)")

		for _, c2 := range []struct {
			column string
			says   string
		}{
			{column: "id", says: "auto_increment"},
			{column: "t", says: "on update"},
			{column: "v", says: "generated column"},
			{column: "legacy", says: "utf8mb4_general_ci"},
		} {
			t.Run(c2.column, func(t *testing.T) {
				before := definitionOf(t, c, table, c2.column)

				_, err := c.Expand(t.Context(), ir.AlterColumn{Table: table, Column: c2.column,
					Nullable: boolPointerLive(true)})

				if !errors.Is(err, ErrRedefine) {
					t.Fatalf("expanding returned %v, want %v", err, ErrRedefine)
				}

				if !strings.Contains(err.Error(), c2.says) {
					t.Errorf("the refusal does not mention %q: %v", c2.says, err)
				}

				if after := definitionOf(t, c, table, c2.column); after != before {
					t.Errorf("the column changed from %q to %q", before, after)
				}
			})
		}

		t.Log("Four real columns, four refusals, and each one still exactly as it was. Every one of")
		t.Log("these would have been silently dropped by a MODIFY that restated only what the")
		t.Log("operation named -- the auto-increment on a primary key, the ON UPDATE clause, the")
		t.Log("generation expression, and a collation the operator chose and migr did not.")
	})
}

func TestMariaDBsJSONColumnIsRefusedBecauseItsValidationIsPartOfTheColumn(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_j"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "j", Type: liveType(t, "json"), Nullable: true},
		}})

		_, err := c.Expand(t.Context(), ir.AlterColumn{Table: table, Column: "j",
			Nullable: boolPointerLive(false)})

		if s.variant == MySQL {
			if err != nil {
				t.Errorf("MySQL's json column was refused: %v", err)
			}

			t.Log("On MySQL json is a real type with nothing hidden in it, so a redefinition restates")
			t.Log("it and loses nothing. Nothing is refused here.")

			return
		}

		if !errors.Is(err, ErrRedefine) {
			t.Fatalf("MariaDB's json column was accepted: %v", err)
		}

		if !strings.Contains(err.Error(), "json_valid") {
			t.Errorf("the refusal does not say what would be lost: %v", err)
		}

		t.Log("MariaDB implements json as a longtext carrying CHECK (json_valid(`j`)), recorded under")
		t.Log("the column's own name. A MODIFY drops it and the column silently stops validating --")
		t.Log("measured, 'not json' was accepted afterwards. The catalog reports COLUMN_TYPE as")
		t.Log("longtext, so nothing in the type says the check is there; the constraint's name does.")
	})
}

func TestAColumnTheCatalogDoesNotHoldIsReportedAsTheAbsenceItIs(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_m"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
		}})

		_, err := c.Expand(t.Context(), ir.AlterColumn{Table: table, Column: "nope",
			Nullable: boolPointerLive(true)})

		if !errors.Is(err, ErrRedefine) {
			t.Fatalf("expanding returned %v, want %v", err, ErrRedefine)
		}

		if !strings.Contains(err.Error(), "no such column") {
			t.Errorf("the refusal does not say the column is absent: %v", err)
		}

		if _, err := c.Expand(t.Context(), ir.AlterColumn{Table: uniqueName(t) + "_gone",
			Column: "id", Nullable: boolPointerLive(true)}); !errors.Is(err, ErrRedefine) {
			t.Errorf("a missing table returned %v, want %v", err, ErrRedefine)
		}

		t.Log("A clear refusal rather than a MODIFY built from an empty row, which would have been a")
		t.Log("statement naming a type of the empty string.")
	})
}

func TestTheExpansionFoldsATableNameTheWayTheServerStoresIt(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_F"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "n", Type: liveType(t, "int32")},
		}})

		expandAndRun(t, c, ir.AlterColumn{Table: table, Column: "n", Nullable: boolPointerLive(true)})

		t.Log("The catalog lookup asks for the name as this server stores it, which is the same rule the")
		t.Log("history store's Ensure needed. A lookup for the wrong spelling would report the column")
		t.Log("absent and refuse a column that is right there.")
		t.Log("")
		t.Log("This case cannot discriminate and says so rather than implying otherwise. Both containers")
		t.Log("run lower_case_table_names = 0, where storedName is the identity, so an unfolded lookup")
		t.Log("succeeds too -- the mutation that drops the folding survives the whole suite. Showing it")
		t.Log("would need a third server started with --lower-case-table-names=1, and storedName itself")
		t.Log("is held to both settings by an untagged test. What this case does prove is that a")
		t.Log("mixed-case table name works at all.")
	})
}

// typePointerLive returns the address of a logical type, which ir.AlterColumn carries as a pointer.
func typePointerLive(t *testing.T, spelling string) *schema.Type {
	t.Helper()

	typ := liveType(t, spelling)

	return &typ
}

// boolPointerLive returns the address of a nullability, which ir.AlterColumn carries as a pointer.
func boolPointerLive(v bool) *bool {
	return &v
}

// collationOf reads what a column compares under, which is the catalog's answer rather than a rendering.
func collationOf(t *testing.T, c *mysqlConn, table, column string) string {
	t.Helper()

	return scalar(t, c, "SELECT coalesce(COLLATION_NAME, '') FROM information_schema.COLUMNS "+
		"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?",
		storedName(table, c.folding), column)
}

func TestAColumnWhoseOnlyExtraDescribesItsExpressionDefaultIsStillRedefinable(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_g"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "b", Type: liveType(t, "text"), Nullable: true, Default: ir.Literal("x")},
		}})

		if s.variant == MySQL {
			if got := scalar(t, c, "SELECT EXTRA FROM information_schema.COLUMNS "+
				"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'b'",
				storedName(table, c.folding)); got != defaultGenerated {
				t.Fatalf("EXTRA is %q, and this case needs a column MySQL marks %s", got,
					defaultGenerated)
			}
		}

		expandAndRun(t, c, ir.AlterColumn{Table: table, Column: "b",
			Nullable: boolPointerLive(false), Default: ir.Literal("y")})

		got := definitionOf(t, c, table, "b")

		if !strings.Contains(got, "NOT NULL") {
			t.Errorf("the column is %q, and it was asked to require a value", got)
		}

		if !strings.Contains(got, "y") {
			t.Errorf("the column is %q, and it was given a new default", got)
		}

		t.Log("MySQL sets EXTRA to DEFAULT_GENERATED for any column with an expression default, and a")
		t.Log("literal default on a text column is one. A check that refused a non-empty EXTRA outright")
		t.Log("would refuse every such column, so the marker is stripped before the check -- it")
		t.Log("describes the default, which is handled separately. The mutation sweep found this:")
		t.Log("removing the stripping broke nothing until a case reached a column that carries it.")
	})
}
