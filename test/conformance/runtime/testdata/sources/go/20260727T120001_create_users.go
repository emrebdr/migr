// Package migrations is the conformance fixture set expressed in Go.
package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(CreateUsers{}) }

// CreateUsers is the fixture for create_table and drop_table.
type CreateUsers struct{}

// Up creates the table the rest of the fixture set builds on.
func (m CreateUsers) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("users").
		Column("id", migr.UUID()).
		Column("email", migr.String(255)).
		Column("created_at", migr.TimestampTZ()).Null().
		Default(migr.Call(migr.FuncCurrentTimestamp)).
		PrimaryKey("id").
		Apply(ctx)
}

// Down drops it, recording what it held so that the drop inverts exactly.
func (m CreateUsers) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("users").Drop().Was(migr.TableDef{
		Columns: []migr.Column{
			{Name: "id", Type: migr.UUID()},
			{Name: "email", Type: migr.String(255)},
			{
				Name: "created_at", Type: migr.TimestampTZ(),
				Nullable: true, Default: migr.Call(migr.FuncCurrentTimestamp),
			},
		},
		PrimaryKey: []string{"id"},
	}).Apply(ctx)
}
