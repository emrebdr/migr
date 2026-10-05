package sqlite

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
func introspect(t *testing.T, c *sqliteConn) *schema.Snapshot {
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

func TestAnEmptyDatabaseIntrospectsAsAnEmptySnapshot(t *testing.T) {
	snapshot := introspect(t, openMemory(t))

	if !snapshot.Empty() {
		t.Errorf("a database with nothing in it reported %s", describeTables(snapshot))
	}
}

func TestEveryDeclaredTypeIsReadBackWithTheCertaintyTheCatalogEarns(t *testing.T) {
	c := openMemory(t)

	cases := []struct {
		spelling  string
		native    string
		logical   string
		certainty schema.Certainty
	}{
		{"string(255)", "varchar(255)", "string(255)", schema.CertaintyExact},
		{"decimal(10,2)", "decimal(10,2)", "decimal(10,2)", schema.CertaintyExact},
		{"bool", "boolean", "bool", schema.CertaintyExact},
		{"uuid", "uuid", "uuid", schema.CertaintyExact},
		{"timestamp", "timestamp", "timestamp", schema.CertaintyExact},
		{"timestamptz", "timestamptz", "timestamptz", schema.CertaintyExact},
		{"date", "date", "date", schema.CertaintyExact},
		{"time", "time", "time", schema.CertaintyExact},
		{"binary", "BLOB", "binary", schema.CertaintyExact},
		{"int16", "INTEGER", "int64", schema.CertaintyAmbiguous},
		{"int32", "INTEGER", "int64", schema.CertaintyAmbiguous},
		{"int64", "INTEGER", "int64", schema.CertaintyAmbiguous},
		{"float32", "REAL", "float64", schema.CertaintyAmbiguous},
		{"float64", "REAL", "float64", schema.CertaintyAmbiguous},
		{"text", "TEXT", "text", schema.CertaintyAmbiguous},
		{"json", "TEXT", "text", schema.CertaintyAmbiguous},
		{"enum(a,b-c)", "TEXT", "enum(a,b-c)", schema.CertaintyAmbiguous},
	}

	columns := make([]ir.Column, 0, len(cases))
	for i, one := range cases {
		columns = append(columns, ir.Column{
			Name: fmt.Sprintf("c%02d", i), Type: mustType(t, one.spelling), Nullable: true,
		})
	}

	apply(t, c, ir.CreateTable{Table: "types", Columns: columns})

	table := tableIn(t, introspect(t, c), "types")

	for i, one := range cases {
		name := fmt.Sprintf("c%02d", i)

		t.Run(one.spelling, func(t *testing.T) {
			read := columnIn(t, table, name).Type

			if read.Native() != one.native {
				t.Errorf("the catalog answered %q for a %s column, and this test expected %q",
					read.Native(), one.spelling, one.native)
			}

			if read.Certainty() != one.certainty {
				t.Errorf("%s reads back %s, want %s", one.spelling, read.Certainty(), one.certainty)
			}

			if logical, _ := read.Logical(); logical.String() != one.logical {
				t.Errorf("%s reads back as %s, want %s", one.spelling, logical, one.logical)
			}
		})
	}

	t.Log("The native spelling is asserted against what the catalog answered rather than against")
	t.Log("what nativeType emits, because the two differ: measured, pragma_table_xinfo uppercases")
	t.Log("text, integer, real and blob and leaves every other spelling verbatim. A reverse map")
	t.Log("keyed on the compiler's own output would be wrong for five of the seventeen.")
}

func TestAnAmbiguousTypeNeverComparesEqualToAnExactOne(t *testing.T) {
	c := openMemory(t)

	apply(t, c, ir.CreateTable{Table: "widths", Columns: []ir.Column{
		{Name: "small", Type: mustType(t, "int16"), Nullable: true},
		{Name: "big", Type: mustType(t, "int64"), Nullable: true},
	}})

	table := tableIn(t, introspect(t, c), "widths")
	small, big := columnIn(t, table, "small").Type, columnIn(t, table, "big").Type

	if !small.Equal(big) {
		t.Error("two columns this engine declares identically read back as different types")
	}

	exact, err := schema.ExactType(mustType(t, "int64"), "INTEGER")
	if err != nil {
		t.Fatalf("ExactType: %v", err)
	}

	if big.Equal(exact) {
		t.Error("an ambiguous int64 compared equal to an exact one")
		t.Log("ADR 0065: a guess never equals a fact. This driver cannot tell the three integer")
		t.Log("widths apart, so an engine that can must not compare equal to it.")
	}
}

func TestADefaultIsReadBackAsTheLiteralOrTheFunctionItHolds(t *testing.T) {
	c := openMemory(t)

	apply(t, c, ir.CreateTable{Table: "defaults", Columns: []ir.Column{
		{Name: "plain", Type: mustType(t, "string(10)"), Nullable: true, Default: ir.Literal("x")},
		{Name: "quoted", Type: mustType(t, "string(10)"), Nullable: true, Default: ir.Literal("it's")},
		{Name: "numeric", Type: mustType(t, "int32"), Nullable: true, Default: ir.Literal("7")},
		{Name: "blank", Type: mustType(t, "string(10)"), Nullable: true, Default: ir.Literal("")},
		{Name: "stamped", Type: mustType(t, "timestamp"), Nullable: true,
			Default: ir.Call(ir.FuncCurrentTimestamp)},
		{Name: "dated", Type: mustType(t, "date"), Nullable: true,
			Default: ir.Call(ir.FuncCurrentDate)},
		{Name: "timed", Type: mustType(t, "time"), Nullable: true,
			Default: ir.Call(ir.FuncCurrentTime)},
		{Name: "none", Type: mustType(t, "int32"), Nullable: true},
	}})

	table := tableIn(t, introspect(t, c), "defaults")

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
		{"none", nil},
	} {
		t.Run(one.column, func(t *testing.T) {
			read := columnIn(t, table, one.column).Default

			if !read.Equal(one.want) {
				t.Errorf("default reads back as %v, want %v", read, one.want)
			}
		})
	}

	t.Log("SQLite records every literal quoted, this driver's quoteLiteral included, so a numeric")
	t.Log("default comes back as '7' and reads as the literal 7. PostgreSQL records the same")
	t.Log("default unquoted, which is why unquoting is the driver's own and not the model's.")
}

