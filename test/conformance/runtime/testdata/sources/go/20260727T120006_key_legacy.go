package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(KeyLegacy{}) }

// KeyLegacy is the fixture for add_primary_key and drop_primary_key, which key a table that exists.
type KeyLegacy struct{}

// Up keys a table that already exists, which is the spelling a new table does not use.
func (m KeyLegacy) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("legacy").
		AddPrimaryKey("pk_legacy", "id").
		Apply(ctx)
}

// Down removes the key, recording which columns it covered.
func (m KeyLegacy) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("legacy").
		DropPrimaryKey("pk_legacy").Was(migr.Columns("id")).
		Apply(ctx)
}
