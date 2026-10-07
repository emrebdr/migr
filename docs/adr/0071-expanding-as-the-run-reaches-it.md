# 0071. Expanding an emulated operation as the run reaches it

## Status

Accepted

## Date

2026-10-07

## Context

[ADR 0022](0022-rollback-executor.md) compiles a whole queue before running any of it, and
[ADR 0053](0053-expanding-an-operation-against-a-session.md) compiles an *emulated* operation
**against the database**. Those two together are a defect: the second expansion in one compilation is
rendered from a catalog the first has already changed.

It is older than the suite that found it. M11-T2's own end-to-end work measured it twice on SQLite,
and M11-T3 then made it a prerequisite rather than a note — a diff that creates a table and gives it
a foreign key is correct IR in the right order, and on that engine it could not compile at all.

The task's done-when is *"a migration holding two emulated operations applies both, and a rollback of
two emulated migrations in one run undoes both — each operation's statements built from the schema
the operations before it left"*.

### What was measured first

Four throwaway probes against a real SQLite database. Nothing from them ships; each decided
something rather than confirming it.

**The defect, reproduced.** A migration whose up is `alter_column … NOT NULL` then
`add_primary_key`, compiled all up front and then run, leaves `"tag" varchar(64),` — the `NOT NULL`
is gone, because the key's rebuild re-renders the whole table from the pre-alter catalog.

**The fix, and that it works inside one transaction.** The same two operations, each expanded
immediately before it runs inside one `Transact`, leave `"tag" varchar(64) NOT NULL,`. This is the
measurement the whole shape rests on: **both expanders already read through the session's open
transaction** — SQLite's `scan` and MySQL's `readColumn` both go through `c.runner()`, which returns
the transaction when there is one — so an expansion sees the uncommitted DDL of the steps ahead of
it, and **no driver changed at all**.

**M11-T3's shape.** `create_table "extra"` then `add_foreign_key` on it: compile-all-first is
*"cannot rebuild this table "extra": this database has no such table, so there is no definition to
rebuild it from"*; expanded as it runs, it applies. That is the file `migr diff` writes for any new
table with a reference, so M11-T5 was unusable on this engine until this landed.

**One `alter_column` expands to seven statements, none of them standalone.** That is what decided
option E: the atomicity rule has to be asked of an expansion, and the one moment it can be asked is
when the statements exist.

## Decision Drivers

- Invariant #10: both directions, always. A rollback that re-creates what a newer inverse removed is
  a rollback that does not roll back.
- §4.2: a capability is never silently degraded. An emulation that drops an attribute the operation
  before it set is the silent degradation, arriving at run time.
- ADR 0022's reason for inverting a whole queue up front is **irreversibility**, which is a property
  of the migration. It is not a reason to *render statements* up front.
- §5: `migr plan` counts a bound argument rather than splicing it in, because splicing it would print
  SQL that is not the SQL that runs.
- ADR 0062: a resume's recorded index names a statement in the list *that run* emitted.

## Decision

### A. Compilation marks; the caller expands

`plan.Step` gains `Deferred bool`. An operation whose compiler answers `driver.ErrNeedsSession`
becomes a deferred step carrying no statements, and `plan.Planner.Expand(ctx, m, step)` is what fills
one in — through the same `checkCompiled` a compilation goes through, so an expansion that reports
success while producing nothing is still the failure `internal/plan` exists to catch.

`Plan.Deferred()` reports how many steps are still to be compiled, and `Plan.Len()` is therefore the
count of statements compiled **so far** rather than the count a run will send.

**Compilation needs no database again, and the signature says so.** `Compile` and `Step` no longer
take a `context.Context`; neither does `internal/validate`'s own compile check. M8-T4 made
`internal/plan`'s doc comment *"Nothing here opens a database, and that is a property rather than a
convenience"* false; this makes it true again, and `Expand` is the one method that is honest about
needing one. A `_ context.Context` left in place would have been a parameter claiming otherwise.

**A planner with no `Expander` still returns the sentinel.** That is what `migr validate` reads to
report an operation `unchecked` (ADR 0040's shape, ADR 0053's `unchecked` half), and deferring there
would hand it a step nothing could ever fill in.

### B. The run is one transaction bracketing a step loop

`internal/engine` walks a migration's steps in order; a deferred one is expanded immediately before
its statements are executed, inside the transaction the migration is. The transaction has to bracket
the loop rather than one statement list, because that is the only thing that makes the expansion read
what the steps before it did on an engine with transactional DDL.

The expanded step is written back into the plan as the run proceeds, so `Run.Statements`,
`Plan.At`'s position-to-operation lookup and the dirty row all count what actually ran.

### C. Grouping is now the atomicity oracle, and M13-T4 writes the loop

Today's accepted set is exactly *"at least one statement, and every statement transactional"*:
`Groups` over such a list yields one transactional group, and `checkRunnable` refuses every other
shape. So `Groups` keeps its job — it is still the one place `Statement.NoTransaction` is read, and
still not a driver's — and the refusals keep their wording. What it decides is now *whether a
migration can be the one transaction it runs inside*:

