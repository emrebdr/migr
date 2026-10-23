package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddNickname{}) }

// AddNickname adds an optional nickname and an index over it, and authors its own down.
type AddNickname struct{}

// Up adds the column and the index.
func (AddNickname) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("accounts").
		AddColumn("nickname", migr.String(64)).Null().
		AddIndex("idx_accounts_nickname", "nickname").
		Apply(ctx)
}

// Down drops both, recording what each was so the drop can itself be reversed.
func (AddNickname) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("accounts").
		DropIndex("idx_accounts_nickname").Was(migr.Columns("nickname")).
		DropColumn("nickname").Was(migr.Column{Name: "nickname", Type: migr.String(64), Nullable: true}).
		Apply(ctx)
}