// literal returns a pointer to a default's text, which is what distinguishes empty from absent.
func literal(v string) *string {
	return &v
}

func TestADefaultThisVocabularyCannotNameIsCarriedAsEngineText(t *testing.T) {
	c := openMemory(t)

	exec(t, c, `CREATE TABLE computed ("n" integer DEFAULT (abs(-1)))`)

	read := columnIn(t, tableIn(t, introspect(t, c), "computed"), "n").Default

	if read == nil || read.Expression == "" {
		t.Fatalf("an expression default reads back as %v, want engine text", read)
	}

	if read.Literal != nil || read.Function != "" {
		t.Errorf("an expression default also claimed to be a literal or a function: %+v", read)
	}
}

func TestEveryUniqueIndexIsReportedAsTheConstraintItStandsFor(t *testing.T) {
	c := openMemory(t)

	apply(t, c, ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
		{Name: "email", Type: mustType(t, "string(255)")},
		{Name: "handle", Type: mustType(t, "string(32)")},
	}, PrimaryKey: []string{"id"}})
	apply(t, c, ir.AddUnique{Table: "users", Name: "uq_users_email", Columns: []string{"email"}})
	apply(t, c, ir.AddIndex{Table: "users", Name: "idx_users_handle", Columns: []string{"handle"}})

	table := tableIn(t, introspect(t, c), "users")

	if len(table.Uniques) != 1 || table.Uniques[0].Name != "uq_users_email" {
		t.Errorf("the unique constraint reads back as %+v", table.Uniques)
	}

	if len(table.Indexes) != 1 || table.Indexes[0].Name != "idx_users_handle" {
		t.Errorf("the indexes read back as %+v", table.Indexes)
	}

	for _, index := range table.Indexes {
		if index.Unique {
			t.Errorf("index %q reports itself unique, which no driver may set", index.Name)
			t.Log("add_unique compiles to CREATE UNIQUE INDEX here and ir.AddIndex has no Unique")
			t.Log("field, so a unique index reported as an index is one no operation could create.")
		}
	}
}

func TestAPrimaryKeyIsReadFromTheColumnsRatherThanFromAnIndex(t *testing.T) {
	c := openMemory(t)

	apply(t, c, ir.CreateTable{Table: "pairs", Columns: []ir.Column{
		{Name: "left", Type: mustType(t, "int64")},
		{Name: "right", Type: mustType(t, "int64")},
		{Name: "note", Type: mustType(t, "text"), Nullable: true},
	}, PrimaryKey: []string{"right", "left"}})

	table := tableIn(t, introspect(t, c), "pairs")

	if table.PrimaryKey == nil {
		t.Fatal("a table with a composite primary key reports none")
	}

	if got := strings.Join(table.PrimaryKey.Columns, ","); got != "right,left" {
		t.Errorf("the key's columns read back as %q, want the order the key declares", got)
	}

	for _, index := range table.Indexes {
		if strings.HasPrefix(index.Name, "sqlite_autoindex") {
			t.Errorf("the index SQLite made for the key is reported as an index of its own: %q",
				index.Name)
		}
	}

	for _, unique := range table.Uniques {
		if strings.HasPrefix(unique.Name, "sqlite_autoindex") {
			t.Errorf("the index SQLite made for the key is reported as a unique constraint: %q",
				unique.Name)
		}
	}
}

