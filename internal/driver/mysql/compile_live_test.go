//go:build integration

package mysql

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// apply compiles an operation and sends every statement it became to the server.
func apply(t *testing.T, c *mysqlConn, op ir.Operation) {
	t.Helper()

	stmts, err := compile(op)
	if err != nil {
		t.Fatalf("compiling %s: %v", op.Kind(), err)
	}

	for _, stmt := range stmts {
		if err := c.Exec(t.Context(), stmt); err != nil {
			t.Fatalf("%s compiled to a statement the server refused:\n%s\n%v", op.Kind(), stmt.SQL, err)
		}
	}
}

// liveType parses a logical type, which these cases name by their canonical spelling.
func liveType(t *testing.T, spelling string) schema.Type {
	t.Helper()

	typ, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("parsing %q: %v", spelling, err)
	}

	return typ
}

func TestEveryCompiledStatementIsAcceptedByTheServer(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		users, orders, legacy := uniqueName(t)+"_u", uniqueName(t)+"_o", uniqueName(t)+"_l"
		view := uniqueName(t) + "_v"

		dropLater(t, c, orders, users, legacy, view)
		drop(t.Context(), c, orders, users, legacy, view)

		apply(t, c, ir.CreateTable{Table: users, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "uuid")},
			{Name: "email", Type: liveType(t, "string(255)")},
			{Name: "created_at", Type: liveType(t, "timestamptz"), Nullable: true,
				Default: ir.Call(ir.FuncCurrentTimestamp)},
		}, PrimaryKey: []string{"id"}})

		apply(t, c, ir.AddColumn{Table: users, Column: ir.Column{Name: "nickname",
			Type: liveType(t, "string(64)"), Nullable: true, Default: ir.Literal("anonymous")}})
		apply(t, c, ir.AddIndex{Table: users, Name: users + "_i", Columns: []string{"email"}})
		apply(t, c, ir.DropIndex{Table: users, Name: users + "_i"})
		apply(t, c, ir.AddUnique{Table: users, Name: users + "_q", Columns: []string{"email"}})
		apply(t, c, ir.DropUnique{Table: users, Name: users + "_q"})
		apply(t, c, ir.RenameColumn{Table: users, From: "email", To: "email_address"})
		apply(t, c, ir.RenameColumn{Table: users, From: "email_address", To: "email"})
		apply(t, c, ir.AlterColumn{Table: users, Column: "nickname", DropDefault: true})
		apply(t, c, ir.DropColumn{Table: users, Column: "nickname"})

		apply(t, c, ir.CreateTable{Table: orders, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
			{Name: "user_id", Type: liveType(t, "uuid")},
			{Name: "total", Type: liveType(t, "int32")},
		}, PrimaryKey: []string{"id"}})

		apply(t, c, ir.AddForeignKey{Table: orders, Name: orders + "_f",
			Columns: []string{"user_id"}, ReferencesTable: users, ReferencesColumns: []string{"id"},
			OnDelete: ir.ActionCascade, OnUpdate: ir.ActionRestrict})
		apply(t, c, ir.DropForeignKey{Table: orders, Name: orders + "_f"})
		apply(t, c, ir.AddCheck{Table: orders, Name: orders + "_c", Expression: "total >= 0"})
		apply(t, c, ir.DropCheck{Table: orders, Name: orders + "_c"})

		apply(t, c, ir.CreateTable{Table: legacy, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
		}})
		apply(t, c, ir.AddPrimaryKey{Table: legacy, Name: legacy + "_pk", Columns: []string{"id"}})
		apply(t, c, ir.DropPrimaryKey{Table: legacy, Name: legacy + "_pk"})

		apply(t, c, ir.CreateView{View: view, Query: "SELECT id FROM " + quote(users)})
		apply(t, c, ir.CreateView{View: view, Query: "SELECT id FROM " + quote(users), OrReplace: true})
		apply(t, c, ir.DropView{View: view})

		apply(t, c, ir.RenameTable{From: legacy, To: legacy + "x"})
		apply(t, c, ir.RenameTable{From: legacy + "x", To: legacy})

		apply(t, c, ir.Raw{Driver: Name, SQL: "ANALYZE TABLE " + quote(users)})

		apply(t, c, ir.DropTable{Table: orders})
		apply(t, c, ir.DropTable{Table: users})
		apply(t, c, ir.DropTable{Table: legacy})

		t.Log("Nineteen operations, compiled and run in dependency order. This is M8-T2's precedent: a")
		t.Log("golden file the engine accepted is worth more than one that is merely the right shape,")
		t.Log("and four of these spellings are MySQL's own rather than the other drivers'.")
	})
}

