// Package validate checks a set of migrations without running them: that each one can be read,
// that every operation it emits compiles for the target engine, and that something can undo it.
//
// Status: complete (M10). The framework and the three checks knowable with no database (M5-T5); a
// third outcome for the halves nothing could judge (M6-T10); destructive-operation detection
// (M10-T1); capability preflight (M10-T2); a pluggable lint rule set (M10-T4); reference integrity
// (M10-T3); and the lock ceiling (M13-T6) -- each of which adds a Check rather than a second
// report.
//
// # The lock check runs only where a ceiling was asked for
//
// Options.Ceiling is safety.max_lock_impact under --require-safe, and empty runs no lock check; a
// ceiling with no capability declaration is refused as ErrCeilingOffline, since every cost would
// then be zero and exceed it. It judges the up half alone, for the destructive check's reason, and
// compiles that half as a run would -- with the session's rewrites, an operation on a table the
// migration creates costing nothing, and an emulated operation judged on its verdict and never
// expanded -- so an operation is judged at the cost migr up would pay. It runs no driver.Assessor,
// because this package reads no table state. A half that does not compile is left to the compile
// check, which has already reported it.
//
// # The destructive check is the one that reads something other than the migrations
//
// Options.Allow is what a run would be permitted to destroy — migr.yaml's list of operation kinds,
// plus --allow-destructive — and engine.Destructive is the policy both this package and the
// executor read, so a report and a run cannot disagree about one operation. It judges the up half
// alone: ir.CreateTable inverts to DropTable, so judging a down would report every project that
// ever created a table.
//
// It stops nothing. Being unpermitted is no reason to skip compiling the same operation, and the
// rule below is that a check stops only the checks that depend on it. One problem is reported per
// operation, carrying the object it destroys, because this is the command a project runs to find
// out which kinds it has to permit before migr up refuses one of them.
//
// # The reference check judges against two projections, or it does not run
//
// A reference integrity check has to know what the schema will look like when a migration runs, and
// internal/fold is what answers that: Forward folds and does not validate, so an add_column onto a
// table nothing created and a key to a column nothing added are each a fold.Refusal naming both.
// What this package decides is what to fold from, and the answer is that neither candidate is a
// basis by itself:
//
//   - the live schema alone refuses every operation of every migration that has already run, since
//     the schema is what they built. That is the live-catalog problem reached from the other side;
//   - the project's own beginning alone refuses every operation of a migration migr diff wrote. A
//     drift correction re-adds an index the project already created and drops a column the project
//     never had, so replaying it from nothing cannot work -- and reporting that would have migr
//     diff's own output fail this command.
//
// Both were measured. So the check folds each half through every projection it has and reports what
// all of them refused: a reference satisfiable neither from the project's own beginning nor by the
// database as it is. An applied migration goes silent, a project adopted into an existing database
// goes silent, and two migrations creating one table against a database holding neither is still
// caught -- which is why there is no suppressed class of refusal.
//
// Options.Schema is nil for a run that opened nothing, and then the check does not run at all. The
// report says so, which is the sentence the capability check already owed for its own reason.
//
// Both halves are judged, the down folded onto the state its own up just left, because a rollback
// runs newest first. The down is judged only where the up projected cleanly: a derived down is the
// up inverted, so a broken up makes a broken down, and reporting both is two complaints about one
// defect. A raw operation ends the projection, recording one Unchecked that says no migration after
// it was judged either -- so a .sql project reports one entry rather than one per migration.
//
// # It never opens a database
//
// A driver is built from migr.yaml and asked to compile; nothing here calls Open. That is what
// makes this the one command CI can run on a pull request with no secret at all, and it is a
// property of the package rather than of the caller — Options carries a plan.Planner, which holds
// a driver.Compiler, and there is nowhere to put a session.
//
// That survived M10-T2 and M10-T3, the two tasks that made migr validate able to connect. What
// Options gained is a *capability.Set and a *schema.Snapshot: two values the caller read, one
// answering with no I/O by contract and the other a plain description, so the command
// opens a database and this package still cannot. Nil means the run
// opened none and the capability check does not run — and nil rather than a zero Set because an
// unbuilt one refuses every operation, which is also what driver.Offline's own set does on purpose.
// Options.check refuses an unbuilt set as the caller bug it is.
//
// A migration may itself need one, which is a different problem and has a different answer. A
// source raises ErrNotCheckable for a half whose answer cannot be trusted — one that reached a
// database this run deliberately did not open — and the runner records it as Unchecked, carrying
// the reason, rather than as a Problem. Unchecked is not a failure: rejecting a migration for
// reading would make a CI gate refuse something migrations are allowed to do. Reporting it as
// passing would be worse, since a half nothing judged would be indistinguishable from one that
// was. What decides it is the caller's, not this package's: internal/cli owns the offline session
// and knows whether it was reached.
//
// # The two refusals, and why they are two checks
//
// The half an offline run cannot answer is the version-dependent one: postgres.checkServer refuses
// an operation this *server* cannot perform, and reading that needs a capability.Set built inside
// Open. The split is a consequence of the contract rather than a choice.
//
// So CheckCompile reports what a pure, version-blind compiler refuses and CheckCapability reports
// exactly what only the connected server knows — and the order is load-bearing. Every driver builds
// its verdict from Compile's own refusal first, so asking the declaration about an operation the
// compiler already refused prints one sentence twice, under a name that would change depending on
// whether the caller connected. An operation compiled against the database is Unchecked for
// compiling and still judged against the server, because MySQL's column redefinition is emulated
// *and* version-gated: the two statements are about different things.
//
// Both halves are judged, a derived down included. A rollback that cannot run is the thing this
// command exists to find before anyone tries it, and the reason the destructive check judges the up
// half alone does not apply — an operation no server will perform is just as unrunnable backwards.
//
// Opening no database is not the same as needing no configuration. Which engine to compile for
// comes from migr.yaml, and there is no answer without one.
//
// # The sixth check is the only one with a severity, and the only one that can warn
//
// Options.Rules holds what internal/rule resolved against migr.yaml's validation.rules, and the
// lint pass runs once per migration *after* the five checks -- which is what lets a rule read where
// the migration's down came from. A rule at error lands in Problems and one at warn lands in
// Warnings, a third list beside Unchecked: judged-and-failed, judged-and-noted, not-judged. So
// Problems keeps meaning "this run exits 2", and Clean() ignores warnings entirely.
//
// Nil Rules means the caller wants no lint pass, mirroring Capabilities; a non-nil zero set is
// refused as ErrUnbuiltRules, since a project that switched every rule off resolves to a *built*
// set holding every rule and those two must not be one value.
//
// A migration whose up could not be read is not linted, which is the rule below applied without
// exception. And none of the five rules can fire on a .sql project, for three reasons rather than
// one: every statement there is ir.Raw, which carries no columns and no key to read; that format has
// no host.db.exec to send, so a half's Direct is always empty; and it can never reach UndoDerived,
// a raw operation having no inverse, so what undo() hands a rule is authored, declared or none.
//
// # Every problem, in the order a run would meet them
//
// Run returns a Report rather than an error, for the reason history.Reconcile does: "there are
// three problems" is the answer, not a failure to produce one. The error return covers only a run
// that could not be set up — no source, no compiler — mirroring engine.Options.check.
//
// Reporting everything is not the same as reporting everything everywhere. A migration that cannot
// be read cannot be compiled or reversed either, and saying so three times is three complaints
// about one defect. So a check that fails stops the checks that depend on it **for that migration
// and nothing else**: the next migration is unaffected, and the report holds one problem per
// distinct thing wrong.
//
// Within a migration, every operation is still reported. plan.Compile fails a migration whole and
// ir.ReverseAll fails a list whole — both correct for a run, which must not perform half of one —
// so this package walks the operations itself through the same primitives, Planner.Step and
// Operation.Reverse. An operator fixing one refusal sees the other in the same pass.
//
// Determinism is by construction: a slice walked in order, no map iterated (invariant #8).
//
// # Reversibility is knowable here and nowhere earlier
//
// M4-T6 refuses an irreversible migration with exit code 1, because a rollback needs a database to
// know what is applied. Asking every migration for its down, and inverting what authors none, needs
// no database — so this is where "which of these could never be rolled back" is answerable before
// anyone tries, and where exit code 2 becomes honest for it.
//
// A migration whose author declared it irreversible with a reason is not a problem. Invariant #10
// allows exactly that, and a check that flagged it anyway would make the declaration something
// nobody could satisfy. The declaration reaches this package as engine.ErrDeclaredIrreversible,
// which the executor also honors — so a migration that validates clean cannot then fail a rollback
// for a reason validation could have found.
//
// A derived down is compiled, not assumed. An inverse can be refused by a driver that accepted what
// it inverts, and stopping at "it inverted" would pass a rollback nothing can run.
//
// Both halves are read as questions, in engine.ModePlan. This command opens no database at all, and
// a check that ran a migration's escape-hatch writes on the way past would not be a check (M6-T4).
package validate