func TestAForeignKeyIsNamedFromTheDefinitionThePragmaCannotName(t *testing.T) {
	c := openMemory(t)

	buildParentAndChild(t, c)

	table := tableIn(t, introspect(t, c), "orders")

	if len(table.ForeignKeys) != 1 {
		t.Fatalf("the foreign keys read back as %+v", table.ForeignKeys)
	}

	key := table.ForeignKeys[0]

	if key.Name != "fk_orders_user" {
		t.Errorf("the key reads back named %q, and pragma_foreign_key_list reports no name at all",
			key.Name)
	}

	if key.ReferencesTable != "users" || strings.Join(key.ReferencesColumns, ",") != "id" {
		t.Errorf("the key references %q(%v)", key.ReferencesTable, key.ReferencesColumns)
	}

	if key.OnDelete != schema.ActionCascade {
		t.Errorf("on_delete reads back as %q, want cascade", key.OnDelete)
	}

	if key.OnUpdate != schema.ActionNoAction {
		t.Errorf("on_update reads back as %q: this engine answers NO ACTION where the other two "+
			"omit the clause, which ReferentialAction.Canonical is what settles", key.OnUpdate)
	}
}

// buildParentAndChild creates a referenced table and one referencing it through a named key.
func buildParentAndChild(t *testing.T, c *sqliteConn) {
	t.Helper()

	apply(t, c, ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
	}, PrimaryKey: []string{"id"}})

	exec(t, c, `CREATE TABLE "orders" (
		"id" integer NOT NULL,
		"user_id" integer,
		PRIMARY KEY ("id"),
		CONSTRAINT "fk_orders_user" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON DELETE CASCADE
	)`)
}

func TestAReferenceNamingNoColumnsResolvesToTheParentsOwnKey(t *testing.T) {
	c := openMemory(t)

	exec(t, c, `CREATE TABLE "users" ("id" integer NOT NULL, PRIMARY KEY ("id"))`)
	exec(t, c, `CREATE TABLE "orders" ("id" integer, "user_id" integer REFERENCES "users")`)

	table := tableIn(t, introspect(t, c), "orders")

	if len(table.ForeignKeys) != 1 {
		t.Fatalf("the foreign keys read back as %+v, and the table's marker says %v",
			table.ForeignKeys, table.Beyond)
	}

	if got := strings.Join(table.ForeignKeys[0].ReferencesColumns, ","); got != "id" {
		t.Errorf("the referenced columns read back as %q, want the parent's own key", got)
		t.Log("pragma_foreign_key_list answers a null referenced column for REFERENCES t with no")
		t.Log("column list, and SQLite resolves that to the parent's primary key.")
	}
}

func TestAnEnumIsAbsorbedIntoItsColumnAndAHandWrittenCheckIsNot(t *testing.T) {
	c := openMemory(t)

	apply(t, c, ir.CreateTable{Table: "labels", Columns: []ir.Column{
		{Name: "state", Type: mustType(t, "enum(draft,live)"), Nullable: true},
	}})

	exec(t, c, `CREATE TABLE "nearly" ("state" text CHECK (state IN ('draft','live')))`)
	exec(t, c, `CREATE TABLE "exactly" ("state" text CHECK ("state" IN ('draft', 'live')))`)

	snapshot := introspect(t, c)

	absorbed := tableIn(t, snapshot, "labels")
	if len(absorbed.Checks) != 0 {
		t.Errorf("the check holding an enum's labels is reported as a constraint: %+v",
			absorbed.Checks)
		t.Log("A differ reading that would propose dropping the column's own type.")
	}

	if logical, _ := columnIn(t, absorbed, "state").Type.Logical(); logical.String() !=
		"enum(draft,live)" {
		t.Errorf("the enum column reads back as %s", logical)
	}

	nearly := tableIn(t, snapshot, "nearly")
	if len(nearly.Checks) != 1 {
		t.Errorf("a hand-written check that is not this driver's rendering was absorbed: %+v",
			nearly.Checks)
	}

	if logical, _ := columnIn(t, nearly, "state").Type.Logical(); logical.String() != "text" {
		t.Errorf("a text column with a hand-written check reads back as %s", logical)
	}

	exactly := tableIn(t, snapshot, "exactly")
	if len(exactly.Checks) != 0 {
		t.Errorf("a check byte-identical to this driver's own rendering was not absorbed: %+v",
			exactly.Checks)
	}

	if columnIn(t, exactly, "state").Type.Exact() {
		t.Error("an absorbed enum claims to be an exact answer")
	}

	t.Log("The bound is measured rather than chosen: a hand-written CHECK (state IN ('a','b')) is")
	t.Log("not what labelCheck renders and stays a check, while CHECK (\"state\" IN ('a', 'b')) is")
	t.Log("byte-identical to it and is indistinguishable from an enum -- which is why the type")
	t.Log("stays ambiguous and nothing acts on it without asking Exact first.")
}

