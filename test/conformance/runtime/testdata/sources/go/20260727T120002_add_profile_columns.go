package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddProfileColumns{}) }

// AddProfileColumns is the fixture for add_column, drop_column, and alter_column.
type AddProfileColumns struct{}

// Dependencies declares the migration that creates the table this one changes.
func (m AddProfileColumns) Dependencies() []string {
	return []string{"20260727T120001"}
}

// Up adds two columns and widens a third, which is the one change that may lose data.
func (m AddProfileColumns) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("users").
		AddColumn("bio", migr.Text()).Null().
		AddColumn("tier", migr.String(16)).Default(migr.Literal("free")).
		AlterColumn("email").Type(migr.String(320)).WasType(migr.String(255)).
		Apply(ctx)
}

// Down narrows the column back and drops the two, recording what each one was.
func (m AddProfileColumns) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("users").
		AlterColumn("email").Type(migr.String(255)).WasType(migr.String(320)).
		DropColumn("tier").Was(migr.Column{
		Name: "tier", Type: migr.String(16), Default: migr.Literal("free"),
	}).
		DropColumn("bio").Was(migr.Column{Name: "bio", Type: migr.Text(), Nullable: true}).
		Apply(ctx)
}
