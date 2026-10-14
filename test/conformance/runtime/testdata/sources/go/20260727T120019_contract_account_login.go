package migrations

import migr "github.com/emrebdr/migr/sdk/go"

// init registers the contract of renaming accounts.handle, irreversible after a lowercasing backfill.
func init() {
	migr.Register(migr.ReplaceColumn("accounts", "handle", migr.String(64)).WasNull().
		WasDefault(migr.Literal("anon")).RenameTo("login").Default(migr.Literal("guest")).
		Through(migr.TransformLower).Contract())
}
