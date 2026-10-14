package migrations

import migr "github.com/emrebdr/migr/sdk/go"

// init registers the contract of widening accounts.email, whose down copies the old values back.
func init() {
	migr.Register(migr.ReplaceColumn("accounts", "email", migr.String(255)).With(migr.String(320)).Contract())
}
