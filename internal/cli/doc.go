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
// A command classifies its own failures by one rule, so that M5-T6 inherits a
// rule rather than a habit: the argument is wrong -> ExitValidation, and the
// world is not ready -> ExitRuntime. `migr create add-users` is the first;
// a config already there, a migrations directory that is not, and a version
// another file already claims are the second. Both are knowable with no
// database, so "needs no connection" is not what separates them -- what
// separates them is whether the operator retypes something or changes something.
//
// # Nothing migr writes replaces anything
//
// Every file this package creates goes through writeNew, which opens with
// O_EXCL, and init refuses an existing migr.yaml before it writes or creates
// anything at all. There is no --force: the config names somebody's databases
// and a migration's checksum may already be recorded, so neither is recoverable
// from the tool that replaced it. TestNoFlagOverwritesAFileMigrDidNotWrite
// holds that against every command's flags rather than against a convention.
//
// # Command construction
//
// newRootCmd returns a fresh command on every call rather than sharing a
// package-level variable. cobra commands carry parsed flag state, so a shared
// one would leak values between invocations and make tests order-dependent.
//
// rootCmd takes the options rather than making them, which is the seam a test
// uses to fix the clock. A migration's version is the instant it was created,
// so without one there is no way to assert the filename create wrote, and no way
// to reach the two-creates-in-one-second case at all except by racing.
//
// # Which database a command reaches
//
// The driver registry is a parameter of Execute rather than something this
// package builds: cmd/migr is the only place that may name a concrete driver
// (invariant #1), and registration is explicit so a duplicate name is reportable
// (ADR 0011). Passing it in is also what lets the untagged suite drive every
// command against a fake database with no Docker at all.
//
// config.Target resolves --database, else the "default" key, else the only
// database defined, else it refuses naming them all. It never fans out. A
// migr.yaml holding a development target and a production one is an ordinary
// file, and `migr up` reaching production because nobody typed a flag is not a
// mistake a tool should be able to make (ADR 0026, F).
//
// # A summary prints before its error
//
// engine.Result is populated alongside a failure, which is the whole reason it
// is a value rather than a field on the error -- so a run that fails on the
// third migration prints the two that committed and then fails. A run that
// failed having done nothing prints no summary at all: "nothing to do" above a
// refusal is two answers to one question, and the first one is wrong.
//
// Status: init, create, up, down, version, and config. The rest land across M5
// (see TASKS.md).
package cli
