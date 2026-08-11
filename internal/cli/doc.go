// Package cli implements the individual migr commands: create, up, down,
// status, plan, validate, diff, history, and seed.
//
// Command wiring and global flags live here; cmd/migr stays a thin entrypoint.
//
// # Streams
//
// Results are written to cmd.OutOrStdout, logs to cmd.ErrOrStderr, and the two
// never mix -- `migr status --json | jq` must not find a log line inside the
// document it is parsing. Taking both writers from the cobra command rather than
// from os also makes every command's output capturable in tests.
//
// The --json flag switches both streams at once: JSON results on stdout, JSON
// logs on stderr. Verbosity is a count, defaulting to warnings only, so an
// ordinary run prints its result and nothing more.
//
// # Exit codes
//
// Anything not explicitly tagged with an ExitError resolves to ExitRuntime, so
// an unclassified failure can never be mistaken for success.
//
// Two contract sentinels resolve themselves below that tag, so no command has to
// remember them: driver.ErrLockTimeout is contention rather than a runtime error,
// and engine.ErrDrifted is the one failure invariant #6 gives a code of its own.
// Both are sentinels rather than concrete types because invariant #1 forbids this
// package from naming a driver at all.
//
// Nothing else resolves itself, and that boundary is what keeps exit code 4 worth
// reading. A history row this build cannot decode reports history.ErrCorrupt and
// one written under a future scheme reports checksum.SchemeError; the first needs
// manual repair, the second needs a newer migr, and neither is someone editing an
// applied migration.
//
// # Command construction
//
// newRootCmd returns a fresh command on every call rather than sharing a
// package-level variable. cobra commands carry parsed flag state, so a shared
// one would leak values between invocations and make tests order-dependent.
//
// Status: skeleton. Commands land across M5 (see TASKS.md).
package cli
