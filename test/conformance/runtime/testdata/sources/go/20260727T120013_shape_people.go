package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(ShapePeople{}) }

// ShapePeople is the fixture that writes rows rather than schema, which irVersion 2 added.
type ShapePeople struct{}

// Up fills, rewrites and copies, through one chain per table.
func (m ShapePeople) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").
		Backfill("nickname", migr.String(64), migr.Literal("anonymous")).
		Transform("email_key", migr.String(255), "email", migr.TransformLower).
		CopyInto("people_archive",
			migr.CopyColumn{From: "id", To: "id"}, migr.CopyColumn{From: "email", To: "email"}).
		Apply(ctx)
}

// Irreversible returns the reason a rollback halts with, since no data operation has an inverse.
func (m ShapePeople) Irreversible() string {
	return "every operation here writes rows, and what they replaced is recorded nowhere"
}
