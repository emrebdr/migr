package migrations

import migr "github.com/emrebdr/migr/sdk/go"

// init registers the backfill of widening accounts.email, whose down has nothing of its own to undo.
func init() {
	migr.Register(migr.ReplaceColumn("accounts", "email", migr.String(255)).With(migr.String(320)).Backfill())
}
