package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(DropLegacy{}) }

// DropLegacy is the fixture whose author states it cannot be undone, and says why.
type DropLegacy struct{}

// Up drops a table without recording what it held, which is what makes it one-way.
func (m DropLegacy) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("legacy").Drop().Apply(ctx)
}

// Irreversible returns the reason a rollback halts with, which is the author's own.
func (m DropLegacy) Irreversible() string {
	return "the rows in legacy are not recorded anywhere; restore from a backup"
}
