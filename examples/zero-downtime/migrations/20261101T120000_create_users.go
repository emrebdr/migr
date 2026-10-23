package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(CreateUsers{}) }

// CreateUsers adds the users table the app is serving from before the change begins.
type CreateUsers struct{}

// Up creates the table with its key inline.
func (CreateUsers) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("users").
		Column("id", migr.Int64()).
		Column("name", migr.String(100)).
		Column("email", migr.String(255)).
		PrimaryKey("id").
		Apply(ctx)
}
