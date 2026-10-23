// Command migr is the CLI entrypoint for the migr migration framework.
//
// This is also the only place where database drivers and language runtimes are
// wired into the binary: each exports a constructor, and this package registers
// it by name and handles the error. Registration is explicit rather than an
// init() behind a blank import, because a duplicate name must be reportable and
// an init() has nowhere to report it. Core packages must never import
// a concrete driver or runtime directly (CLAUDE.md invariant #1), and depguard
// enforces that.
//
// main delegates to run so that deferred cleanup still executes: os.Exit does
// not unwind the stack, so any defer registered above it would be skipped.
//
// The one print left here is for the one failure that precedes the CLI. A
// registry that will not build happens before a flag is parsed, so nothing yet
// knows whether --json was asked for; everything after it is reported by
// internal/cli, which is the only place that does.
//
// # Why this handles signals
//
// A language runtime is spawned into a process group of its own, so the
// terminal's Ctrl-C reaches migr and not the child (M6-T5).
// Canceling the run is what sends $/cancel, waits the grace, and then kills
// that group; without it, an interrupted migr up would leave a runtime holding
// a database connection through callbacks nothing will ever answer.
//
// The second interrupt restores the default disposition and the kernel ends
// the process, because an operator who asks twice is not asking for a grace
// period. That is signal.NotifyContext's own behavior and the reason it is
// what this uses.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/emrebdr/migr/internal/cli"
)

func main() {
	os.Exit(run())
}

// run executes the CLI and returns the process exit code.
func run() int {
	engines, err := drivers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "migr: %v\n", err)

		return cli.ExitCode(err)
	}

	languages, err := runtimes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "migr: %v\n", err)

		return cli.ExitCode(err)
	}

	ctx, restore := interruptible(context.Background())
	defer restore()

	return cli.ExitCode(cli.Execute(ctx, engines, languages, os.Args[1:]))
}

// interruptible cancels the run on the first interrupt and leaves the second one to the kernel.
func interruptible(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
}
