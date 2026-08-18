// Package migrations is the fixture project's migrations, as a Go project writes them.
package migrations

import (
	"github.com/emrebdr/migr/sdk/go/runner"

	"example.com/app/helpers"
)

func init() {
	runner.Register("20260727T153000" + helpers.Suffix)
}
