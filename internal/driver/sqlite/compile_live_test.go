package sqlite

import (
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// apply compiles one operation and runs every statement it produced, failing on either.
func apply(t *testing.T, c *sqliteConn, op ir.Operation) {
	t.Helper()

	if err := op.Validate(); err != nil {
		t.Fatalf("%s is not a well-formed operation: %v", op.Kind(), err)
	}

	stmts, err := compile(op)
	if err != nil {
		t.Fatalf("compiling %s: %v", op.Kind(), err)
	}

	for i, stmt := range stmts {
		if err := c.Exec(t.Context(), stmt); err != nil {
			t.Fatalf("%s statement %d was refused by SQLite:\n  %s\n  %v",
				op.Kind(), i+1, stmt.SQL, err)
		}
	}
}

// migration returns one coherent sequence exercising every operation this driver compiles.
func migration(t *testing.T) []ir.Operation {
	t.Helper()

	return []ir.Operation{
		ir.CreateTable{Table: "users", Columns: []ir.Column{
			{Name: "id", Type: mustType(t, "int64")},
			{Name: "email", Type: mustType(t, "string(255)")},
			{Name: "status", Type: mustType(t, "enum(draft,live)"), Default: ir.Literal("draft")},
			{Name: "score", Type: mustType(t, "decimal(10,2)"), Nullable: true},
			{Name: "seen_at", Type: mustType(t, "timestamptz"), Nullable: true,
				Default: ir.Call(ir.FuncCurrentTimestamp)},
		}, PrimaryKey: []string{"id"}},
		ir.AddColumn{Table: "users", Column: ir.Column{Name: "nickname",
			Type: mustType(t, "string(64)"), Nullable: true, Default: ir.Literal("anonymous")}},
		ir.AddIndex{Table: "users", Name: "idx_users_email", Columns: []string{"email"}},
		ir.AddUnique{Table: "users", Name: "uq_users_nickname", Columns: []string{"nickname"}},
		ir.RenameColumn{Table: "users", From: "email", To: "email_address"},
		ir.CreateView{View: "active_users", Query: `SELECT "id" FROM "users"`},
		ir.CreateView{View: "active_users", OrReplace: true,
			Query: `SELECT "id", "email_address" FROM "users"`},
		ir.DropView{View: "active_users"},
		ir.DropIndex{Table: "users", Name: "idx_users_email"},
		ir.DropUnique{Table: "users", Name: "uq_users_nickname"},
		ir.DropColumn{Table: "users", Column: "nickname"},
		ir.RenameTable{From: "users", To: "accounts"},
		ir.Raw{Driver: Name, SQL: "ANALYZE"},
		ir.DropTable{Table: "accounts"},
	}
}

func TestEveryOperationThisDriverCompilesIsAcceptedBySQLite(t *testing.T) {
	c := openFile(t)

	sequence := migration(t)
	for _, op := range sequence {
		apply(t, c, op)
	}

	covered := make(map[ir.Kind]bool, len(sequence))
	for _, op := range sequence {
		covered[op.Kind()] = true
	}

	for _, kind := range ir.Kinds() {
		if covered[kind] == needsRebuild[kind] {
			t.Errorf("%s is %s and the sequence %s it", kind,
				refusalOrSupport(needsRebuild[kind]), inclusion(covered[kind]))
		}
	}

	t.Log("This is the layer PostgreSQL's driver cannot have untagged: a live SQLite database is")
	t.Log("a t.TempDir(), so the golden files are not merely the right shape -- every statement in")
	t.Log("them is accepted by the engine, inside make check and on both CI legs. Coverage is")
	t.Log("derived from ir.Kinds(), so a twenty-first operation fails here the day it is added.")
}

// refusalOrSupport names which side of the rebuild boundary an operation is on.
func refusalOrSupport(rebuilds bool) string {
	if rebuilds {
		return "refused until M8-T4"
	}

	return "compiled by this driver"
}

// inclusion names whether the live sequence reached an operation.
func inclusion(covered bool) string {
	if covered {
		return "runs"
	}

	return "never runs"
}

func TestTheDeclaredTypesAreTheOnesSQLiteRecords(t *testing.T) {
	c := openFile(t)

	columns := []ir.Column{
		{Name: "a_string", Type: mustType(t, "string(255)"), Nullable: true},
		{Name: "a_text", Type: mustType(t, "text"), Nullable: true},
		{Name: "a_bool", Type: mustType(t, "bool"), Nullable: true},
		{Name: "an_int16", Type: mustType(t, "int16"), Nullable: true},
		{Name: "an_int64", Type: mustType(t, "int64"), Nullable: true},
		{Name: "a_float32", Type: mustType(t, "float32"), Nullable: true},
		{Name: "a_decimal", Type: mustType(t, "decimal(19,4)"), Nullable: true},
		{Name: "a_uuid", Type: mustType(t, "uuid"), Nullable: true},
		{Name: "a_json", Type: mustType(t, "json"), Nullable: true},
		{Name: "a_timestamptz", Type: mustType(t, "timestamptz"), Nullable: true},
		{Name: "a_date", Type: mustType(t, "date"), Nullable: true},
		{Name: "a_binary", Type: mustType(t, "binary"), Nullable: true},
		{Name: "an_enum", Type: mustType(t, "enum(draft,live)"), Nullable: true},
	}

	apply(t, c, ir.CreateTable{Table: "every_type", Columns: columns})

	recorded := map[string]string{
		"a_string": "varchar(255)", "a_text": "TEXT", "a_bool": "boolean",
		"an_int16": "INTEGER", "an_int64": "INTEGER", "a_float32": "REAL",
		"a_decimal": "decimal(19,4)", "a_uuid": "uuid", "a_json": "TEXT",
		"a_timestamptz": "timestamptz", "a_date": "date", "a_binary": "BLOB", "an_enum": "TEXT",
	}

	for _, row := range read(t, c, `SELECT name, type FROM pragma_table_info('every_type')`).Values {
		name, declared := *row[0], *row[1]
		if recorded[name] != declared {
			t.Errorf("%s is recorded as %q, want %q", name, declared, recorded[name])
		}
	}

	t.Log("SQLite uppercases the four bare storage-class keywords -- integer, text, real, blob --")
	t.Log("and records everything else exactly as written, measured on 3.53.4. So a declared name")
	t.Log("is what M11's introspection reads back, which is why this driver preserves the logical")
	t.Log("type where affinity allows it rather than collapsing eight types onto TEXT.")
}

func TestAnIntegerPrimaryKeyIsTheRowidAlias(t *testing.T) {
	c := openFile(t)

	apply(t, c, ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
		{Name: "email", Type: mustType(t, "string(255)")},
	}, PrimaryKey: []string{"id"}})

	if _, err := c.Perform(t.Context(), driver.Query{
		SQL: `INSERT INTO "users" ("email") VALUES ('nobody@example.com')`,
	}); err != nil {
		t.Fatalf("inserting without a key: %v", err)
	}

	if got := value(t, read(t, c, `SELECT "id" FROM "users"`)); got != "1" {
		t.Errorf("the key was assigned %q, want 1", got)
	}

	t.Log("int64 maps to integer rather than bigint, and this is why: INTEGER PRIMARY KEY is the")
	t.Log("rowid alias and no other spelling is. A bigint primary key would be an ordinary indexed")
	t.Log("column that assigns nothing and accepts text.")
}

