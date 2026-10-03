// Package validate checks a set of migrations without running them: that each one can be read,
// that every operation it emits compiles for the target engine, and that something can undo it.
//
// Status: the framework and the three checks knowable with no database (M5-T5); a third outcome
// for the halves nothing could judge (M6-T10); destructive-operation detection (M10-T1);
// capability preflight (M10-T2). Reference integrity is M10-T3 and a pluggable lint rule set
// M10-T4 — each of which adds a Check rather than a second report. See
// docs/adr/0028-validation-report.md, docs/adr/0040-running-a-go-migration.md,
// docs/adr/0063-destructive-operations.md and docs/adr/0064-capability-preflight.md.
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
// # It never opens a database
//
// A driver is built from migr.yaml and asked to compile; nothing here calls Open. That is what
// makes this the one command CI can run on a pull request with no secret at all, and it is a
// property of the package rather than of the caller — Options carries a plan.Planner, which holds
// a driver.Compiler, and there is nowhere to put a session.
//
// That survived M10-T2, which is the task that made migr validate able to connect. What Options
// gained is a *capability.Set: a value the caller resolved, which answers with no I/O by contract
// (ADR 0012), so the command opens a database and this package still cannot. Nil means the run
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
// and knows whether it was reached (ADR 0040, C).
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
// See ADR 0064.
//
// Opening no database is not the same as needing no configuration. Which engine to compile for
// comes from migr.yaml, and there is no answer without one.
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
// a check that ran a migration's escape-hatch writes on the way past would not be a check (M6-T4,
// ADR 0033).
package validate
