package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(IndexPeopleOnline{}) }

// IndexPeopleOnline is the fixture for the online declaration, which every SDK reports identically.
type IndexPeopleOnline struct{}

// Online declares the index is built through a shadow table rather than by a plain ALTER.
func (m IndexPeopleOnline) Online() migr.Strategy { return migr.ShadowTable }

// Up indexes one column.
func (m IndexPeopleOnline) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").AddIndex("idx_people_region", "region").Apply(ctx)
}

// Down removes it, recording which column it covered.
func (m IndexPeopleOnline) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").DropIndex("idx_people_region").Was(migr.Columns("region")).Apply(ctx)
}
