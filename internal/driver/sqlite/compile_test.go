package sqlite

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// statements compiles an operation that is expected to succeed, and returns each statement's text.
func statements(t *testing.T, op ir.Operation) []string {
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

// only compiles an operation expected to produce exactly one statement, and returns its text.
func only(t *testing.T, op ir.Operation) string {
	t.Helper()

	texts := statements(t, op)
	if len(texts) != 1 {
		t.Fatalf("%s compiled to %d statements, want 1", op.Kind(), len(texts))
	}

	return texts[0]
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

func TestAUniqueConstraintIsAUniqueIndex(t *testing.T) {
	added := only(t, ir.AddUnique{
		Table: "users", Name: "uq_users_email", Columns: []string{"email", "tenant"},
	})

	want := `CREATE UNIQUE INDEX "uq_users_email" ON "users" ("email", "tenant");`
	if added != want {
		t.Errorf("add_unique compiled to\n  %s\nwant\n  %s", added, want)
	}

	dropped := only(t, ir.DropUnique{Table: "users", Name: "uq_users_email"})
	if dropped != `DROP INDEX "uq_users_email";` {
		t.Errorf("drop_unique compiled to %s", dropped)
	}

	t.Log("A UNIQUE constraint is a unique index in SQLite, and adding one to an existing table")
	t.Log("any other way means a rebuild. The IR gives create_table no way to declare a unique")
	t.Log("inline, so every unique in a migr-managed database came from add_unique -- which is")
	t.Log("what makes DROP INDEX always find it.")
}

func TestAnIndexIsDroppedByNameWithNoTable(t *testing.T) {
	dropped := only(t, ir.DropIndex{Table: "accounts", Name: "idx_email"})

	if strings.Contains(dropped, "accounts") {
		t.Errorf("drop_index named the table: %s", dropped)
		t.Log("SQLite indexes are database-scoped, as PostgreSQL's are schema-scoped. The IR")
		t.Log("carries the table so a reversal can rebuild the index; the statement does not.")
	}
}

func TestReplacingAViewIsTwoStatementsInOrder(t *testing.T) {
	replaced := statements(t, ir.CreateView{
		View: "active_users", Query: "SELECT id FROM users WHERE active", OrReplace: true,
	})

	want := []string{
		`DROP VIEW IF EXISTS "active_users";`,
		`CREATE VIEW "active_users" AS SELECT id FROM users WHERE active;`,
	}

	if !slices.Equal(replaced, want) {
		t.Errorf("or_replace compiled to\n  %v\nwant\n  %v", replaced, want)
	}

	t.Log("SQLite has no CREATE OR REPLACE VIEW. The drop is IF EXISTS so a new view is not a")
	t.Log("failure, and DROP VIEW on a name held by a table is refused by SQLite rather than")
	t.Log("silently doing something else. Both statements run inside the migration's one")
	t.Log("transaction, so the replacement is atomic -- SQLite's DDL is transactional.")
}

func TestAViewCreatedWithoutOrReplaceDoesNotDropAnything(t *testing.T) {
	created := statements(t, ir.CreateView{View: "active_users", Query: "SELECT 1"})

	if len(created) != 1 {
		t.Fatalf("create_view compiled to %d statements, want 1: %v", len(created), created)
	}

	if strings.Contains(created[0], "DROP") {
		t.Errorf("a plain create_view drops something: %s", created[0])
	}
}

func TestAnEnumColumnCarriesItsCheckIntoBothStatementsThatDeclareOne(t *testing.T) {
	status := ir.Column{Name: "status", Type: mustType(t, "enum(draft,live)"),
		Default: ir.Literal("draft")}

	created := only(t, ir.CreateTable{Table: "posts", Columns: []ir.Column{status}})
	added := only(t, ir.AddColumn{Table: "posts", Column: status})

	for _, statement := range []string{created, added} {
		if !strings.Contains(statement, `CHECK ("status" IN ('draft', 'live'))`) {
			t.Errorf("an enum column lost its check: %s", statement)
		}
	}
}

func TestAddingAColumnIsCompiledAndLeftToTheEngineToRefuse(t *testing.T) {
	for name, column := range map[string]ir.Column{
		"not null with no default": {Name: "email", Type: mustType(t, "string(255)")},
		"a function default": {Name: "seen_at", Type: mustType(t, "timestamptz"),
			Nullable: true, Default: ir.Call(ir.FuncCurrentTimestamp)},
	} {
		t.Run(name, func(t *testing.T) {
			if statement := only(t, ir.AddColumn{Table: "users", Column: column}); statement == "" {
				t.Error("nothing was compiled")
			}
		})
	}

	t.Log("SQLite refuses both of these -- \"Cannot add a NOT NULL column with default value NULL\"")
	t.Log("and \"Cannot add a column with non-constant default\" -- but only when the table already")
	t.Log("has rows. Measured on 3.53.4: against an empty table both are accepted and both are")
	t.Log("what the author asked for. So the condition is on the data rather than on the payload,")
	t.Log("which is neither something Compile can see nor something a capability Rule can. The")
	t.Log("engine's own refusal is loud and inside the transaction, which is the right place.")
}

func TestACreatedColumnRendersEverythingTheOperationDeclared(t *testing.T) {
	columns := []ir.Column{
		{Name: "email", Type: mustType(t, "string(255)")},
		{Name: "seen_at", Type: mustType(t, "timestamptz"), Nullable: true,
			Default: ir.Call(ir.FuncCurrentTimestamp)},
	}

	statement := only(t, ir.CreateTable{Table: "users", Columns: columns})

	for _, fragment := range []string{`"email" varchar(255) NOT NULL`,
		`"seen_at" timestamptz DEFAULT CURRENT_TIMESTAMP`} {
		if !strings.Contains(statement, fragment) {
			t.Errorf("create_table did not render %s:\n%s", fragment, statement)
		}
	}
}

func TestAnIdentifierIsQuotedWithItsOwnQuotesDoubled(t *testing.T) {
	statement := only(t, ir.DropTable{Table: `we"ird`})

	if statement != `DROP TABLE "we""ird";` {
		t.Errorf("a quoted identifier compiled to %s", statement)
	}
}

func TestALiteralDefaultIsWrittenInWithItsOwnQuotesDoubled(t *testing.T) {
	statement := only(t, ir.AddColumn{Table: "users", Column: ir.Column{
		Name: "motto", Type: mustType(t, "text"), Nullable: true, Default: ir.Literal("it's fine"),
	}})

	if !strings.Contains(statement, `DEFAULT 'it''s fine'`) {
		t.Errorf("a literal default compiled to %s", statement)
	}
}

func TestANULIsRefusedWhereverItWouldEndTheStatement(t *testing.T) {
	for name, op := range map[string]ir.Operation{
		"a table name":  ir.DropTable{Table: "users\x00"},
		"a column name": ir.DropColumn{Table: "users", Column: "email\x00"},
		"a literal default": ir.AddColumn{Table: "users", Column: ir.Column{
			Name: "motto", Type: mustType(t, "text"), Nullable: true, Default: ir.Literal("a\x00b"),
		}},
		"a view query": ir.CreateView{View: "v", Query: "SELECT 1\x00, 2"},
		"raw SQL":      ir.Raw{Driver: Name, SQL: "VACUUM\x00"},
	} {
		t.Run(name, func(t *testing.T) {
			if reason := refusalFor(t, op).Reason; !strings.Contains(reason, "C string") {
				t.Errorf("the refusal does not say why a NUL matters: %s", reason)
			}
		})
	}
}

func TestRawIsPassedThroughOnlyForThisEngine(t *testing.T) {
	statement := only(t, ir.Raw{Driver: Name, SQL: "VACUUM"})

	if statement != "VACUUM" {
		t.Errorf("raw SQL compiled to %q, and run verbatim is the whole contract", statement)
	}

	elsewhere := refusalFor(t, ir.Raw{Driver: "postgres", SQL: "VACUUM FULL"})
	if !strings.Contains(elsewhere.Reason, "postgres") {
		t.Errorf("the refusal does not name the engine it is addressed to: %s", elsewhere.Reason)
	}

	if refusalFor(t, ir.Raw{Driver: Name, SQL: "  "}).Reason == "" {
		t.Error("raw SQL that runs nothing was accepted")
	}
}

func TestNothingThisDriverCompilesLeavesTheTransaction(t *testing.T) {
	for _, op := range compilable(t) {
		stmts, err := compile(op)
		if err != nil {
			t.Fatalf("%s: %v", op.Kind(), err)
		}

		for i, stmt := range stmts {
			if stmt.NoTransaction {
				t.Errorf("%s statement %d declares it cannot run inside a transaction", op.Kind(), i+1)
			}
		}
	}

	t.Log("SQLite's DDL is transactional and this driver emits nothing concurrent, so a migration")
	t.Log("it compiles is always exactly one transactional run. The golden format's no-transaction")
	t.Log("header stays unexercised here, as it does for PostgreSQL.")
}

func TestEveryComposedStatementIsTerminatedAndRawIsNot(t *testing.T) {
	for _, op := range compilable(t) {
		for i, statement := range statements(t, op) {
			terminated := strings.HasSuffix(statement, terminator)

			if terminated == (op.Kind() == ir.KindRaw) {
				t.Errorf("%s statement %d ends %q", op.Kind(), i+1,
					statement[max(0, len(statement)-12):])
			}
		}
	}

	t.Log("A raw statement is run verbatim, and its author may already have written a semicolon.")
}

// compilable returns the representative operations this driver compiles, skipping those it refuses.
func compilable(t *testing.T) []ir.Operation {
	t.Helper()

	var ops []ir.Operation

	for _, kind := range ir.Kinds() {
		if !needsRebuild[kind] {
			ops = append(ops, representative(t, kind))
		}
	}

	return ops
}
