package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(IndexPeople{}) }

// IndexPeople is the fixture for add_index, drop_index, add_unique, and drop_unique.
type IndexPeople struct{}

// Up indexes one column and constrains another, which are one schema fact each.
func (m IndexPeople) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").
		AddIndex("idx_people_tier", "tier").
		AddUnique("uq_people_address", "address").
		Apply(ctx)
}

// Down removes both, recording which columns each covered.
func (m IndexPeople) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").
		DropUnique("uq_people_address").Was(migr.Columns("address")).
		DropIndex("idx_people_tier").Was(migr.Columns("tier")).
		Apply(ctx)
}
