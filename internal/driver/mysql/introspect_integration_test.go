//go:build integration

package mysql

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
	conformance "github.com/emrebdr/migr/test/conformance/driver"
)

// introspect reads the snapshot back, failing the test rather than returning a reason.
func introspect(t *testing.T, c *mysqlConn) *schema.Snapshot {
	t.Helper()

	snapshot, err := c.Introspect(t.Context())
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}

	return snapshot
}

// tableIn returns one table of a snapshot, failing the test when the snapshot does not hold it.
func tableIn(t *testing.T, snapshot *schema.Snapshot, name string) schema.Table {
	t.Helper()

	table, held := snapshot.Table(name)
	if !held {
		t.Fatalf("the snapshot holds no table %q, only %s", name, describeTables(snapshot))
	}

	return table
}

// columnIn returns one column of a table, failing the test when the table does not hold it.
func columnIn(t *testing.T, table schema.Table, name string) schema.Column {
	t.Helper()

	column, held := table.Column(name)
	if !held {
		t.Fatalf("table %q holds no column %q", table.Name, name)
	}

	return column
}

// describeTables names what a snapshot holds, for a failure about something it does not.
func describeTables(snapshot *schema.Snapshot) string {
	names := make([]string, 0, len(snapshot.Tables))
	for _, table := range snapshot.Tables {
		names = append(names, table.Name)
	}

	return "[" + strings.Join(names, " ") + "]"
}

// owned builds one table name this test has to itself on a database every case shares.
func owned(t *testing.T, suffix string) string {
	t.Helper()

	return uniqueName(t) + "_" + suffix
}

// raw runs one statement no operation can express, which is what a fixture needs.
func raw(t *testing.T, c *mysqlConn, statements ...string) {
	t.Helper()

	for _, statement := range statements {
		if err := c.Exec(t.Context(), driver.Statement{SQL: statement}); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

// dropped drops what a fixture built, on a context that outlives the test's own.
func dropped(t *testing.T, c *mysqlConn, tables ...string) {
	t.Helper()

	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())

		for _, table := range tables {
			if err := c.Exec(ctx, driver.Statement{SQL: "DROP TABLE IF EXISTS " + quote(table)}); err != nil {
				t.Errorf("dropping %q: %v", table, err)
			}
		}
	})
}

// applied compiles and runs every operation, asking the session for what the compiler cannot.
func applied(t *testing.T, c *mysqlConn, ops ir.Operations) {
	t.Helper()

	for _, op := range ops {
		stmts, err := compile(op)
		if err != nil {
			stmts, err = c.Expand(t.Context(), op)
		}

		if err != nil {
			t.Fatalf("%s: %v", op.Kind(), err)
		}

		for _, stmt := range stmts {
			if err := c.Exec(t.Context(), stmt); err != nil {
				t.Fatalf("%s: %s: %v", op.Kind(), stmt.SQL, err)
			}
		}
	}
}