- the known statements are grouped before the lock, as they always were;
- an expansion's statements are grouped **as they arrive**, which is the one moment they exist. A
  refusal there is inside the transaction, so the transaction is discarded;
- `ErrNothingToRun` reads a statement list, and a plan holding only deferred steps has none yet — so
  it is refused only where nothing is deferred either. Otherwise every SQLite migration of one
  rebuild would be unrunnable.

**The cost is real and is not hidden.** The speculative multi-run loop ADR 0017 left written for
M13-T4 is deleted along with its three direct-call tests, because a per-group `Transact` cannot
bracket a step loop and SQLite refuses a nested transaction. M13-T4 therefore **writes** that loop
rather than deleting a precondition, and it inherits a question this creates: a deferred step's own
split points are not known until it is expanded, so where a migration is split around a standalone
statement, an expansion after the split can no longer read the statements before it from inside one
transaction. That is recorded in `TASKS.md` on M13-T4 rather than guessed at here.

### D. A resume is refused where any operation is compiled as it runs

`engine.ErrNotResumable`, naming `--applied` and `--rolled-back`. This is ADR 0062's own argument one
layer further in: the recorded index names a statement in the list *that run* emitted, and an
expansion's statements are a function of a schema this run has already changed, so re-rendering them
here numbers a different list. It is refused for **any** deferred step rather than only one after the
failure point, because knowing whether `LastStatement` covers an earlier expansion would mean
re-expanding it — which is the thing that cannot be trusted.

This is strictly better than what it replaces. Before it, a resume silently re-expanded against the
moved catalog and indexed into the result.

**Rejected: recording statements-per-step in the history so a resume could land inside an
expansion.** A history column that still could not re-derive a half-run rebuild, for a case only an
engine whose DDL commits can reach — and on which a rebuild is a single `MODIFY COLUMN` anyway.

### E. A dirty run says how many operations it never compiled

`DirtyError` gains `Deferred`, and where it is non-zero the total is rendered as *"the N it had
compiled"* with a paragraph of its own saying why. The row still holds **how far the run got**, which
is unchanged and is what a resume and a refusal read; what a run can no longer state is **how far it
had to go**, because the operations it never reached are rendered from a catalog it never read.

Measured on both MySQL variants, which is the only engine in compose that can produce this at all:
every statement there auto-commits, so a run can stop with operations still uncompiled.

### F. A preview expands only what it can compile exactly

The first operation of the first queued migration, and only where that migration sends nothing
through the escape hatch first. Everything else is marked `→` with no statements, and a closing note
says why.

That is `judgedFirst`'s own rule (ADR 0069) one level finer, and for the same reason: a preview
applies nothing, so the schema every later operation would run against is the one its predecessors
leave. It keeps the thing `migr plan` was for — an operator can still read a table rebuild before
anything runs, which is the common case on SQLite — without printing SQL that is not the SQL that
runs.

The statement count is qualified in the same breath: `migr plan` says *"7 statements from the
operations it could compile"* and `--dry-run` says *"2 statements, and 1 operation compiled as it
runs"*. A count presented as the whole would read as a smaller migration than the one about to run.

**Rejected: expanding by executing into a transaction the preview discards.** It would be exact, and
it is unacceptable: a rebuild takes an exclusive lock and copies every row, so a *report* would take
a table offline for the length of its own data copy (invariants #12 and #13) — and on an engine whose
DDL commits it would not be discardable at all, so `migr plan` would permanently change the database.

**Rejected: expanding everything against the live catalog and marking it provisional.** That is what
this task set out to stop, moved from the run into the report.

**Rejected: expanding nothing.** Simpler to state, and it takes away the only way to read a rebuild
before it happens while the exact case is cheap to identify.

## Consequences

- PostgreSQL emulates nothing and pays nothing: with no `Expander`, nothing is ever deferred and the
  run is the same single transaction over the same statements it always was.
- `migr validate` is unchanged. It opens no database, so it never has an `Expander`, and an emulated
  operation is still reported `unchecked`.
- `cmd/migr`'s own restorable suite no longer steps its rollback one migration at a time, and its
  second remedy is one migration holding both operations rather than two migrations. The comment
  explaining why it could not be was deleted with the defect.
- A failed expansion in a preview is dropped rather than reported, as `judgedFirst` drops a failed
  read: a preview refuses nothing and must not fail over a question it only asked in order to print
  more. `migr up` asks the same question and does fail over it.

## Notes

The sweep ran 30 mutations and caught 30. Five survivors became untagged tests before the second
pass: the dirty row's partial total and the resume refusal were asserted only in the `integration`
MySQL suite, so `make check` could not see either; and the preview had no case for a second
migration, for a migration holding an escape-hatch statement, or for an expansion that fails. One
mutation turned out to be **equivalent** — `Expand` returns a zero `Step` on failure, so checking
`expanded.Op == nil` is the error check spelled differently — and was re-anchored to write the empty
step back into the plan, which is a real change and is caught.