func TestEveryLogicalTypeIsAcceptedInOneTable(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_t"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		spellings := []string{
			"string(10)", "text", "bool", "int16", "int32", "int64", "float32", "float64",
			"decimal(12,4)", "uuid", "json", "timestamp", "timestamptz", "date", "time", "binary",
			"enum(red,green)",
		}

		columns := make([]ir.Column, len(spellings))
		for i, spelling := range spellings {
			columns[i] = ir.Column{Name: "c" + strings.Map(keepAlphanumeric, spelling),
				Type: liveType(t, spelling)}
		}

		apply(t, c, ir.CreateTable{Table: table, Columns: columns})

		declared := read(t, c, "SELECT COLUMN_TYPE FROM information_schema.COLUMNS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION",
			storedName(table, c.folding))

		if len(declared) != len(spellings) {
			t.Fatalf("the table has %d columns, want %d", len(declared), len(spellings))
		}

		t.Logf("the server declared: %s", strings.Join(declared, ", "))

		t.Log("Seventeen of the eighteen logical types in one CREATE TABLE. array is the one MySQL has")
		t.Log("no type for and refuses at compile, which the untagged tests cover.")
	})
}

// keepAlphanumeric maps a type spelling into something that can be a column name.
func keepAlphanumeric(r rune) rune {
	if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
		return r
	}

	return '_'
}

func TestTheCharacterSetAndCollationAreWhatTheCatalogReports(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_c"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "s", Type: liveType(t, "string(64)")},
			{Name: "b", Type: liveType(t, "text")},
			{Name: "u", Type: liveType(t, "uuid")},
			{Name: "e", Type: liveType(t, "enum(a,b)")},
			{Name: "n", Type: liveType(t, "int32")},
		}})

		for _, column := range []string{"s", "b", "u", "e"} {
			got := scalar(t, c, "SELECT concat(CHARACTER_SET_NAME, ' ', COLLATION_NAME) "+
				"FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() "+
				"AND TABLE_NAME = ? AND COLUMN_NAME = ?", storedName(table, c.folding), column)

			if got != characterSet+" "+collation {
				t.Errorf("column %s is %q, want %q", column, got, characterSet+" "+collation)
			}
		}

		if got := scalar(t, c, "SELECT count(*) FROM information_schema.COLUMNS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'n' "+
			"AND COLLATION_NAME IS NOT NULL", storedName(table, c.folding)); got != "0" {
			t.Errorf("an int column reports a collation")
		}

		if got := scalar(t, c, "SELECT TABLE_COLLATION FROM information_schema.TABLES "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			storedName(table, c.folding)); got != collation {
			t.Errorf("the table's own collation is %q, want %q", got, collation)
		}
	})
}

func TestTheCollationMakesUniquenessMeanWhatItMeansOnTheOtherEngines(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_u"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "email", Type: liveType(t, "string(64)")},
		}})
		apply(t, c, ir.AddUnique{Table: table, Name: table + "_q", Columns: []string{"email"}})

		for _, value := range []string{"bob@x.com", "Bob@x.com"} {
			if _, err := c.Perform(t.Context(), driver.Query{
				SQL: "INSERT INTO " + quote(table) + " VALUES (?)", Args: []string{value},
			}); err != nil {
				t.Fatalf("inserting %q: %v", value, err)
			}
		}

		if got := scalar(t, c, "SELECT count(*) FROM "+quote(table)); got != "2" {
			t.Fatalf("the table holds %s rows, want 2", got)
		}

		t.Log("Both spellings are stored, which is what PostgreSQL and SQLite do and what neither")
		t.Log("server's own default collation does: under utf8mb4_0900_ai_ci on MySQL and")
		t.Log("utf8mb4_uca1400_ai_ci on MariaDB the second insert is error 1062. So this one migration")
		t.Log("declares the same constraint on all three engines only because the collation is pinned.")
		t.Log("The cost is the other side of it: equality is case-sensitive here, where a MySQL")
		t.Log("operator may expect otherwise. doc.go says so.")
	})
}

func TestTheTwoIndexLimitsAreTheServersOwnRefusalAndAreExplained(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_k"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "body", Type: liveType(t, "text"), Nullable: true},
			{Name: "a", Type: liveType(t, "string(500)"), Nullable: true},
			{Name: "b", Type: liveType(t, "string(500)"), Nullable: true},
		}})

		wide := ir.AddIndex{Table: table, Name: table + "_w", Columns: []string{"a", "b"}}

		stmts, err := compile(wide)
		if err != nil {
			t.Fatalf("compiling a wide index: %v", err)
		}

		err = c.Exec(t.Context(), stmts[0])
		if !errors.Is(err, ErrIndexKey) {
			t.Fatalf("a 4000-byte key was answered with %v, want %v", err, ErrIndexKey)
		}

		if !strings.Contains(err.Error(), "3072") {
			t.Errorf("the explanation does not name the limit: %v", err)
		}

		textIndex := ir.AddIndex{Table: table, Name: table + "_b", Columns: []string{"body"}}

		stmts, err = compile(textIndex)
		if err != nil {
			t.Fatalf("compiling a text index: %v", err)
		}

		err = c.Exec(t.Context(), stmts[0])

		if s.variant == MariaDB {
			if err != nil {
				t.Errorf("MariaDB refused a longtext index: %v", err)
			}

			t.Log("MariaDB indexes a longtext with no prefix length, so the same migration succeeds")
			t.Log("here and fails on MySQL. That asymmetry is exactly why the driver does not invent a")
			t.Log("prefix: it would make MariaDB's behavior the one nobody gets.")

			return
		}

		if !errors.Is(err, ErrIndexKey) {
			t.Fatalf("MySQL answered a longtext index with %v, want %v", err, ErrIndexKey)
		}

		if !strings.Contains(err.Error(), "MariaDB indexes") {
			t.Errorf("the explanation does not say the two engines disagree: %v", err)
		}
	})
}

