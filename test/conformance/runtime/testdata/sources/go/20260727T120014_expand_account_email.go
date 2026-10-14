package migrations

import migr "github.com/emrebdr/migr/sdk/go"

// init registers the expand of widening accounts.email: the column it is rebuilt in, added nullable.
func init() {
	migr.Register(migr.ReplaceColumn("accounts", "email", migr.String(255)).With(migr.String(320)).Expand())
}