func TestACheckHasNoCatalogSoItComesFromTheRecordedDefinition(t *testing.T) {
	c := openMemory(t)

	apply(t, c, ir.CreateTable{Table: "bounded", Columns: []ir.Column{
		{Name: "n", Type: mustType(t, "int32"), Nullable: true},
	}})

	stmts, err := c.Expand(t.Context(), ir.AddCheck{Table: "bounded", Name: "ck_bounded_n",
		Expression: `"n" > 0`})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	for _, stmt := range stmts {
		if err := c.Exec(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt.SQL, err)
		}
	}

	table := tableIn(t, introspect(t, c), "bounded")

	if len(table.Checks) != 1 {
		t.Fatalf("the checks read back as %+v", table.Checks)
	}

	if table.Checks[0].Name != "ck_bounded_n" || table.Checks[0].Expression != `"n" > 0` {
		t.Errorf("the check reads back as %+v", table.Checks[0])
	}
}

func TestAViewCarriesItsQueryRatherThanTheStatementAroundIt(t *testing.T) {
	c := openMemory(t)

	body := `SELECT "id" FROM "users" WHERE "id" > 0`

	apply(t, c, ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
	}})
	apply(t, c, ir.CreateView{View: "live_users", Query: body})

	snapshot := introspect(t, c)

	view, held := snapshot.View("live_users")
	if !held {
		t.Fatal("the snapshot holds no view")
	}

	if view.Query != body {
		t.Errorf("the view's body reads back as %q, want %q", view.Query, body)
		t.Log("Every other engine reports the query alone, so reporting the whole CREATE VIEW")
		t.Log("statement here would make a same-engine round trip compare unequal to itself.")
	}
}

