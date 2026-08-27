package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddNote{}) }

// AddNote is the fixture that authors no undo at all, which the engine derives one for.
type AddNote struct{}

// Up adds one column and writes no Down, so the engine is asked to invert what it emitted.
func (m AddNote) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("people").
		AddColumn("note", migr.Text()).Null().
		Apply(ctx)
}
