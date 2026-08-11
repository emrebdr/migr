# 0023. `--to` names the state to end in, and narrowing is a prefix of the run order

## Status

Accepted

## Date

2026-08-11

## Context

[ADR 0021](0021-apply-executor.md) built the apply queue and [ADR 0022](0022-rollback-executor.md)
the rollback queue. Both act on everything: `Apply` runs every pending migration, `Rollback` undoes
every applied one. ADR 0022's own Neutral section says what is left — *"`--step`, `--to`, and the
'last batch' default are M4-T8's, and nothing reaches this from a command line until M5-T2."*

ADR 0021 §E already settled what a batch **numbers**: one past that database's own `Latest`, per
database, with ordinals counting the runs an invocation performed rather than the migrations it
considered. What it did not settle is how an operator asks for less than everything, and that is four
separate questions with genuinely different answers.

Three things about the existing code decide most of it.

**Neither queue is in version order.** Going up, the sequence is `migration.Order`'s — timestamps
with the declared-dependency overlay on top, and [ADR 0018](0018-declared-dependencies.md) makes a
backwards-pointing dependency the *use case* rather than a mistake. Going down, ADR 0022 §D chose
`(Batch, Ordinal)` descending off the anchor run and **rejected version-descending by name**. So the
one version comparator that exists in the repository — `migration.byVersion`, a `strings.Compare` on
the fixed-width timestamp — is the wrong tool in both directions.

**`Latest` is direction- and outcome-blind.** `pgHistory.Latest` is
`ORDER BY "batch" DESC, "ordinal" DESC LIMIT 1` with no `WHERE` clause at all, so a `down` row, a
failed row and a dirty row each count as the latest run. ADR 0013 chose that deliberately and
ADR 0021 §E relies on it: allocating the next batch must not collide with anything, including a
rollback's own rows.

**`queued` already carries the run that applied each migration.** M4-T6 added `recorded
history.Record` to it so `newestFirst` could sort by `(Batch, Ordinal)`. That field is the anchor
*up* run, which is a different fact from `Latest`.

## Decision Drivers

- CLAUDE.md §5 fixes the surface: `migr up [--step N] [--to VER]` and `migr down [--step N] [--to
  VER]`. There is no `--all` and no `--batch`.
- M4-T8's done-when: *"`down` without arguments rolls back the last batch, not the last single
  migration."*
- Invariant #8: nothing in a planning path may depend on map iteration order or a clock.
- ADR 0021 §E: an ordinal numbers what an invocation did, so a narrowed batch must still number 1, 2.
- The refusals M4-T5 and M4-T6 established fire on the whole reconciled report, before anything runs.

## Considered Options

### A. What `--to <version>` names

- **A1: the state the database should end in** ✔. `up --to X` applies through X inclusive;
  `down --to X` undoes everything newer than X and leaves X applied. Both runs finish with the
  database at X, which is what the preposition means and what Rails and golang-migrate both do.
- A2: the last migration the run acts on — inclusive in both directions, so `down --to X` undoes X.
  **Rejected on merit.** `up --to X` and `down --to X` would then leave the database in two different
  places, and an operator reading a runbook cannot tell which one a line means without knowing which
  command it was. The appeal was implementation uniformity: `--step` and `--to` become one prefix
  rule. That is a real cost of A1 and is recorded below.

### B. How the prefix is computed

- **B1: a prefix of the sequence the queue is already in** ✔. `--step N`, `--to V` and the last batch
  all take a leading run of the queue; they differ only in how the run ends.
- B2: a version comparison — `strings.Compare(v, target) <= 0`. **Rejected**, and it is the mistake
  this ADR exists to prevent. Going up it is wrong whenever a declared dependency points backwards,
  which ADR 0018 made the use case; going down it is wrong always, because ADR 0022 §D rejected
  version order as the undo order. It would pass every test written with version-ordered fixtures.

### C. Where the "last batch" default lives

- **C1: the engine's zero `Scope` stays "everything"; `migr down` passes `LastBatch()`** ✔.
  `engine.Rollback` keeps the behaviour ADR 0022 gave it, the neutral default stays neutral, and the
  policy is one visible line in the command that owns it.
- C2: the zero `Scope` means "the last batch" going down and "everything pending" going up.
  **Rejected on merit.** A zero value that means something different per direction is one nobody can
  read at a call site, and it would silently change what `engine.Rollback` does for every caller that
  predates the field. The cost of C1 is that the done-when's second clause is delivered as a
  capability rather than as observable behaviour, since `migr down` does not exist until M5-T2 —
  stated in Consequences rather than hidden.

### D. Which batch bare `down` selects

- **D1: the highest `recorded.Batch` among the applied queue** ✔. That is the anchor *up* run of each
  migration the database still carries, and in `newestFirst` order those migrations are already a
  prefix.
- D2: read it from `Latest`. **Rejected**, and it is the trap M4-T8's own note flags. `Latest` is
  direction-blind, so after one rollback the highest batch in the table is that rollback's own —
  which has nothing left to undo. A second `migr down` would report nothing to do, and every batch
  before it would be unreachable forever.
