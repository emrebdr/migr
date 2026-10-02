# 0061. Refusing a dirty database, and what the row may be asked to say

## Status

Accepted

## Date

2026-10-02

## Context

[ADR 0024](0024-dirty-runs.md) built the producing half of this state: `durable` decides what survived
a failure, `recordFailure` sends one or more survivors to `MarkDirty`, and a `*engine.DirtyError`
carries the window to whoever asked for the run. Both `runnable` and `reversible` refuse
`history.StateDirty`, so *"the next `up` refuses"* was already true.

Two things were not.

**The refusal said almost nothing.** `engine.dirtyError` was one `fmt.Errorf` naming the migration and
the recorded index. It did not say which direction the run was performing, what stopped it, when it
ran, or under which batch and ordinal — all of which the row it read already carries — and under
`--json` it rendered as prose in the `error` field with the `migration` field **absent**, the one shape
[ADR 0029](0029-failure-presentation.md) says a paragraph-rendering failure must not have. M9-T5's
done-when is *"the next `up` refuses to proceed and prints exactly what was and was not applied"*,
and that is a message, not a sentinel.

**No server had ever produced a dirty row by failing.** ADR 0024 §Negative records why: PostgreSQL
declares transactional DDL and `checkRunnable` still refuses multi-run migrations, so every dirty
refusal ever tested read a row a test had written by hand — including the integration one, which calls
`MarkDirty` itself. MySQL declares `TransactionalDDL: false` and is the first engine that can reach the
state by failing.

Driving it there found a third thing, and it is the one that moved the design.

**The row recorded the window rather than the cause.** `perform` computed `leftDirty(...)` once and
passed the resulting `*DirtyError` as both the error to return *and* the cause to record, and every
store does `rec.Error = cause.Error()`. Measured against MySQL 8.0 and MariaDB 11.4: the `error`
column held **638 bytes over 10 lines** — the whole window paragraph, down to the advice about
resolving the state and a `dirty at statement 1` sitting inside the column beside `last_statement = 1`.
The column is `text`, so nothing truncated and nothing complained. `MarkFailed` recorded the clean
one-line statement failure, so the two write paths had been disagreeing since M4-T9. And the refusal
this ADR adds reads that column, as does `migr history`.

## Decision Drivers

- M9-T5's done-when: *"prints exactly what was and was not applied."*
- ADR 0024's distinction between a run that **became** dirty and a database that **is** dirty, which
  `errors.As` on `*DirtyError` is the only thing that separates.
- ADR 0029: a failure that renders a paragraph is carried in `--json` as the fields it is built from,
  never as its prose, and names the migration it is about.
- [ADR 0009](0009-migration-checksum.md): a migration may branch on live query results, so the
  operations one run emitted cannot be recomputed.
- M9-T6 resolves the state and prints what it is about to change; whatever this refusal reads, that
  command reads too.
- Invariant #9: every user-facing error names the migration and the offending operation.

## Considered Options

### A. What shape the refusal takes

- **A1: a typed `*engine.DirtyStateError`** ✔, built from the row, rendering a labeled paragraph the
  way `*DriftError` does, with a `--json` reader of its own. M9-T6's TASKS.md note reserved this call
  for that task; it is taken here because the done-when's *"prints"* cannot be met without the fields,
  and the note is updated rather than left to contradict the code.
- A2: extend the `fmt.Errorf`. **Rejected on merit.** The message could be made to say everything, but
  `--json` would still carry a paragraph in `error` and still omit `migration` — and ADR 0029 picked
  the field-carrying shape for `DriftError` and `DirtyError` for exactly that reason. A third
  paragraph-rendering refusal that opted out would make the rule a coincidence.

### B. Whether the two senses of `ErrDirty` stay tellable apart

- **B1: two types, one sentinel** ✔. `DirtyStateError.Unwrap` reports `ErrDirty` and nothing else, so
  `errors.Is(err, ErrDirty)` still finds both senses while `errors.As(err, &*DirtyError)` still finds
  only the run that produced one. The `--json` document names the distinction too, as `sense`.
- B2: one type with a flag. **Rejected**: the two senses carry different fields — a run that just
  failed knows its statement total and its session's declaration, and a row read back knows when it
  was recorded and neither of the other two. A flag would make three of five fields conditional and
  `errors.As` would stop separating them.

### C. Whether the refusal can say how many statements there were

- **C1: no, and it says so** ✔. The row records how far the run got, not how far it had to go. The
  `not applied` line names the boundary — *whatever that run had left after statement N* — and states
  why nothing recomputes it: the statements were the ones **that run** emitted, and a migration may
  branch on live query results (ADR 0009).
- C2: add a `statements` column to the history table. **Rejected as scope, not as an idea.** It would
  let the refusal say *"1 through 3 of 5"* and let M9-T6 print its plan from the row alone, at the
  cost of a column in all three drivers, `Record.Validate`, three shape tests and an amendment to
  [ADR 0013](0013-migration-history.md). M4-T9's forward note scoped this task to
  `Status.Recorded.LastStatement`, and a refusal that states its own limit is not wrong — only less
  specific than one with a column it does not have.
- C3: re-ask the migration and compile it to count. **Rejected**: it starts a language runtime to
  answer a question about a run that is already over, and the answer would be the statements a *new*
  emit produced, which ADR 0009 says may legitimately differ from the ones that half-ran.

### D. What the row records as the cause

- **D1: what stopped the run** ✔ — the `*StatementError`, which already names the position, the
  statement and the server's own message, on one line. `perform` joins the window and the bookkeeping
  failure for the caller and hands `recordFailure` the raw cause, so `MarkFailed` and `MarkDirty`
  now record the same kind of thing.
