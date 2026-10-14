package drivertest

import (
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// rawFixture is a SQL comment rather than a statement, so no engine SQL is authored above a
// driver package (CLAUDE.md invariant #3) and no golden file can be mistaken for real DDL.
const rawFixture = "-- a raw statement, passed through verbatim"

// Operations returns one representative operation per kind, in the order the IR declares them.
func Operations(driver string) ir.Operations {
	ops := ir.Operations{
		ir.CreateTable{
			Table: "users",
			Columns: []ir.Column{
				{Name: "id", Type: typeOf("uuid")},
				{Name: "email", Type: typeOf("string(255)")},
				{
					Name:     "created_at",
					Type:     typeOf("timestamptz"),
					Nullable: true,
					Default:  ir.Call(ir.FuncCurrentTimestamp),
				},
			},
			PrimaryKey: []string{"id"},
		},
		ir.DropTable{Table: "users"},
		ir.RenameTable{From: "users", To: "accounts"},
		ir.AddColumn{Table: "users", Column: ir.Column{
			Name:     "nickname",
			Type:     typeOf("string(64)"),
			Nullable: true,
			Default:  ir.Literal("anonymous"),
		}},
		ir.DropColumn{Table: "users", Column: "nickname"},
		ir.RenameColumn{Table: "users", From: "email", To: "email_address"},
		ir.AlterColumn{
			Table:    "users",
			Column:   "email",
			Type:     pointerTo(typeOf("string(512)")),
			Nullable: pointerTo(true),
		},
		ir.AddIndex{Table: "users", Name: "idx_users_email", Columns: []string{"email"}},
		ir.DropIndex{Table: "users", Name: "idx_users_email"},
		ir.AddForeignKey{
			Table:             "orders",
			Name:              "fk_orders_user",
			Columns:           []string{"user_id"},
			ReferencesTable:   "users",
			ReferencesColumns: []string{"id"},
			OnDelete:          ir.ActionCascade,
			OnUpdate:          ir.ActionRestrict,
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
		ir.Raw{Driver: driver, SQL: rawFixture},
		ir.DataCopy{From: "users", To: "users_archive", Columns: []ir.CopyColumn{
			{From: "id", To: "id"}, {From: "email", To: "email_address"},
		}},
		ir.DataBackfill{Table: "users", Column: "nickname", Type: typeOf("string(64)"),
			Value: ir.Literal("anonymous")},
		ir.DataTransform{Table: "users", Column: "email_key", Type: typeOf("string(255)"),
			From: "email", Function: ir.TransformLower},
	}

	return ops
}

// typeOf returns a logical type by its canonical spelling, or the zero Type if it is misspelled.
func typeOf(spelling string) schema.Type {
	typ, _ := schema.Parse(spelling)

	return typ
}

// pointerTo returns a pointer to a value, for the operation fields that are tri-state.
func pointerTo[T any](v T) *T {
	return &v
}