func TestEveryDeclaredTypeIsReadBackWithTheCertaintyTheCatalogEarns(t *testing.T) {
	cases := []struct {
		spelling  string
		native    string
		logical   string
		certainty schema.Certainty
	}{
		{"string(255)", "varchar(255)", "string(255)", schema.CertaintyExact},
		{"text", "longtext", "text", schema.CertaintyExact},
		{"bool", "tinyint(1)", "bool", schema.CertaintyExact},
		{"int16", "smallint", "int16", schema.CertaintyExact},
		{"int32", "int", "int32", schema.CertaintyExact},
		{"int64", "bigint", "int64", schema.CertaintyExact},
		{"float32", "float", "float32", schema.CertaintyExact},
		{"float64", "double", "float64", schema.CertaintyExact},
		{"decimal(10,2)", "decimal(10,2)", "decimal(10,2)", schema.CertaintyExact},
		{"uuid", "char(36)", "uuid", schema.CertaintyExact},
		{"date", "date", "date", schema.CertaintyExact},
		{"time", "time(6)", "time", schema.CertaintyExact},
		{"binary", "longblob", "binary", schema.CertaintyExact},
		{"enum(a,b-c)", "enum('a','b-c')", "enum(a,b-c)", schema.CertaintyExact},
		{"timestamp", "datetime(6)", "timestamp", schema.CertaintyAmbiguous},
		{"timestamptz", "datetime(6)", "timestamp", schema.CertaintyAmbiguous},
		{"json", "json", "json", schema.CertaintyExact},
	}

	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := owned(t, "types")
		dropped(t, c, table)

		columns := make([]ir.Column, 0, len(cases))
		for i, one := range cases {
			columns = append(columns, ir.Column{
				Name: fmt.Sprintf("c%02d", i), Type: liveType(t, one.spelling), Nullable: true,
			})
		}

		applied(t, c, ir.Operations{ir.CreateTable{Table: table, Columns: columns}})

		read := tableIn(t, introspect(t, c), table)

		for i, one := range cases {
			column := columnIn(t, read, fmt.Sprintf("c%02d", i)).Type
			native := nativeFor(s, one)

			if column.Native() != native {
				t.Errorf("the catalog answered %q for a %s column, and this test expected %q",
					column.Native(), one.spelling, native)
			}

			if column.Certainty() != one.certainty {
				t.Errorf("%s reads back %s, want %s", one.spelling, column.Certainty(),
					one.certainty)
			}

			if logical, _ := column.Logical(); logical.String() != one.logical {
				t.Errorf("%s reads back as %s, through the native spelling %q",
					one.spelling, logical, column.Native())
			}
		}

		t.Log("MariaDB appends a display width to every integer type and this driver strips it, so")
		t.Log("the spelling asserted here is MySQL 8.0's own -- which is what keeps an int32 column")
		t.Log("comparable between the two variants, since an ambiguous type compares on that text.")
	})
}

// nativeFor returns the spelling one variant answers for a case, where the two variants differ.
func nativeFor(s server, one struct {
	spelling  string
	native    string
	logical   string
	certainty schema.Certainty
},
) string {
	if s.variant == MariaDB && one.spelling == "json" {
		return "longtext"
	}

	return one.native
}

func TestAJSONColumnComparesEqualBetweenTheTwoVariants(t *testing.T) {
	answers := make(map[Variant]schema.ColumnType, len(servers))

	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := owned(t, "docs")
		dropped(t, c, table)

		applied(t, c, ir.Operations{ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "doc", Type: liveType(t, "json"), Nullable: true},
			{Name: "body", Type: liveType(t, "text"), Nullable: true},
		}}})

		read := tableIn(t, introspect(t, c), table)
		doc, body := columnIn(t, read, "doc").Type, columnIn(t, read, "body").Type

		if logical, _ := doc.Logical(); logical.String() != "json" {
			t.Errorf("a json column reads back as %s (%s)", logical, doc.Native())
		}

		if logical, _ := body.Logical(); logical.String() != "text" {
			t.Errorf("a text column reads back as %s (%s)", logical, body.Native())
		}

		if doc.Equal(body) {
			t.Error("a json column and a text column read back as the same type")
			t.Log("On MariaDB both are longtext, and the json_valid check it attaches to the one")
			t.Log("is the only thing that tells them apart.")
		}

		if len(read.Checks) != 0 {
			t.Errorf("the check MariaDB attaches to a json column is reported as a constraint: %+v",
				read.Checks)
			t.Log("A differ reading that would propose dropping the column's own type.")
		}

		answers[s.variant] = doc
	})

	if len(answers) == len(servers) && !answers[MySQL].Equal(answers[MariaDB]) {
		t.Errorf("a json column reads back as %s on one variant and %s on the other",
			answers[MySQL], answers[MariaDB])
	}
}