func TestEverythingTheOperationSetCannotExpressIsMarkedRatherThanOmitted(t *testing.T) {
	c := openMemory(t)

	for _, statement := range []string{
		`CREATE TABLE "beyond" (
			"id" integer PRIMARY KEY AUTOINCREMENT,
			"plain" text,
			"shouty" text COLLATE NOCASE,
			"untyped",
			"virt" integer GENERATED ALWAYS AS ("id" * 2) VIRTUAL,
			"stored" integer GENERATED ALWAYS AS ("id" * 3) STORED
		)`,
		`CREATE INDEX "idx_partial" ON "beyond" ("plain") WHERE "plain" IS NOT NULL`,
		`CREATE INDEX "idx_desc" ON "beyond" ("plain" DESC)`,
		`CREATE INDEX "idx_expr" ON "beyond" (lower("plain"))`,
		`CREATE INDEX "idx_mixed" ON "beyond" ("plain", lower("plain"))`,
		`CREATE TABLE "strict_rowid" ("id" integer PRIMARY KEY, "n" integer NOT NULL) WITHOUT ROWID`,
		`CREATE TABLE "deferred" ("id" integer PRIMARY KEY, "p" integer,
			CONSTRAINT "fk_deferred" FOREIGN KEY ("p") REFERENCES "beyond" ("id")
				DEFERRABLE INITIALLY DEFERRED)`,
	} {
		exec(t, c, statement)
	}

	snapshot := introspect(t, c)

	findings := make(map[string]string, 8)
	for _, found := range schema.Unrepresentable(*snapshot) {
		findings[string(found.Element)+":"+found.Object] = found.Detail
	}

	for _, one := range []struct {
		at   string
		says string
	}{
		{"column:untyped", "no type at all"},
		{"column:virt", "virtual generated"},
		{"column:stored", "stored generated"},
		{"column:shouty", "COLLATE"},
		{"column:id", "AUTOINCREMENT"},
		{"index:idx_partial", "partial index"},
		{"index:idx_desc", "descending order"},
		{"table:", "WITHOUT ROWID"},
		{"foreign_key:fk_deferred", "DEFERRABLE"},
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

	expression := tableIn(t, snapshot, "beyond")
	if expression.Beyond == nil || !strings.Contains(expression.Beyond.Detail, "idx_expr") {
		t.Errorf("an index over an expression is not named on its table: %v", expression.Beyond)
		t.Log("schema.Index holds columns, and an expression is not one, so the index cannot be")
		t.Log("reported as an index at all -- it is named on the table's own marker instead.")
	}

	for _, index := range expression.Indexes {
		if index.Name == "idx_expr" || index.Name == "idx_mixed" {
			t.Errorf("index %q is reported as an index over columns", index.Name)
			t.Log("idx_mixed covers a column and an expression, so reporting the column half of")
			t.Log("it would be an index a differ could act on that is not the index that exists.")
		}
	}

	if !strings.Contains(expression.Beyond.Detail, "idx_mixed") {
		t.Errorf("an index covering a column and an expression is not named on its table: %v",
			expression.Beyond)
	}
}

func TestTheSnapshotHoldsNeitherMigrsOwnHistoryNorSQLitesOwnTables(t *testing.T) {
	c := openFile(t)

	if err := c.History().Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	apply(t, c, ir.CreateTable{Table: "app", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
	}, PrimaryKey: []string{"id"}})
	exec(t, c, `CREATE TABLE "counted" ("id" integer PRIMARY KEY AUTOINCREMENT)`)
	exec(t, c, `INSERT INTO "counted" DEFAULT VALUES`)

	snapshot := introspect(t, c)

	for _, name := range []string{c.table, "sqlite_sequence"} {
		if _, held := snapshot.Table(name); held {
			t.Errorf("the snapshot holds %q, which a differ would propose dropping", name)
		}
	}

	if _, held := snapshot.Table("app"); !held {
		t.Error("the snapshot lost the table a migration created")
	}
}

func TestASnapshotCanBeTakenInsideAnOpenTransaction(t *testing.T) {
	c := openFile(t)

	apply(t, c, ir.CreateTable{Table: "app", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
	}})

	var inside *schema.Snapshot

	err := c.Transact(t.Context(), func(ctx context.Context) error {
		read, err := c.Introspect(ctx)
		inside = read

		return err
	})
	if err != nil {
		t.Fatalf("introspecting inside a transaction: %v", err)
	}

	if !inside.Equal(*introspect(t, c)) {
		t.Error("the snapshot read inside a transaction differs from the one read outside it")
		t.Log("The engine's own check runs before the transaction a migration's statements open,")
		t.Log("and a resume reads one inside it, so both must work (ADR 0017).")
	}
}

func TestADriverReadsItsOwnCompiledSchemaBackUnchanged(t *testing.T) {
	c := openMemory(t)

	for _, op := range migration(t) {
		apply(t, c, op)
	}

	before := introspect(t, c)

	if err := before.Validate(); err != nil {
		t.Fatalf("the snapshot this driver built is not usable: %v", err)
	}

	after := introspect(t, c)

	if !before.Equal(*after) {
		t.Error("two snapshots of one unchanged database compare unequal")
		t.Log("Introspection is a planning path, so it must be deterministic (invariant #8).")
	}
}

// compile-time proof that a session satisfies the contract the method was added to.
var _ interface {
	Introspect(context.Context) (*schema.Snapshot, error)
} = (*sqliteConn)(nil)

func TestIntrospectionFailsRatherThanGuessingAtAnActionItCannotName(t *testing.T) {
	if _, err := referentialAction("SET FIRE TO"); err == nil {
		t.Error("an action this vocabulary has no name for was read back as one")
	}

	for _, recorded := range []string{"", "NO ACTION", "no action"} {
		action, err := referentialAction(recorded)
		if err != nil || action != schema.ActionNoAction {
			t.Errorf("referentialAction(%q) = %q, %v", recorded, action, err)
		}
	}
}

// compile-time proof that the reads this file asserts go through the contract's own row shape.
var _ = driver.Rows{}

func TestTheSnapshotConformanceSuitePasses(t *testing.T) {
	d, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conformance.Snapshot{Driver: d, Conn: openFile(t), Prefix: "migr_snap"}.Run(t)

	t.Log("Untagged, for ADR 0050's reason: this engine's requirement is a t.TempDir() and a few")
	t.Log("milliseconds, so one third of the cross-engine claim is inside make check.")
}