func TestADecimalColumnDoesNotHoldTheDigitsItWasGiven(t *testing.T) {
	c := openFile(t)

	apply(t, c, ir.CreateTable{Table: "money", Columns: []ir.Column{
		{Name: "exact", Type: mustType(t, "decimal(19,4)"), Nullable: true},
		{Name: "as_text", Type: mustType(t, "text"), Nullable: true},
	}})

	if _, err := c.Perform(t.Context(), driver.Query{
		SQL: `INSERT INTO "money" ("exact", "as_text") VALUES ('1.10', '1.10')`,
	}); err != nil {
		t.Fatalf("inserting: %v", err)
	}

	rows := read(t, c, `SELECT "exact", typeof("exact"), "as_text" FROM "money"`)
	exact, class, asText := *rows.Values[0][0], *rows.Values[0][1], *rows.Values[0][2]

	if exact == "1.10" || class != "real" {
		t.Errorf("a decimal column stored %q as %s", exact, class)
		t.Log("If this starts passing, SQLite's NUMERIC affinity has changed and the warning in")
		t.Log("doc.go is now wrong -- which is the point of measuring it rather than citing it.")
	}

	if asText != "1.10" {
		t.Errorf("a text column stored %q, so the comparison proves nothing", asText)
	}

	t.Log("Measured on 3.53.4: NUMERIC affinity converted '1.10' to the REAL 1.1. decimal(p,s) is")
	t.Log("declared because it is what every SQLite consumer expects to read back, and exactness")
	t.Log("is not preserved -- int64 of minor units is the schema that keeps its cents.")
}

func TestAnEnumColumnRefusesAValueThatIsNotALabel(t *testing.T) {
	c := openFile(t)

	apply(t, c, ir.CreateTable{Table: "posts", Columns: []ir.Column{
		{Name: "status", Type: mustType(t, "enum(draft,live)"), Default: ir.Literal("draft")},
	}})

	if _, err := c.Perform(t.Context(), driver.Query{
		SQL: `INSERT INTO "posts" ("status") VALUES ('live')`,
	}); err != nil {
		t.Fatalf("a label was refused: %v", err)
	}

	_, err := c.Perform(t.Context(), driver.Query{
		SQL: `INSERT INTO "posts" ("status") VALUES ('deleted')`,
	})
	if err == nil {
		t.Fatal("a value that is not a label was accepted")
	}

	if !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("the value was refused by something other than the check: %v", err)
	}
}

