// Package engine orchestrates migration execution: locking, transactions,
// apply and rollback, and batch semantics.
//
// Deciding what order migrations run in is not here. Finding them, sorting them
// by version, and overlaying the dependencies they declare is
// internal/migration, which imports stdlib and nothing else — so the CLI can
// list and validate migrations without reaching through an executor that opens
// databases (M4-T1, M4-T2). What arrives here is already a sequence.
//
// Apply runs one migration's compiled statements as one all-or-nothing unit.
// It groups the statements first, brackets the single run in the session's
// transaction, and reports a failure by naming the statement's position — the
// number M4-T9 records as how far a run that could not be rolled back got.
//
// # Grouping decides where transactions begin
//
// A statement may declare that it cannot run inside a transaction; CREATE INDEX
// CONCURRENTLY is why the flag exists. Groups splits a statement list into
// consecutive runs that agree on that flag, so a migration is a sequence of
// runs rather than one wrapped block. The distinction is the whole point: an
// executor written as "one migration = one transaction" cannot be split later
// without rewriting it (ADR 0017).
//
// Grouping lives here and not in a driver, because it reads Statement's flag
// and nothing engine-specific. Three drivers must not reach three answers about
// the same statement list.
//
// # What Apply refuses, and why refusing is the honest answer
//
// Three migrations are refused before anything runs, each naming the task that
// lifts the refusal:
//
//   - one that compiles to no statements at all, because reporting success for
//     a schema change that never happened is the same failure the driver
//     conformance suite exists to reject, one layer up;
//   - one whose session declares no transactional DDL, because a failure
//     partway would leave the schema half changed with nothing recording how
//     far it got. The dirty-state model that makes such a run recoverable is
//     M4-T9's. The branch reads capability.Set.TransactionalDDL and never a
//     driver name (CLAUDE.md §11);
//   - one that groups into more than one run, because two runs means two
//     commits, and a half-applied migration reported as an atomic one is
//     exactly what this task exists to prevent. Splitting a migration around a
//     statement that cannot be in a transaction is M13-T4's.
//
// The last is unreachable today: nothing any driver compiles sets the flag. It
// is written anyway, so the shape M4-T5 and M13-T4 inherit already exists and
// they delete a refusal rather than redesign an executor.
//
// # Lock first, then transact
//
// The migration lock is acquired before the transaction begins. Deferring both
// in that order makes the rollback run before the release, which matters
// because PostgreSQL refuses every command in an aborted transaction — the
// release included. Reversing the order turns a failed migration into a failed
// release as well, and the second failure is the one an operator would read.
//
// Status: statement-level execution complete (M3-T5). The queue that runs many
// migrations in dependency order, records each before its transaction commits,
// and aborts the rest on failure is M4-T5; the dirty-state model is M4-T9 and
// the transaction opt-out is completed in M13-T4 (see TASKS.md).
package engine
