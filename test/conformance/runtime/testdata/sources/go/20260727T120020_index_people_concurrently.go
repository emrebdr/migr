package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(IndexPeopleConcurrently{}) }

// IndexPeopleConcurrently is the fixture for raw's no_transaction, which irVersion 3 carries.
type IndexPeopleConcurrently struct{}

// Up builds an index outside any transaction, then runs a statement that may share one.
func (m IndexPeopleConcurrently) Up(ctx context.Context, s *migr.Schema) error {
	return s.Raw("postgres", "CREATE INDEX CONCURRENTLY idx_people_nickname ON people (nickname)").
		NoTransaction().
		Raw("postgres", "ANALYZE people").
		Apply(ctx)
}

// Down drops the index outside any transaction, as it was built.
func (m IndexPeopleConcurrently) Down(ctx context.Context, s *migr.Schema) error {
	return s.Raw("postgres", "DROP INDEX CONCURRENTLY idx_people_nickname").NoTransaction().Apply(ctx)
}