func TestAUniqueIndexEnforcesWhatTheConstraintPromised(t *testing.T) {
	c := openFile(t)

	apply(t, c, ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "email", Type: mustType(t, "string(255)")},
	}})
	apply(t, c, ir.AddUnique{Table: "users", Name: "uq_users_email", Columns: []string{"email"}})

	insert := driver.Query{SQL: `INSERT INTO "users" ("email") VALUES ('one@example.com')`}

	if _, err := c.Perform(t.Context(), insert); err != nil {
		t.Fatalf("the first row was refused: %v", err)
	}

	if _, err := c.Perform(t.Context(), insert); err == nil {
		t.Error("a duplicate was accepted, so add_unique enforced nothing")
	}

	apply(t, c, ir.DropUnique{Table: "users", Name: "uq_users_email"})

	if _, err := c.Perform(t.Context(), insert); err != nil {
		t.Errorf("the duplicate was still refused after drop_unique: %v", err)
	}
}

func TestReplacingAViewLeavesTheNewDefinition(t *testing.T) {
	c := openFile(t)

	apply(t, c, ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
		{Name: "email", Type: mustType(t, "string(255)")},
	}})
	apply(t, c, ir.CreateView{View: "listing", Query: `SELECT "id" FROM "users"`})
	apply(t, c, ir.CreateView{View: "listing", OrReplace: true,
		Query: `SELECT "id", "email" FROM "users"`})

	definition := value(t, read(t, c,
		`SELECT sql FROM sqlite_master WHERE type = 'view' AND name = 'listing'`))

	if !strings.Contains(definition, `"email"`) {
		t.Errorf("the view was not replaced: %s", definition)
	}

	if err := c.Exec(t.Context(), driver.Statement{SQL: `DROP VIEW IF EXISTS "users"`}); err == nil {
		t.Error("DROP VIEW on a name held by a table was accepted")
		t.Log("The IF EXISTS is what makes replacing a new view work, and this is the case that")
		t.Log("keeps it from being a silent way to do nothing: SQLite refuses it loudly.")
	}
}

func TestTheAddColumnRestrictionsAreAboutTheRowsRatherThanTheStatement(t *testing.T) {
	columns := map[string]ir.Column{
		"not null with no default": {Name: "email", Type: mustType(t, "string(255)")},
		"a function default": {Name: "seen_at", Type: mustType(t, "timestamptz"),
			Nullable: true, Default: ir.Call(ir.FuncCurrentTimestamp)},
	}

	for name, column := range columns {
		t.Run(name, func(t *testing.T) {
			empty, populated := openFile(t), openFile(t)

			for _, c := range []*sqliteConn{empty, populated} {
				apply(t, c, ir.CreateTable{Table: "users", Columns: []ir.Column{
					{Name: "id", Type: mustType(t, "int64")},
				}})
			}

			if _, err := populated.Perform(t.Context(), driver.Query{
				SQL: `INSERT INTO "users" ("id") VALUES (1)`,
			}); err != nil {
				t.Fatalf("seeding a row: %v", err)
			}

			add := ir.AddColumn{Table: "users", Column: column}
			apply(t, empty, add)

			statement, err := compile(add)
			if err != nil {
				t.Fatalf("compiling: %v", err)
			}

			if err := populated.Exec(t.Context(), statement[0]); err == nil {
				t.Error("the same statement was accepted against a table that has rows")
			}
		})
	}

	t.Log("This is why neither refusal is a payload refusal: the identical statement succeeds")
	t.Log("against an empty table and fails against one with a row in it. Compile is pure by")
	t.Log("contract and a capability Rule sees the operation, so neither can tell them apart --")
	t.Log("and the engine's refusal is loud, inside the transaction, and rolls the migration back.")
}

func TestAColumnAnIndexReferencesCannotBeDropped(t *testing.T) {
	c := openFile(t)

	apply(t, c, ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
		{Name: "email", Type: mustType(t, "string(255)")},
	}})
	apply(t, c, ir.AddIndex{Table: "users", Name: "idx_users_email", Columns: []string{"email"}})

	drop, err := compile(ir.DropColumn{Table: "users", Column: "email"})
	if err != nil {
		t.Fatalf("compiling drop_column: %v", err)
	}

	if err := c.Exec(t.Context(), drop[0]); err == nil {
		t.Fatal("an indexed column was dropped")
	}

	apply(t, c, ir.DropIndex{Table: "users", Name: "idx_users_email"})
	apply(t, c, ir.DropColumn{Table: "users", Column: "email"})

	t.Log("Compile is pure by contract -- no context, no connection -- so it cannot know that an")
	t.Log("index references this column, and native DROP COLUMN is what it emits. SQLite refuses")
	t.Log("the same for a primary key, a unique, or a column a check, view or foreign key names.")
	t.Log("Covering those is the rebuild, which is M8-T3's verdict and M8-T4's implementation.")
}

func TestTheSequenceLeavesNothingBehind(t *testing.T) {
	c := openFile(t)

	for _, op := range migration(t) {
		apply(t, c, op)
	}

	rows := read(t, c, `SELECT name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	if len(rows.Values) != 0 {
		names := make([]string, len(rows.Values))
		for i, row := range rows.Values {
			names[i] = *row[0]
		}

		slices.Sort(names)
		t.Errorf("the sequence left %s behind", strings.Join(names, ", "))
	}
}