- D3: `max(batch)` over the whole log. **Rejected** for the same reason plus one more: a failed or
  dirty run also carries a batch, so a run that changed nothing would shadow the batch that did.

### E. Where narrowing sits relative to the state switch

- **E1: after it — refusals fire on the whole report, narrowing sees what survives** ✔. It is what
  M4-T8 specifies, and it keeps `drifted`, `dirty` and `missing` properties of the reconciled
  database rather than of one invocation's appetite.
- E2: before it, so a refusal only fires for a migration the run would actually touch.
  **Rejected on timing, not on merit** — see Consequences.

### F. What a migration narrowing excluded reports

- **F1: an entry in `Result.Skipped` carrying the state it is still in** ✔. `migr up --step 1` can
  then say two pending migrations remain, from the `Result` alone.
- F2: nothing. **Rejected**: the caller would have to reconcile a second time, against a database it
  has already given the lock back on, to answer a question the first pass knew.

## Decision Outcome

**A1, B1, C1, D1, E1, F1.**

`Scope` is a sum type in `internal/engine` — exactly one of `All()`, `Steps(n)`, `To(v)`,
`LastBatch()` — with unexported fields, so `--step 2 --to X` is unrepresentable rather than refused,
and its zero value is `All()`. It is an `Options` field, validated from `Options.check()` before the
lock, and its kind is switched with no `default` so `exhaustive` names every switch the day a fifth
selector arrives.

Narrowing is two methods rather than one with a direction flag, because the two differ in more than a
sign:

- `narrowUp` cuts against the **report**, not the queue, so `up --to X` still applies a pending
  migration ordered before an already-applied X. It refuses `LastBatch` outright: a pending migration
  has never run and belongs to no batch.
- `narrowDown` cuts the queue `newestFirst` has already ordered, exclusive of the `--to` target, and
  is the only one that reads `recorded.Batch`.

The order inside `classifyDown` is therefore fixed: `selected → newestFirst → narrow → number`.

Four refusals, each naming what the run did have: `ErrNoSteps` for a count below one, `ErrUnknownVersion`
for a `--to` target this run reconciled to nothing, `ErrNotApplied` for a `down --to` on a version
the database does not carry, and `ErrNoBatch` for `LastBatch()` given to an apply.

## Consequences

### Positive

- `--step`, `--to` and the last batch are one idea — a prefix of the order the run is already in —
  rather than three filters that could disagree.
- The version-comparison bug is unwritable: nothing in `internal/engine` compares two versions.
- A narrowed run still allocates its batch from `Latest`, so ADR 0021 §E is untouched and a filtered
  log can never become the source of a batch number.
- `Options` gained a field whose zero value changes nothing, so every M4-T5 and M4-T6 test passes
  unmodified — which is the regression this design is most exposed to.

### Negative

- **Refusals over-refuse a narrowed run.** `migr down --step 1` still refuses if the *oldest* applied
  migration drifted, even though the run would never have reached it. For drift that is invariant #6
  and is right. For `missing` it is stricter than ADR 0022's own argument requires — that argument
  was about undoing everything older than the gap, which `--step 1` does not do. Option E2 would fix
  it and is rejected on timing: making a refusal depend on the scope means the answer to "is this
  database safe to migrate" changes with the flags, and that is a larger decision than this task.
- **`down --to` cannot express "undo everything."** Under A1 there is no version that names the state
  before the first migration. `--step N` with a large N does it, which is a worse spelling than an
  `--all` this CLI surface does not have. If that proves painful, the flag is the fix, not the
  reading.
- **The `--to` cut going up is a set membership test over the report**, so it is O(report) rather than
  O(1) and it reads less obviously as a prefix than the other two selectors. The alternative was
  refusing `up --to X` when X is already applied, which is the case a hotfix inserted behind X
  produces.

### Neutral

- **`migr down` with no arguments is not yet observable.** The command lands in M5-T2, which must pass
  `LastBatch()` when neither flag is given. `engine.LastBatch()` is built and tested here; the wiring
  is one line there, and it is recorded in `TASKS.md`, in `internal/engine/doc.go`, and here.
- All four refusals exit 1 for now. CLAUDE.md §5 has no code for a bad selector, and exit 2 is for a
  failure knowable with no database — which `ErrNotApplied` is not. M5-T6 owns the mapping.
- `Skipped` now means "this invocation did not run it", where it used to mean "reconciliation said it
  needed no run". Its `State` field is unchanged and still carries the reconciled state.

## Notes

M5-T3's `migr status` renders the batch a migration was applied under; that comes from
`Status.Recorded.Batch`, the same field `LastBatch()` reads, so the number an operator sees in the
table is the number `migr down` will act on.

M9-T6 resolves a dirty migration. A dirty migration refuses both directions before narrowing, so no
selector can step around one — which is what that task's "refuses to proceed until explicitly
resolved" requires.