- D2: keep recording the window, and have the refusal render only its first line. **Rejected**: the
  column would still hold advice about resolving a state, and a row is read by `migr history`, by
  `migr status --json`, and by whatever M9-T6 becomes. Storing a presentation of a row inside that
  row is a second copy to keep in agreement, and it was already wrong — it named a statement index
  beside the column that holds one.

### E. How `--json` carries two documents under one key

- **E1: one `dirty` key with a `sense` of `"became"` or `"already"`** ✔. Fields only one sense knows
  are omitted: `statements` and `transactional_ddl` for the run that just failed, `recorded_at` for
  the row read back. `transactional_ddl` is a `*bool`, so an absent declaration is absent rather than
  `false` — a false there reads as a session this refusal never asked.
- E2: a second key, `dirty_state`. **Rejected**: a consumer would have to know both keys and that
  exactly one appears, where a named discriminator is the thing to branch on. ADR 0024 separates the
  senses by type; naming them in the document is the same distinction written down.

## Decision Outcome

**A1, B1, C1, D1, E1.**

```go
type DirtyStateError struct {
	Migration     migration.Migration
	Version       string
	Direction     ir.Direction
	LastStatement int32
	Batch         int64
	Ordinal       int32
	RecordedAt    time.Time
	Cause         string
}
```

`Cause` is a string because a history column holds text, not an error. The message is four labeled
rows and two sentences, and what an operator sees against a real MySQL server is:

```
migr: 20260927T120000_add_people.sql: this migration's last run left the schema between states.

  applied      statement 1 of the up run, and nothing rolled them back
  stopped by   statement 2: ALTER TABLE m9t5_absent ADD COLUMN orphan int;: executing a statement
               on mysql: Error 1146 (42S02): Table 'migr.m9t5_absent' doesn't exist
  not applied  whatever that run had left after statement 1 -- the row records how far it got,
               not how far it had to go, and nothing here recomputes that: the statements were
               the ones that run emitted, and a migration may branch on live query results
               (ADR 0009)
  recorded     batch 1, ordinal 1, at 2026-10-02 15:01:37 UTC

Neither up nor down runs against this database until the dirty state is resolved explicitly, and
nothing here undoes it -- nothing here knows what the statement that failed left behind. migr
status and migr history print this row without failing; resuming from statement 1 is M9-T6's.
```

A single surviving statement renders as `statement 1`, not `statements 1 through 1`. A row carrying no
cause — which `Record.Validate` refuses to write, so reaching it means a row written around the
contract — renders a line saying so rather than an empty one.

`perform` splits what it records from what it returns:

```go
survived, err := e.runMigration(ctx, ready.groups, e.recorder(&rec))
if err != nil {
	return Run{}, e.fail(ready.item, ready.compiled, errors.Join(
		e.leftDirty(ready, survived, err), e.recordFailure(ctx, rec, survived, err)))
}
```

The join's order is load-bearing and now asserted: the window the run left open is reported above the
row it could not write about it, because both are real and the operator acts on the first one read.

## Consequences

### Positive

- **A real server produces this state by failing.** MySQL 8.0 and MariaDB 11.4 both record
  `last_statement = 1` for a three-statement migration failing at its second, with the first
  statement's table still in the catalog — which is *why* it is dirty rather than failed, and ADR
  0057's auto-commit observed from the engine's side rather than cited.
- **The row a run wrote is the row the next run is refused with**, asserted on one database in one
  test. Every dirty refusal tested before this read a fixture, so the two halves of ADR 0024 had
  never been held to each other.
- `migr history` prints a dirty run's cause on one line. It had been printing a 10-line paragraph
  inside a table cell wherever a real engine wrote the row.
- The `--json` failure document for a dirty database now names the migration, which ADR 0029 recorded
  as the one thing this refusal could not say.

### Negative

- **The refusal cannot say how many statements the run had.** §C. It states that limit in the message
  rather than leaving a reader to infer it, and C2 remains available if M9-T6 wants the column.
- **`DirtyStateError` is a fourth thing `errors.As` is used to distinguish**, after `DriftError`,
  `DirtyError` and `StatementError`. The engine's own tests assert the two dirty types do not find
  each other, because nothing structural prevents a future `Unwrap` from collapsing them.
- **A refusal reads a column written by whatever version of migr ran last.** A row written before this
  task holds the window paragraph, so its `stopped by` line will be a paragraph rather than a line.
  Nothing migrates the column: an applied migration's record is immutable (invariant #6), and
  rewriting a recorded error to improve a message is the shape of edit that rule exists to forbid.

### Neutral

- Exit codes are unchanged. A dirty database is **1** in both senses, and `migr status` and
  `migr history` still exit **0** over one.
- `migr validate` still passes over a dirty database, because it opens none (ADR 0028).

## Notes

The live suite needed a history table it drops per case. `uniqueName(t)` is derived from the test's
name, so it is stable across runs and the previous run's dirty row was still there — which made the
first `Apply` of a fresh run get *refused* rather than going dirty, and `errors.Is(err, ErrDirty)` was
true either way. That is M7-T8's finding again, in the same words: the database is the thing that
remembers.

M9-T6 inherits a type rather than the decision to make one. It resumes from `LastStatement`, which is
the last statement that **succeeded**, so the next statement to run is `LastStatement + 1`; whatever
it prints before acting reads the same row this refusal does. *(It does:
[ADR 0062](0062-resolving-a-dirty-migration.md)'s `migr resolve` renders that row through the same
`recordedRun`, and declined §C2's `statements` column again.)*
