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
// A command classifies its own failures by one rule: the argument is wrong ->
// ExitValidation, and the world is not ready -> ExitRuntime. `migr create
// add-users` is the first; a config already there, a migrations directory that
// is not, and a version another file already claims are the second. Both are
// knowable with no database, so "needs no connection" is not what separates them
// -- what separates them is whether the operator retypes something or changes
// something.
//
// M5-T6 held every command against that rule and moved four failures. A flag,
// command name or argument count cobra refuses is now 2 rather than 1: nothing
// is more literally "the argument is wrong", and cobra reports all of it, so
// argsOf wraps every Args check and SetFlagErrorFunc catches the rest. --step
// and --to values that name no migration are 2 rather than 1, tagged by the
// commands that register those flags. And a migrations directory that is not
// there, or a migration file that will not open, are 1 rather than 2 -- migr
// create already exited 1 for the first of those, and neither is fixed by
// retyping anything. engine.ErrNotApplied stayed at 1 on purpose: the same
// command line succeeds against a database that does carry the version.
//
// The root command carries both an Args check and a RunE that prints help, and
// each is load-bearing. Cobra applies its own unknown-command check only to a
// command whose Args is nil, and returns flag.ErrHelp before validating the
// arguments of one that has nothing to run -- so without both, `migr nonsense`
// is answered with the help text instead of refused.
//
// # A failure is a document too
//
// The failure is written by this package rather than by cmd/migr, because
// cmd/migr never sees --json and this is the only place opts.jsonOut exists.
// Without it, `migr up --json` against a drifted database put fourteen lines of
// English on the stream that promises JSON.
//
// It goes to stderr, as one object on one line, so stdout keeps holding exactly
// one result document -- a run that committed two migrations and then failed
// owes both answers, and putting them in one stream makes neither parseable
// (ADR 0029, A). The two errors that render paragraphs, engine.DriftError and
// engine.DirtyError, are carried as the fields they are built from rather than
// as their prose; the prose is what --json exists to replace, so it is not
// repeated beside them.
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
// # A report takes no lock
//
// status and history only ever read, and they read without the migration lock.
// A preview takes it because it answers "what will run", and an answer about a
// database someone else is changing is not one; a status answers "what is" and
// does not promise to act on it. Locking would make a read-only report block a
// deploy and give it an exit code 3 of its own, when it is exactly the command
// an operator wants during one (ADR 0027, A).
//
// They exit 0 having found drift or a dirty migration. The five states are the
// output, not the failure -- migr up is already the gate that exits 4 and 1, and
// one database holding both would have to pick between them.
//
// A database with no history table is printed as itself: one sentence, plus how
// many migration files are on disk, and no rows. Not N pendings, which is what a
// preview says and what ADR 0019 exists to prevent -- read that way, a fully
// migrated production database looks virgin.
//
// The fold is history.Read plus history.Reconcile, called here rather than
// through engine.inspect, whose substitution of a present-empty log is exactly
// what that paragraph refuses. session.log narrows the store to history.Reader,
// so no report can write, Ensure, or repair.
//
// # What a command reaches for
//
// open split into resolve (config and target), files (discovery and hashing),
// and connect (driver and session), so what a command needs is visible in which
// it calls. status skips the source, since it compiles nothing -- and a project
// whose language has no runtime until M6-T10 can therefore still ask for its
// status. history skips discovery too: the log is the database's, and refusing
// to print it because the migrations directory is broken removes the tool
// somebody reached for to diagnose that.
//
// Consequently status renders a filename and history renders version_name. No
// history row records an extension, deliberately, since a run may outlive the
// file it ran from -- and a status with no file carries a zero Migration, whose
// Filename() is the string "_", so the renderer asks before it calls it.
//
// # What a plan prints, and what it does not yet
//
// migr plan is engine.Preview rendered in full: --dry-run prints the statement
// counts and this prints the statements, off one pipeline, so there is no second
// preview to drift out of sync. --down plans the rollback, which is the
// direction that most needs it -- an authored down is the only thing standing
// between an operator and statements nobody wrote, and a .sql migration that
// authors none is refused here rather than partway through migr down.
//
// Each operation carries the verdict this engine gives it, printed whether it is
// native or emulated: a label that appears only when something is unusual is one
// nobody learns to read. The verdict is resolved here rather than on
// plan.Planner, which internal/plan/doc.go reserves for M13-T6.
//
// It does not print lock impact, and that is not an omission. Every .sql
// migration compiles to ir.Raw, which PostgreSQL declares LockUnknown, so every
// line of every plan M5 can produce would read `unknown`. The lock axis and
// --safety are M13-T6, by name. Nor does it mark a statement that cannot run
// inside a transaction: no driver sets Statement.NoTransaction yet, and
// engine.checkRunnable refuses such a migration before a plan could print one.
// TestPlanRefusesAMigrationNothingCanRunInOneTransaction fails the day M13-T4
// changes that, which is when the marker is worth adding.
//
// # A plan prints before its error where a run does not
//
// A run that failed having done nothing prints no summary: "nothing to do" above
// a refusal is two answers to one question. A plan that compiled two of three
// migrations produced two thirds of what was asked for, and the refusal is the
// answer those two are the context for -- so it prints them and then fails. A
// plan that compiled nothing prints nothing, which is the same rule, not an
// exception to it. Reporting every refusal rather than the first is M5-T5's.
//
// # What validate checks, and what it opens
//
// migr validate is the only command that reaches no database at all: a driver
// is built from migr.yaml and asked to compile, and nothing calls Open. It
// reads every migration, compiles both directions for the configured engine,
// and checks that something can undo each one -- reporting every problem rather
// than the first (ADR 0028).
//
// Opening no database is not the same as needing no configuration. Which engine
// to compile for comes from migr.yaml, so a config it cannot read is still a
// failure. And it cannot validate a Go or TypeScript project, since sourceFor
// refuses a language whose runtime does not exist yet.
//
// The remedy line is this package's, not the check's. ir.Raw's reason says to
// write the undo "in Down" -- source-neutral, correct, and language an author of
// a .sql file cannot act on. The command knows migrations.language and prints
// how that format answers it; internal/validate cannot and should not.
//
// It exits 2 where migr status exits 0 over the same project. They answer
// different questions: one reports state, the other judges the repository.
//
// Status: init, create, up, down, status, history, plan, validate, version, and
// config -- which is M5 complete. diff is M11-T4, seed is M12-T1, and cutover is
// M13-T10. TestEveryCommandIsInTheExitCodeMatrix is what makes each of them say
// which of the five endings it can reach before it ships.
package cli