func TestAPrimaryKeyOverATextColumnIsRefusedBeforeAnythingRuns(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		_, err := compile(ir.CreateTable{Table: uniqueName(t), Columns: []ir.Column{
			{Name: "body", Type: liveType(t, "text")},
		}, PrimaryKey: []string{"body"}})

		if !errors.Is(err, driver.ErrUnsupported) {
			t.Fatalf("compile returned %v, want %v", err, driver.ErrUnsupported)
		}

		t.Log("Refused with no database open, on the strength of a measurement this test is the other")
		t.Log("half of: a primary key over a longtext is error 1170 on MariaDB too, where a plain index")
		t.Log("over one is accepted. create_table carries its columns' types, so this is the one place")
		t.Log("the rule is decidable by Compile.")
	})
}

func TestANamedPrimaryKeyLandsAsPRIMARYBecauseMySQLHasNoOtherName(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_p"

		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
		}})
		apply(t, c, ir.AddPrimaryKey{Table: table, Name: "pk_" + table, Columns: []string{"id"}})

		names := read(t, c, "SELECT CONSTRAINT_NAME FROM information_schema.TABLE_CONSTRAINTS "+
			"WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = ? "+
			"AND CONSTRAINT_TYPE = 'PRIMARY KEY'", storedName(table, c.folding))

		if len(names) != 1 || names[0] != "PRIMARY" {
			t.Fatalf("the key is recorded as %v, want [PRIMARY]", names)
		}

		apply(t, c, ir.DropPrimaryKey{Table: table, Name: "pk_" + table})

		t.Log("The name the operation carried is gone, and it was never sent: MySQL accepts ADD")
		t.Log("CONSTRAINT <name> PRIMARY KEY and discards the name, so emitting it would be a")
		t.Log("statement whose name nothing can find again. The IR anticipated this -- Name is")
		t.Log("optional on both operations, and DropPrimaryKey's own comment says MySQL has none.")
	})
}

func TestASetDefaultForeignKeyWouldHaveBeenAcceptedAndNotPerformed(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		_, err := compile(ir.AddForeignKey{Table: "child", Name: "fk", Columns: []string{"pid"},
			ReferencesTable: "parent", ReferencesColumns: []string{"id"},
			OnDelete: ir.ActionSetDefault})

		if !errors.Is(err, driver.ErrUnsupported) {
			t.Fatalf("compile returned %v, want %v", err, driver.ErrUnsupported)
		}

		parent, child := uniqueName(t)+"_pp", uniqueName(t)+"_cc"

		dropLater(t, c, child, parent)
		drop(t.Context(), c, child, parent)

		exec(t, c, "CREATE TABLE "+quote(parent)+" (id int PRIMARY KEY)")
		exec(t, c, "CREATE TABLE "+quote(child)+" (id int PRIMARY KEY, pid int DEFAULT 1)")
		exec(t, c, "INSERT INTO "+quote(parent)+" VALUES (1), (2)")
		exec(t, c, "ALTER TABLE "+quote(child)+" ADD CONSTRAINT "+quote(child+"_f")+
			" FOREIGN KEY (pid) REFERENCES "+quote(parent)+" (id) ON DELETE SET DEFAULT")
		exec(t, c, "INSERT INTO "+quote(child)+" VALUES (10, 2)")

		_, err = c.Perform(t.Context(), driver.Query{
			SQL: "DELETE FROM " + quote(parent) + " WHERE id = 2",
		})
		if err == nil {
			t.Fatal("the parent row was deleted, so this server does perform SET DEFAULT")
		}

		if got := scalar(t, c, "SELECT pid FROM "+quote(child)+" WHERE id = 10"); got != "2" {
			t.Errorf("the child's value is %q, and SET DEFAULT would have made it 1", got)
		}

		t.Log("The clause is accepted and the action is not performed: the delete is refused as though")
		t.Log("it were restrict, and the child keeps its old value. MySQL records ON DELETE SET DEFAULT")
		t.Log("in SHOW CREATE TABLE anyway and MariaDB drops the clause entirely, so the catalog is not")
		t.Log("even agreed on what is there. PostgreSQL performs it, so passing this through would give")
		t.Log("one migration two meanings -- which is why compile refuses it above.")
	})
}
