package migrations

import migr "github.com/emrebdr/migr/sdk/go"

// init registers the expand of renaming accounts.handle to login, which carries a default from the start.
func init() {
	migr.Register(migr.ReplaceColumn("accounts", "handle", migr.String(64)).WasNull().
		WasDefault(migr.Literal("anon")).RenameTo("login").Default(migr.Literal("guest")).
		Through(migr.TransformLower).Expand())
}
