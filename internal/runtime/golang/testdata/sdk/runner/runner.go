// Package runner stands in for the real sdk/go runner until M6-T7 writes it.
//
// It exports exactly what the generated main names -- Register, for a migrations
// package to call from init, and Main, for the generated main to call -- and it
// speaks no protocol at all. What this fixture proves is the adapter's half:
// that the runner was generated with the right import path, built, and executed,
// and that the blank import linked the user's migrations package into the binary.
// That the process then speaks JSON-RPC is the SDK's half, and M6-T10 proves it
// end to end against a real database.
package runner

import (
	"fmt"
	"os"
	"strings"
)

// loaded is what the migrations package registered from its init, in the order it registered.
var loaded []string

// Register records one migration, standing in for what the real SDK does with a migration value.
func Register(version string) {
	loaded = append(loaded, version)
}

// Main prints what was registered and exits, which is all this fixture is asked to do.
func Main() {
	fmt.Fprintln(os.Stdout, strings.Join(loaded, ","))
}
