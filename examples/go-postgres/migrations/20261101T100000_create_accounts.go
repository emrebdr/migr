package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(CreateAccounts{}) }

// CreateAccounts adds the accounts table, whose down migr derives by inverting the up.
type CreateAccounts struct{}

// Up creates the table with its key inline.
func (CreateAccounts) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("accounts").
		Column("id", migr.Int64()).
		Column("email", migr.String(255)).
		Column("created_at", migr.TimestampTZ()).Default(migr.Call(migr.FuncCurrentTimestamp)).
		PrimaryKey("id").
		Apply(ctx)
}