func TestAHandWrittenCheckNamedAfterItsColumnIsNotAbsorbed(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := owned(t, "nearly")
		dropped(t, c, table)

		raw(t, c, "CREATE TABLE "+quote(table)+" (`state` varchar(10), "+
			"CONSTRAINT `state` CHECK (`state` <> ''))")

		read := tableIn(t, introspect(t, c), table)

		if len(read.Checks) != 1 {
			t.Fatalf("a hand-written check named after its column reads back as %+v", read.Checks)
		}

		t.Log("MariaDB names a column's own check after the column, which is also how it names the")
		t.Log("json_valid check it attaches -- so the clause and not the name is what decides.")
	})
}

func TestADefaultIsReadBackAsTheLiteralOrTheFunctionItHolds(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := owned(t, "defaults")
		dropped(t, c, table)

		applied(t, c, ir.Operations{ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "plain", Type: liveType(t, "string(10)"), Nullable: true,
				Default: ir.Literal("x")},
			{Name: "quoted", Type: liveType(t, "string(10)"), Nullable: true,
				Default: ir.Literal("it's")},
			{Name: "numeric", Type: liveType(t, "int32"), Nullable: true,
				Default: ir.Literal("7")},
			{Name: "blank", Type: liveType(t, "string(10)"), Nullable: true,
				Default: ir.Literal("")},
			{Name: "stamped", Type: liveType(t, "timestamp"), Nullable: true,
				Default: ir.Call(ir.FuncCurrentTimestamp)},
			{Name: "dated", Type: liveType(t, "date"), Nullable: true,
				Default: ir.Call(ir.FuncCurrentDate)},
			{Name: "timed", Type: liveType(t, "time"), Nullable: true,
				Default: ir.Call(ir.FuncCurrentTime)},
			{Name: "identified", Type: liveType(t, "uuid"), Nullable: true,
				Default: ir.Call(ir.FuncUUID)},
			{Name: "none", Type: liveType(t, "int32"), Nullable: true},
			{Name: "required", Type: liveType(t, "int32")},
		}}})

		read := tableIn(t, introspect(t, c), table)

		for _, one := range []struct {
			column string
			want   *schema.ColumnDefault
		}{
			{"plain", &schema.ColumnDefault{Literal: literal("x")}},
			{"quoted", &schema.ColumnDefault{Literal: literal("it's")}},
			{"numeric", &schema.ColumnDefault{Literal: literal("7")}},
			{"blank", &schema.ColumnDefault{Literal: literal("")}},
			{"stamped", &schema.ColumnDefault{Function: schema.FuncCurrentTimestamp}},
			{"dated", &schema.ColumnDefault{Function: schema.FuncCurrentDate}},
			{"timed", &schema.ColumnDefault{Function: schema.FuncCurrentTime}},
			{"identified", &schema.ColumnDefault{Function: schema.FuncUUID}},
			{"none", nil},
			{"required", nil},
		} {
			if got := columnIn(t, read, one.column).Default; !got.Equal(one.want) {
				t.Errorf("the default on %q reads back as %v, want %v", one.column, got, one.want)
			}
		}

		t.Log("The two variants record a default differently enough that the reading is split:")
		t.Log("MySQL marks an expression in EXTRA and records a literal unquoted, where MariaDB")
		t.Log("quotes a literal and spells a nullable column's absent default as NULL -- the four")
		t.Log("characters, which is why a NOT NULL column with no default is the other half.")
	})
}

