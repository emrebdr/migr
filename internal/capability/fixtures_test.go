package capability

import (
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// mustType parses a logical type, failing the test if the spelling is wrong.
func mustType(t *testing.T, s string) schema.Type {
	t.Helper()

	typ, err := schema.Parse(s)
	if err != nil {
		t.Fatalf("schema.Parse(%q): %v", s, err)
	}

	return typ
}

// intType returns the logical type the add_column fixtures use.
func intType(t *testing.T) schema.Type {
	t.Helper()

	return mustType(t, "int32")
}

// everyOperation returns one instance of each operation in the v1 set, in declaration order.
func everyOperation(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.CreateTable{
			Table:      "users",
			Columns:    []ir.Column{{Name: "id", Type: mustType(t, "uuid")}},
			PrimaryKey: []string{"id"},
		},
		ir.DropTable{Table: "users"},
		ir.RenameTable{From: "users", To: "accounts"},
		ir.AddColumn{Table: "users", Column: column(t, nil)},
		ir.DropColumn{Table: "users", Column: "attempts"},
		ir.RenameColumn{Table: "users", From: "email", To: "email_address"},
		ir.AlterColumn{Table: "users", Column: "attempts", Nullable: ptrBool(true)},
		ir.AddIndex{Table: "users", Name: "idx_users_email", Columns: []string{"email"}},
		ir.DropIndex{Table: "users", Name: "idx_users_email"},
		ir.AddForeignKey{
			Table:             "orders",
			Name:              "fk_orders_user",
			Columns:           []string{"user_id"},
			ReferencesTable:   "users",
			ReferencesColumns: []string{"id"},
		},
		ir.DropForeignKey{Table: "orders", Name: "fk_orders_user"},
		ir.AddUnique{Table: "users", Name: "uq_users_email", Columns: []string{"email"}},
		ir.DropUnique{Table: "users", Name: "uq_users_email"},
		ir.AddCheck{Table: "orders", Name: "ck_orders_total", Expression: "total >= 0"},
		ir.DropCheck{Table: "orders", Name: "ck_orders_total"},
		ir.AddPrimaryKey{Table: "legacy", Name: "pk_legacy", Columns: []string{"id"}},
		ir.DropPrimaryKey{Table: "legacy", Name: "pk_legacy"},
		ir.CreateView{View: "active_users", Query: "SELECT id FROM users WHERE active"},
		ir.DropView{View: "active_users"},
		ir.Raw{Driver: "postgres", SQL: "CREATE EXTENSION IF NOT EXISTS pgcrypto"},
		ir.DataCopy{From: "users", To: "users_new", Columns: []ir.CopyColumn{
			{From: "id", To: "id"}}},
		ir.DataBackfill{Table: "users", Column: "plan", Type: mustType(t, "string(16)"),
			Value: ir.Literal("free")},
		ir.DataTransform{Table: "users", Column: "email_key", Type: mustType(t, "string(255)"),
			From: "email", Function: ir.TransformLower},
	}
}

// column returns a valid column carrying an optional default, for the add_column fixtures.
func column(t *testing.T, def *ir.Default) ir.Column {
	t.Helper()

	return ir.Column{Name: "attempts", Type: intType(t), Default: def}
}

// ptrBool returns a pointer to a bool, for the operations whose fields are tri-state.
func ptrBool(v bool) *bool {
	return &v
}

func TestTheFixtureCoversEveryOperation(t *testing.T) {
	seen := make(map[ir.Kind]bool, len(ir.Kinds()))
	for _, op := range everyOperation(t) {
		if seen[op.Kind()] {
			t.Errorf("the fixture holds %s twice", op.Kind())
		}

		seen[op.Kind()] = true
	}

	for _, kind := range ir.Kinds() {
		if !seen[kind] {
			t.Errorf("the fixture has no %s, so no test in this package walks it", kind)
		}
	}

	for _, op := range everyOperation(t) {
		if err := op.Validate(); err != nil {
			t.Errorf("the %s fixture is not a well-formed operation: %v", op.Kind(), err)
		}
	}
}
