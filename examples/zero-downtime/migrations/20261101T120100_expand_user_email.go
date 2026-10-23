package migrations

import migr "github.com/emrebdr/migr/sdk/go"

func init() {
	migr.Register(migr.ReplaceColumn("users", "email", migr.String(255)).
		With(migr.String(320)).RenameTo("email_address").Expand())
}
