// Package validate checks a set of migrations without running them: that each one can be read,
// that every operation it emits compiles for the target engine, and that something can undo it.
//
// Status: the framework and the three checks knowable with no database (M5-T5). Destructive-
// operation detection is M10-T1, capability preflight M10-T2, reference integrity M10-T3, and a
// pluggable lint rule set M10-T4 — each of which adds a Check rather than a second report.
// See docs/adr/0028-validation-report.md.
//
// # It never opens a database
//
// A driver is built from migr.yaml and asked to compile; nothing here calls Open. That is what
// makes this the one command CI can run on a pull request with no secret at all, and it is a
// property of the package rather than of the caller — Options carries a plan.Planner, which holds
// a driver.Compiler, and there is nowhere to put a session.
//
// The half it therefore cannot answer is the version-dependent one: postgres.checkServer refuses
// an operation this *server* cannot perform, and reading that needs a capability.Set built inside
// Open. M10-T2 is that half, and the split is a consequence of the contract rather than a choice.
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
package validate
