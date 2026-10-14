package migrations

import migr "github.com/emrebdr/migr/sdk/go"

// init registers the swap of widening accounts.email: both columns change nullability, then names.
func init() {
	migr.Register(migr.ReplaceColumn("accounts", "email", migr.String(255)).With(migr.String(320)).Swap())
}
