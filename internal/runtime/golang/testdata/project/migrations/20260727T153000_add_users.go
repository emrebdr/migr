// Package migrations is the fixture project's migrations, as a Go project writes them.
package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"

	"example.com/app/helpers"
)

func init() { migr.Register(AddUsers{}) }

// AddUsers is the one migration this fixture project carries.
type AddUsers struct{}

// Up emits the operation the adapter's tests read back off the wire.
func (m AddUsers) Up(ctx context.Context, s *migr.Schema) error {
	return s.Apply(ctx, migr.Raw{
		Driver: "postgres", SQL: "CREATE TABLE users (id bigint)" + helpers.Suffix,
	})
}