func TestAnUnspecifiedReferentialActionReadsTheSameOnBothVariants(t *testing.T) {
	answers := make(map[Variant]string, len(servers))

	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		parent, child := owned(t, "parent"), owned(t, "child")
		dropped(t, c, child, parent)

		applied(t, c, ir.Operations{
			ir.CreateTable{Table: parent, Columns: []ir.Column{
				{Name: "id", Type: liveType(t, "int64")},
			}, PrimaryKey: []string{"id"}},
			ir.CreateTable{Table: child, Columns: []ir.Column{
				{Name: "id", Type: liveType(t, "int64")},
				{Name: "parent_id", Type: liveType(t, "int64"), Nullable: true},
			}, PrimaryKey: []string{"id"}},
			ir.AddForeignKey{Table: child, Name: "fk_" + child, Columns: []string{"parent_id"},
				ReferencesTable: parent, ReferencesColumns: []string{"id"},
				OnDelete: ir.ActionCascade},
		})

		read := tableIn(t, introspect(t, c), child)

		if len(read.ForeignKeys) != 1 {
			t.Fatalf("the foreign keys read back as %+v", read.ForeignKeys)
		}

		key := read.ForeignKeys[0]

		if key.OnUpdate != schema.ActionNoAction {
			t.Errorf("an unspecified on_update reads back as %q, want no_action", key.OnUpdate)
			t.Log("MariaDB answers RESTRICT where MySQL answers NO ACTION, and its own SHOW CREATE")
			t.Log("drops an explicit RESTRICT while keeping an explicit NO ACTION -- so on that")
			t.Log("variant the two are one answer and this driver reads it as the default it is.")
		}

		if key.OnDelete != schema.ActionCascade {
			t.Errorf("a declared on_delete reads back as %q", key.OnDelete)
		}

		if len(read.Indexes) != 0 {
			t.Errorf("this server's own index for the foreign key is reported as an index: %+v",
				read.Indexes)
			t.Log("It creates one for a key over an unindexed column, named after the constraint,")
			t.Log("and no other engine does -- so reporting it would break the cross-engine claim.")
		}

		answers[s.variant] = string(key.OnDelete) + " " + string(key.OnUpdate)
	})

	if len(answers) == len(servers) && answers[MySQL] != answers[MariaDB] {
		t.Errorf("one declared key's actions read back as %q on MySQL and %q on MariaDB",
			answers[MySQL], answers[MariaDB])
		t.Log("The names and the table names differ because each server's fixture is its own, so")
		t.Log("what is compared here is the pair of actions and nothing else.")
	}
}

func TestAPrimaryKeyIsReportedWithoutItsIndexAndUnderThisEnginesOwnName(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := owned(t, "keyed")
		dropped(t, c, table)

		applied(t, c, ir.Operations{
			ir.CreateTable{Table: table, Columns: []ir.Column{
				{Name: "left", Type: liveType(t, "int64")},
				{Name: "right", Type: liveType(t, "int64")},
				{Name: "code", Type: liveType(t, "string(8)")},
			}, PrimaryKey: []string{"right", "left"}},
			ir.AddUnique{Table: table, Name: "uq_" + table, Columns: []string{"code"}},
		})

		read := tableIn(t, introspect(t, c), table)

		if read.PrimaryKey == nil {
			t.Fatal("a table with a composite primary key reports none")
		}

		if got := strings.Join(read.PrimaryKey.Columns, ","); got != "right,left" {
			t.Errorf("the key's columns read back as %q, want the order the key declares", got)
		}

		if read.PrimaryKey.Name != primaryIndex {
			t.Errorf("the key reads back named %q, and this engine calls every one PRIMARY",
				read.PrimaryKey.Name)
		}

		if len(read.Indexes) != 0 {
			t.Errorf("the indexes behind the key and the unique constraint are reported as "+
				"indexes of their own: %+v", read.Indexes)
		}

		if len(read.Uniques) != 1 || read.Uniques[0].Name != "uq_"+table {
			t.Errorf("the unique constraint reads back as %+v", read.Uniques)
		}
	})
}

