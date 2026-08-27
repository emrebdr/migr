package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(ConstrainOrders{}) }

// ConstrainOrders is the fixture for add_check, drop_check, add_foreign_key, and drop_foreign_key.
type ConstrainOrders struct{}

// Up constrains a column and references another table, which is a rule rather than a row.
func (m ConstrainOrders) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("orders").
		AddCheck("ck_orders_total", "total >= 0").
		AddForeignKey("fk_orders_person", []string{"person_id"}, "people", []string{"id"}).
		OnDelete(migr.ActionCascade).OnUpdate(migr.ActionRestrict).
		Apply(ctx)
}

// Down removes both, recording what each one said.
func (m ConstrainOrders) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("orders").
		DropForeignKey("fk_orders_person").Was(migr.ForeignKeyDef{
		Columns:         []string{"person_id"},
		ReferencesTable: "people", ReferencesColumns: []string{"id"},
		OnDelete: migr.ActionCascade, OnUpdate: migr.ActionRestrict,
	}).
		DropCheck("ck_orders_total").Was(migr.CheckDef{Expression: "total >= 0"}).
		Apply(ctx)
}
