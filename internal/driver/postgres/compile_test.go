package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// sql compiles an operation that is expected to succeed, and returns each statement's text.
func sql(t *testing.T, op ir.Operation) []string {
	t.Helper()

	stmts, err := compile(op)
	if err != nil {
		t.Fatalf("%s: %v", op.Kind(), err)
	}

	texts := make([]string, len(stmts))
	for i, stmt := range stmts {
		texts[i] = stmt.SQL
	}

	return texts
}

// refusalFor compiles an operation that is expected to be refused, and returns the refusal.
func refusalFor(t *testing.T, op ir.Operation) *driver.UnsupportedError {
	t.Helper()

	stmts, err := compile(op)
	if err == nil {
		t.Fatalf("%s compiled to %d statements where a refusal was expected", op.Kind(), len(stmts))
	}

	var refusal *driver.UnsupportedError
	if !errors.As(err, &refusal) {
		t.Fatalf("%s failed with %v, which is not a refusal an operator can act on", op.Kind(), err)
	}

	return refusal
}

func TestAlterColumnEmitsItsChangesInAFixedOrder(t *testing.T) {
	nullable := false
	typ := mustType(t, "string(64)")

	statements := sql(t, ir.AlterColumn{
		Table:    "users",
		Column:   "status",
		Type:     &typ,
		Nullable: &nullable,
		Default:  ir.Literal("draft"),
	})

	want := []string{
		`ALTER TABLE "users" ALTER COLUMN "status" TYPE varchar(64);`,
		`ALTER TABLE "users" ALTER COLUMN "status" SET NOT NULL;`,
		`ALTER TABLE "users" ALTER COLUMN "status" SET DEFAULT 'draft';`,
	}

	if len(statements) != len(want) {
		t.Fatalf("got %d statements, want %d: %v", len(statements), len(want), statements)
	}

	for i, stmt := range statements {
		if stmt != want[i] {
			t.Errorf("statement %d is %s, want %s", i+1, stmt, want[i])
		}
	}

	t.Log("Type first is not cosmetic: SET NOT NULL against a column being retyped has to run")
	t.Log("after the retype, and a default has to be expressible in the new type. A fixed order")
	t.Log("is also what makes the golden file reproducible (invariant #8).")
}

func TestAlterColumnDroppingADefaultSaysSoExplicitly(t *testing.T) {
	statements := sql(t, ir.AlterColumn{Table: "users", Column: "status", DropDefault: true})

	if len(statements) != 1 || statements[0] != `ALTER TABLE "users" ALTER COLUMN "status" DROP DEFAULT;` {
		t.Errorf("compiled to %v", statements)
	}
}

func TestAlterColumnThatAsksForNothingIsRefused(t *testing.T) {
	refusalFor(t, ir.AlterColumn{Table: "users", Column: "status"})

	t.Log("Compile does not validate, so an operation requesting no change would compile to an")
	t.Log("empty statement list -- success, with the migration having run nothing.")
}

func TestAlterColumnThatBothSetsAndDropsADefaultIsRefused(t *testing.T) {
	refusalFor(t, ir.AlterColumn{
		Table:       "users",
		Column:      "status",
		Default:     ir.Literal("draft"),
		DropDefault: true,
	})

	t.Log("Emitting both would leave whichever this driver wrote last as the outcome, which is")
	t.Log("a coin flip decided by the order of two if statements.")
}

func TestAPrimaryKeyWithNoNameLetsPostgresDeriveOne(t *testing.T) {
	statements := sql(t, ir.AddPrimaryKey{Table: "legacy", Columns: []string{"id"}})

	if len(statements) != 1 || statements[0] != `ALTER TABLE "legacy" ADD PRIMARY KEY ("id");` {
		t.Errorf("compiled to %v", statements)
	}
}

func TestDroppingAPrimaryKeyWithNoNameIsRefused(t *testing.T) {
	refusal := refusalFor(t, ir.DropPrimaryKey{Table: "legacy"})

	if !strings.Contains(refusal.Reason, "DROP CONSTRAINT") {
		t.Errorf("the refusal %q does not say what PostgreSQL needs instead", refusal.Reason)
		t.Log("PostgreSQL has no name-free form, and the v1 IR leaves the name optional because")
		t.Log("MySQL has no name for a primary key. Guessing <table>_pkey would name a")
		t.Log("constraint the migration never declared.")
	}
}

func TestAForeignKeyOmitsAnActionItWasNotGiven(t *testing.T) {
	statements := sql(t, ir.AddForeignKey{
		Table:             "orders",
		Name:              "fk_orders_user",
		Columns:           []string{"user_id"},
		ReferencesTable:   "users",
		ReferencesColumns: []string{"id"},
		OnDelete:          ir.ActionCascade,
	})

	if strings.Contains(statements[0], "ON UPDATE") {
		t.Errorf("an unset action was rendered: %s", statements[0])
		t.Log("Omitting the clause leaves PostgreSQL's own behavior, which is what the IR means")
		t.Log("by an absent action. Rendering NO ACTION would state a decision nobody made.")
	}

	if !strings.HasSuffix(statements[0], "ON DELETE CASCADE;") {
		t.Errorf("the declared action is missing: %s", statements[0])
	}
}