func TestEverythingTheOperationSetCannotExpressIsMarkedRatherThanOmitted(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := owned(t, "beyond")
		dropped(t, c, table)

		raw(t, c, "CREATE TABLE "+quote(table)+" ("+
			"`id` bigint NOT NULL AUTO_INCREMENT PRIMARY KEY,"+
			"`body` longtext,"+
			"`gen` int GENERATED ALWAYS AS (1 + 1) STORED,"+
			"`shouty` varchar(10) COLLATE utf8mb4_general_ci,"+
			"INDEX `idx_prefix` (`body`(10)),"+
			"INDEX `idx_desc` (`shouty` DESC)"+
			") DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin")

		snapshot := introspect(t, c)

		findings := make(map[string]string, 8)
		for _, found := range schema.Unrepresentable(*snapshot) {
			findings[string(found.Element)+":"+found.Object] = found.Detail
		}

		for _, one := range []struct {
			at   string
			says string
		}{
			{"column:id", "AUTO_INCREMENT"},
			{"column:gen", "generated"},
			{"column:shouty", "collation"},
			{"index:idx_prefix", "prefix"},
			{"index:idx_desc", "descending order"},
		} {
			detail, marked := findings[one.at]
			if !marked {
				t.Errorf("%s is not marked at all, so a differ would act on it", one.at)

				continue
			}

			if !strings.Contains(detail, one.says) {
				t.Errorf("%s is marked %q, which does not say %q", one.at, detail, one.says)
			}
		}

		read := tableIn(t, introspect(t, c), table)
		if body := columnIn(t, read, "body"); body.Beyond != nil {
			t.Errorf("a column whose collation is its table's own is marked %v", body.Beyond)
			t.Log("Every text column on this engine has a collation, inherited from the table, so")
			t.Log("marking all of them would make a varchar column compare unequal to the same")
			t.Log("column on another engine for a reason nobody declared.")
		}
	})
}

func TestTheSnapshotHoldsNeitherTheHistoryTableNorItsIndexes(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := owned(t, "app")
		dropped(t, c, table, c.table, c.seeds)

		if err := c.History().Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		if err := c.Seeds().Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure the seed history: %v", err)
		}

		applied(t, c, ir.Operations{ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
		}, PrimaryKey: []string{"id"}}})

		snapshot := introspect(t, c)

		for _, name := range []string{c.table, c.seeds} {
			if _, held := snapshot.Table(name); held {
				t.Errorf("the snapshot holds %q, which a differ would propose dropping", name)
			}
		}

		if _, held := snapshot.Table(table); !held {
			t.Errorf("the snapshot lost the table a migration created: %s",
				describeTables(snapshot))
		}
	})
}

func TestASnapshotCanBeTakenInsideAnOpenTransaction(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := owned(t, "app")
		dropped(t, c, table)

		applied(t, c, ir.Operations{ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
		}, PrimaryKey: []string{"id"}}})

		var inside *schema.Snapshot

		err := c.Transact(t.Context(), func(ctx context.Context) error {
			read, err := c.Introspect(ctx)
			inside = read

			return err
		})
		if err != nil {
			t.Fatalf("introspecting inside a transaction: %v", err)
		}

		if _, held := inside.Table(table); !held {
			t.Error("the snapshot read inside a transaction lost the table")
		}
	})
}

func TestTheSnapshotConformanceSuitePasses(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		d, err := New(driver.Options{})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		conformance.Snapshot{Driver: d, Conn: c, Prefix: uniqueName(t)}.Run(t)
	})

	t.Log("Both variants are held to the one expected snapshot PostgreSQL and SQLite are held to,")
	t.Log("which is what M11-T2's done-when asks for: equivalent schemas compare equal across")
	t.Log("engines despite differing native type names.")
}

func TestTheDiffConformanceSuitePasses(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		d, err := New(driver.Options{})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		conformance.Diff{Driver: d, Conn: c, Prefix: uniqueName(t)}.Run(t)
	})

	t.Log("This is the only engine that changes a column by restating its whole definition, so it")
	t.Log("is the one that holds the differ to stating a default it is not changing (ADR 0058).")
}
