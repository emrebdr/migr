package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(BackfillTiers{}) }

// BackfillTiers is the fixture that reads, branches on what it read, and reaches the database itself.
type BackfillTiers struct{}

// Up counts two things, fills one in, and emits an operation shaped by both answers.
func (m BackfillTiers) Up(ctx context.Context, s *migr.Schema) error {
	tiers, err := s.Query(ctx, "SELECT count(*) FROM people WHERE tier IS NULL")
	if err != nil {
		return err
	}

	notes, err := s.Query(ctx, "SELECT count(*) FROM people WHERE note IS NULL")
	if err != nil {
		return err
	}

	if _, err := s.Exec(ctx, "UPDATE people SET tier = 'free' WHERE tier IS NULL"); err != nil {
		return err
	}

	if err := s.Raw("postgres", "ANALYZE people").Apply(ctx); err != nil {
		return err
	}

	people := s.Table("people").AddColumn("tier_backfilled", migr.Bool())

	if counted(tiers) {
		people.Default(migr.Literal("true"))
	}

	if counted(notes) {
		people.Null()
	}

	return people.Apply(ctx)
}

// Down removes the column and undoes the raw statement, which nothing could have derived.
func (m BackfillTiers) Down(ctx context.Context, s *migr.Schema) error {
	if err := s.Table("people").
		DropColumn("tier_backfilled").Was(migr.Column{
		Name: "tier_backfilled", Type: migr.Bool(), Default: migr.Literal("true"),
	}).
		Apply(ctx); err != nil {
		return err
	}

	return s.Raw("postgres", "ANALYZE people").Apply(ctx)
}

// counted reports whether a read found anything, which is the branch each pinned answer decides.
func counted(rows migr.Rows) bool {
	if rows.Len() != 1 || len(rows.Values[0]) != 1 || rows.Values[0][0] == nil {
		return false
	}

	return *rows.Values[0][0] != "0"
}
