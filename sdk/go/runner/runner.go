package runner

import (
	"os"

	migr "github.com/emrebdr/migr/sdk/go"
)

// Main serves this program as a migr runtime and exits with the status docs/protocol.md §9 gives.
func Main() {
	os.Exit(migr.Run())
}
