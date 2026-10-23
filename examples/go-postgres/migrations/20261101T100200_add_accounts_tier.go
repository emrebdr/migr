package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddTier{}) }

// AddTier adds a tier, defaulting it to free only where accounts already exist.
type AddTier struct{}

// Up reads the table and decides what to emit from the answer.
func (AddTier) Up(ctx context.Context, s *migr.Schema) error {
	rows, err := s.Query(ctx, "SELECT count(*) FROM accounts")
	if err != nil {
		return err
	}

	tier := s.Table("accounts").AddColumn("tier", migr.String(16))
	if *rows.Values[0][0] != "0" {
		tier = tier.Default(migr.Literal("free"))
	}

	return tier.Apply(ctx)
}

// Irreversible says why no down is offered.
func (AddTier) Irreversible() string {
	return "the tiers written after it ran are not recorded anywhere a rollback could restore them from"
}
