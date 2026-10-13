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
// (ADR 0029, A). The three errors that render paragraphs -- engine.DriftError,
// engine.DirtyError and engine.DirtyStateError -- are carried as the fields they
// are built from rather than as their prose; the prose is what --json exists to
// replace, so it is not repeated beside them.
//
// The last two share the `dirty` key and are told apart by its `sense`, which is
// `became` for a run that just left the schema between states and `already` for
// one refused over a database that was. That is the distinction ADR 0024 draws
// with errors.As, written into the document so a consumer branches on a named
// field rather than on which key turned up. Fields only one sense knows are
// absent from the other: a run that just failed knows its statement total and
// the declaration of the session it ran on, and a row read back knows when it
// was recorded. transactional_ddl is a pointer for that reason -- a false there
// would read as a session the refusal never asked (ADR 0061).
//
// # The one command that repairs, and the two ways it reaches a database
//
// migr resolve settles the migration a stopped run left between states, and
// it is the only command invariant #6 permits to exist: a repair path that is
// explicit, separately named, and says exactly what it is changing. The mode
// is required -- naming none of the three is exit 2, as any mistyped
// invocation is -- and --dry-run prints without acting, which is the spelling
// migr up already uses.
//
// --resume follows open's path, because it asks the migration what it does and
// therefore needs every runtime the directory holds. --applied and
// --rolled-back follow runStatus's -- resolve, files, connect, no sources --
// because the row they write is built from the row that went dirty plus this
// session, so nothing needs compiling. That is not an optimization: a
// migration that no longer builds is exactly when an operator reaches for
// them, and starting a runtime to write a bookkeeping row would fail for a
// reason that has nothing to do with the repair (ADR 0062).
//
// What it prints is a third rendering of one history row. recordedRun is the
// shape migr status and migr history already print, and the statements a
// resume will run are laid out the way migr plan lays out a compiled
// operation -- so an operator reads shapes three commands have already taught
// them.
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
// open split into resolve (config and target), discovery and hashing, and
// connect (driver and session), so what a command needs is visible in which it
// calls. status calls the pair as files, in version order, because its sequence
// needs nothing started; a run hashes after its sessions are up, for the reason
// two sections below. status skips the source, since it compiles nothing, and can
// therefore report on a project whose runtime it never asked for. history skips
// discovery too: the log is the database's, and refusing to print it because the
// migrations directory is broken removes the tool somebody reached for to
// diagnose that.
//
// # One directory, several languages
//
// Each migration is routed by its own extension, through the extensions map in
// create.go -- a language owns a set, the first of which is what migr create
// writes, and languageOf reads it backwards. A file whose extension no language
// owns is refused by discovered, naming the file and every extension that is
// routed, which keeps §9's strictness while making the answer a set rather than
// one value. migrations.language is the default for migr create and nothing
// else reads it.
//
// So sources builds one source per language the directory needs and returns a
// mux over them: sql needs no process, go and typescript each get a session, and
// the closer ends every one of them newest first. A failure part-way closes what
// already started, which is the same rule as before with more than one subject.
//
// Two consequences are the whole cost of this. Several runtime sessions are
// alive over one pinned driver.Conn, which is safe only because the engine asks
// one migration at a time -- the untagged suite counts calls in flight on that
// session rather than trusting the claim. And engine.Options.Runtime is a
// function of the migration rather than one string for the run, because
// history.Record.Runtime was always per row; runtimeOf answers it from the same
// map the mux routes on, so the row and the runtime that ran it cannot disagree.
//
// Before M7-T8's refusal, migr up applied every migration its one runtime
// recognized and failed on the first it did not, leaving the project half
// applied and blaming the migration -- the refusal came back from the SDK, as
// unknown_migration, which is true and useless. That guarantee is re-made rather
// than dropped: sourceFor asks each session what it loaded and holds it to what
// was routed there, in both directions, so a routed file nothing loaded and a
// migration two runtimes claim are each a failure naming the file.
//
// migr create --lang still writes a file for another language on purpose, and
// now the project simply runs it.
//
// The source is built after connect and never before it (ADR 0040). A runtime
// session needs the driver.Conn its callbacks reach, so before the connection
// exists there is nothing to give one; t.sources takes it as a parameter, and
// building the source first is refused by runtime.Options.check rather than by
// the compiler, since a nil interface is still spellable. Two consequences: a
// source that fails to start leaves a connection open, so open closes it rather
// than leaking a backend on every failed run; and session.close ends the process
// before closing the connection, because a callback in flight reaches it. Those
// two defers are not interchangeable, for the reason the lock and the
// transaction are not (ADR 0020).
//
// Consequently status renders a filename and history renders version_name. No
// history row records an extension, deliberately, since a run may outlive the
// file it ran from -- and a status with no file carries a zero Migration, whose
// Filename() is the string "_", so the renderer asks before it calls it.
//
// # Where the run order settles
//
// After every session is started, and before the digests are taken. A declared
// dependency can only come from a started runtime, so nothing earlier could know
// one: discovered puts the directory in version order, sources starts what reads
// each language and brings back what each of its migrations declared, and attach
// calls migration.Order over the union before hashing the sequence that came
// back (ADR 0049). validate takes the same step between offlineSource and
// validate.Run, so the gate reads the sequence it gates.
//
// The digests follow the order rather than the order following them:
// history.File exposes no version, deliberately, so a hashed slice cannot be
// permuted from here -- and an accessor that permitted it would invite exactly
// the re-sorting history/doc.go warns against.
//
// An edge names a version, whoever loaded it, so a .ts migration may declare a
// .go one; each session reports its own half and the run resolves one graph over
// both. The merge cannot collide, since routes are disjoint by extension and the
// reconciliation above has already refused a runtime claiming a migration routed
// elsewhere. A cycle, and an edge naming a version nothing carries, are exit 2
// wherever they surface: migration.Order reports every one of both in one pass,
// each cycle as its path in filenames, and a migration is what an operator edits.
//
// status prints version order and names it. It starts no runtime by design
// (ADR 0027), so it cannot read an edge at all; claiming the run order anyway
// and caching the declarations where a report could read them were the
// alternatives, and the second is a new record of what the migrations say with
// its own invalidation. The basis is in --json as order, and the one-line note
// is printed only for a directory holding migrations whose declarations a report
// cannot read -- so a .sql project's output is unchanged, because that format
// has no syntax to declare with. migr plan answers "what order exactly": it
// starts the same runtimes a run does.
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
// # What permits destruction, and what only reports it
//
// --allow-destructive is registered on migr up and migr validate, and on nothing
// else. CLAUDE.md §5 listed it as a global flag; a rollback is not gated and a
// report refuses nothing, so anywhere else it would be a flag accepted and
// ignored, which is worse than one that is absent. allowanceFor reads it beside
// validation.allow_destructive, so one invocation carries both the project's
// standing decision and this run's.
//
// Three commands render the same policy three ways, and the split is CLAUDE.md
// §5's rule rather than a preference. migr up refuses the migration it reached
// and exits 2 -- the argument is wrong, and the operator retypes it or names the
// kind. migr validate reports every one as a destructive problem with the object
// it destroys, which is the list a project needs before the gate stops it. And
// migr plan marks each operation with what it destroys, says which of them
// nothing has permitted, and exits 0 -- as migr status reports drift at 0 while
// migr up exits 4. --dry-run counts them in a line of its own, because a dry run
// that said nothing would report two migrations it would apply and then watch
// migr up refuse the first.
//
// migr init writes allow_destructive: [raw] into the scaffold, which is the one
// defaulted key it writes. The scaffold commits to language: sql, and every
// statement in such a migration is a raw operation migr cannot read -- so
// without it a scaffolded project could not run its own first migration, which
// is the failure the dsn fallback already exists to avoid. The comment beside it
// is what makes it a decision rather than a restated default. See
// docs/adr/0063-destructive-operations.md.
//
// # A lint rule reaches migr validate and no other command
//
// rulesFor resolves migr.yaml's validation.rules against the rules this build
// ships, and only runValidate reads it: there is no flag, and migr up, migr plan
// and --dry-run say nothing about rules at all. That is ADR 0066's E, which is
// M10-T4's own note read straight -- the run's gates are each about whether an
// operation is correct or permitted, and a practice judgment is not one of those.
//
// A rule at error is a problem and exits 2; one at warn is printed in a section
// of its own beneath the problems and exits 0, counted in the summary. The column
// that says what found a problem prints the rule's id rather than the word lint,
// since the id is what a project configures; --json keeps check and rule as two
// members, those being two vocabularies. The remedy names the rules that
// *failed* and not the ones that warned: a remedy explains how to get past
// something that stopped the command, and advice to silence advice is not that.
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
// migr validate reaches no database unless it is asked to: a driver is built
// from migr.yaml and asked to compile, and nothing calls Open. It reads every
// migration, compiles both directions for the configured engine, and checks
// that something can undo each one -- reporting every problem rather than the
// first (ADR 0028).
//
// Opening no database is not the same as needing no configuration. Which engine
// to compile for comes from migr.yaml, so a config it cannot read is still a
// failure.
//
// --connect adds the checks that need the target server, and is registered here
// and nowhere else: every other command either opens a database already or is a
// report that must not. It opens the session before anything else runs, so a DSN
// that points nowhere fails before a runtime is spawned, reads Capabilities(),
// and closes -- a verdict performs no I/O (ADR 0012), so the declaration outlives
// the connection that produced it. It takes no migration lock and reads no
// history: this is the command an operator reaches for during a deploy.
//
// An unreachable database fails the command rather than skipping the check, which
// is ADR 0028 B2's rule: a gate that silently drops a check reports a pass it did
// not earn. Without the flag the report says so in a sentence of its own, because
// "no problems found" alone would make the two runs indistinguishable. It names
// the server it did reach, which is also what --json carries beside connected.
// See docs/adr/0064-capability-preflight.md.
//
// The migrations themselves are still asked over driver.Offline even with the
// flag, and that is deliberate rather than unfinished: answering a migration's
// own reads live would make its verdict depend on how far that database happens
// to be migrated, so a read of a table an earlier migration creates would fail
// against an empty CI database and the report would call the migration broken.
// Unchecked is the honest answer there (ADR 0040), and it stays the answer.
//
// A language needing a runtime keeps that promise rather than trading it away
// (ADR 0040). The process is started -- so a module that will not build, and the
// registration errors ADR 0037 answers at migration.discover, are caught here and
// nowhere earlier -- but the session behind it is driver.Offline, which refuses
// every database call. A migration that reads nothing is therefore validated in
// full, exactly as a .sql one is; a migration that reaches the database is
// reported as unchecked, carrying its reason, and is not a failure. Failing there
// would have a CI gate reject a migration for reading, which migrations may do.
//
// The offline session remembers being reached, because a caller cannot learn it
// from the error: the migration runs in another process, so the sentinel is a
// string by the time it comes back. That is what the checking source in
// session.go wraps, and it exists only for this command.
//
// The remedy line is this package's, not the check's. ir.Raw's reason says to
// write the undo "in Down" -- source-neutral, correct, and language an author of
// a .sql file cannot act on. The command knows migrations.language and prints
// how that format answers it; internal/validate cannot and should not.
//
// It exits 2 where migr status exits 0 over the same project. They answer
// different questions: one reports state, the other judges the repository.
//
// # What this server will not perform
//
// The capability refusal renders three ways, the same split the destructive one
// does. migr up fails with exit 1 -- the world is not ready rather than the
// argument being wrong, since an operator upgrades the server, changes a
// variable, or rewrites the migration -- and --json carries a capability
// sub-document naming the driver, the server, and each operation with its
// subject and the driver's own reason. migr validate --connect reports each as a
// capability problem. And migr plan marks them ×, prints the reason beneath the
// operation, and exits 0; --dry-run counts them.
//
// The plan's reason is new rather than reformatted: before M10-T2 that column
// printed the word "unsupported" alone, beside the statement it would have sent.
//
// # Confirming a rename, which has no command yet
//
// ConfirmRenames is the interactive half of M11-T4 and its one caller is
// M11-T5's migr diff: detection is pure and so is emission, so the person goes
// between them rather than inside either (ADR 0072). The policy is this
// package's rather than that command's, and it is one asymmetry throughout.
//
// A declined rename becomes a drop and a create, which invariant #7 gates -- the
// run refuses it until the project permits that kind. A confirmed rename is
// Destructive() == false and is gated by nothing at all. So the default is no,
// and the answer nothing else checks is the one a person has to type: an empty
// line, an unrecognized answer, and a stream with nothing left in it all
// decline. There is no yes-to-all, that being a guess wearing a confirmation's
// clothes for every candidate the operator did not read; q declines the rest,
// which is safe because what it bulk-applies is the gated side.
//
// Each prompt states both outcomes rather than only the rename, because that is
// the choice -- "rename x to y? [y/N]" hides what saying no costs. A stream that
// ran out and an operator typing q are reported apart, being different facts,
// and a prompt with no streams at all is ErrNotConfirmable rather than a silent
// no: that is the one place declining would be wrong, since a caller would read
// it as somebody's own answer.
//
// It drives its own rounds, because a confirmed table rename is what makes the
// column renames inside it detectable and this is the only thing that knows
// which candidates it has already put to somebody. A loop outside it would
// re-propose every declined candidate forever.
//
// # Seeds
//
// migr seed applies the seeds in seeds.dir and in the subdirectory named for this
// database's environment, through engine.Apply over a session whose History is
// the seed table -- the seeding wrapper -- so the lock, the transactions, dirty,
// drift and resume are a migration's own and nothing in internal/engine branches
// on seeds. The lock is the migration lock: a driver keys it by the history table
// alone, so a seed and a migr up exclude each other (ADR 0076, A).
//
// It refuses unless every migration has run, which is the whole of seeds never
// mixing with migration ordering: a seed always meets the schema the migrations
// leave. Seeds are ordered among themselves alone, so a seed declaring a migration
// names a version nothing in its set carries. A seed emitting a schema change is
// refused by the dataOnly source, and a subdirectory no environment names is
// refused before anything connects, since its seeds would otherwise never run.
//
// Status: init, create, up, down, status, history, plan, validate, version, and
// config -- which is M5 complete. diff is M11-T5, seed is M12-T1 (applying; M12-T5
// lists and rolls back), and cutover is M13-T10. TestEveryCommandIsInTheExitCodeMatrix is what makes each of them say
// which of the five endings it can reach before it ships.
package cli