func TestARawStatementAddressedToAnotherEngineIsRefused(t *testing.T) {
	refusal := refusalFor(t, ir.Raw{Driver: "mysql", SQL: "-- addressed elsewhere"})

	if !strings.Contains(refusal.Reason, "mysql") {
		t.Errorf("the refusal %q does not name the engine it was addressed to", refusal.Reason)
	}

	if refusal.Driver != Name || refusal.Kind != ir.KindRaw {
		t.Errorf("the refusal names %s/%s", refusal.Driver, refusal.Kind)
	}
}

func TestARawStatementForThisEngineIsPassedThroughUntouched(t *testing.T) {
	const verbatim = "CREATE EXTENSION IF NOT EXISTS pgcrypto"

	statements := sql(t, ir.Raw{Driver: Name, SQL: verbatim})

	if len(statements) != 1 || statements[0] != verbatim {
		t.Errorf("compiled to %v, want the SQL exactly as written and nothing appended", statements)
		t.Log("Every other operation ends with a semicolon this driver composes. Raw does not:")
		t.Log("run verbatim is the whole contract, and the author may already have written one.")
	}
}

func TestARawStatementCarryingNoSQLIsRefused(t *testing.T) {
	refusalFor(t, ir.Raw{Driver: Name, SQL: "   "})
}

func TestAViewQueryIsEmbeddedWithoutItsOwnTerminator(t *testing.T) {
	statements := sql(t, ir.CreateView{View: "active", Query: "SELECT id FROM users;  "})

	if statements[0] != `CREATE VIEW "active" AS SELECT id FROM users;` {
		t.Errorf("compiled to %s", statements[0])
		t.Log("A query already ending in a semicolon would otherwise produce two, and pgx sends")
		t.Log("this through the extended protocol, which rejects more than one command.")
	}
}

func TestReplacingAViewSaysSo(t *testing.T) {
	statements := sql(t, ir.CreateView{View: "active", Query: "SELECT 1", OrReplace: true})

	if !strings.HasPrefix(statements[0], "CREATE OR REPLACE VIEW ") {
		t.Errorf("compiled to %s", statements[0])
	}
}

func TestAnEnumColumnIsRefusedWhereverItAppears(t *testing.T) {
	enum := mustType(t, "enum(draft,live)")
	column := ir.Column{Name: "status", Type: enum}

	ops := []ir.Operation{
		ir.CreateTable{Table: "posts", Columns: []ir.Column{column}},
		ir.AddColumn{Table: "posts", Column: column},
		ir.AlterColumn{Table: "posts", Column: "status", Type: &enum},
	}

	for _, op := range ops {
		if refusal := refusalFor(t, op); !strings.Contains(refusal.Reason, enumBlocker) {
			t.Errorf("%s: the refusal %q does not say what supporting it would need",
				op.Kind(), refusal.Reason)
		}
	}

	t.Log("The refusal is per payload rather than per kind: create_table is supported, and this")
	t.Log("particular create_table is not. capability.Rule takes the whole operation for exactly")
	t.Log("this reason, so the declaration can say the same thing.")
}

func TestAnOverlongIdentifierIsRefusedWhereverItAppears(t *testing.T) {
	tooLong := strings.Repeat("a", maxIdentifier+1)

	ops := []ir.Operation{
		ir.DropTable{Table: tooLong},
		ir.RenameTable{From: "users", To: tooLong},
		ir.AddIndex{Table: "users", Name: tooLong, Columns: []string{"email"}},
		ir.AddIndex{Table: "users", Name: "idx", Columns: []string{tooLong}},
		ir.AddForeignKey{
			Table:             "orders",
			Name:              "fk",
			Columns:           []string{"user_id"},
			ReferencesTable:   tooLong,
			ReferencesColumns: []string{"id"},
		},
		ir.CreateView{View: tooLong, Query: "SELECT 1"},
		ir.DropIndex{Name: tooLong},
	}

	for _, op := range ops {
		refusalFor(t, op)
	}
}

func TestEmbeddedEngineSQLHoldingANulByteIsRefused(t *testing.T) {
	ops := []ir.Operation{
		ir.AddCheck{Table: "orders", Name: "ck", Expression: "total >= 0\x00 anything after this"},
		ir.CreateView{View: "active", Query: "SELECT 1\x00 anything after this"},
		ir.Raw{Driver: Name, SQL: "SELECT 1\x00 anything after this"},
	}

	for _, op := range ops {
		refusalFor(t, op)
	}

	t.Log("These three fields are engine SQL this driver embeds without reading. A NUL in one")
	t.Log("truncates the statement at the wire protocol, so what runs is a prefix of what was")
	t.Log("compiled -- and a golden file would record the whole of it.")
}

func TestAnUnknownOperationFailsRatherThanCompilingToNothing(t *testing.T) {
	if _, err := compile(nil); err == nil {
		t.Error("compiling no operation at all succeeded")
	}
}
