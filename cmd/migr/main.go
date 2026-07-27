// Command migr is the CLI entrypoint for the migr migration framework.
//
// This is also the only place where database drivers and language runtimes are
// wired into the binary, via blank imports of their registration packages. Core
// packages must never import a concrete driver or runtime directly (CLAUDE.md
// invariant #1).
//
// Status: scaffolding. The real CLI lands in M0-T6 (see TASKS.md).
package main

import "fmt"

func main() {
	fmt.Println("migr: scaffolding only — the CLI is implemented in M0-T6 (see TASKS.md)")
}
