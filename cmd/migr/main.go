// Command migr is the CLI entrypoint for the migr migration framework.
//
// This is also the only place where database drivers and language runtimes are
// wired into the binary: each exports a constructor, and this package registers
// it by name and handles the error. Registration is explicit rather than an
// init() behind a blank import, because a duplicate name must be reportable and
// an init() has nowhere to report it (ADR 0011). Core packages must never import
// a concrete driver or runtime directly (CLAUDE.md invariant #1), and depguard
// enforces that.
//
// main delegates to run so that deferred cleanup still executes: os.Exit does
// not unwind the stack, so any defer registered above it would be skipped.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/emrebdr/migr/internal/cli"
)

func main() {
	os.Exit(run())
}

// run executes the CLI and returns the process exit code.
func run() int {
	err := execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "migr: %v\n", err)
	}

	return cli.ExitCode(err)
}

// execute builds the driver registry this binary carries and runs the command line against it.
func execute() error {
	registry, err := drivers()
	if err != nil {
		return err
	}

	return cli.Execute(context.Background(), registry, os.Args[1:])
}
