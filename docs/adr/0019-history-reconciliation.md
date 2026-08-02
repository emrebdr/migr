# 0019. Reconciliation replays the whole log, and a half-applied migration is a fifth state

## Status

Accepted

## Date

2026-08-02

## Context

[ADR 0013](0013-migration-history.md) made the history table an append-only log: every run appends
a row, including rollbacks and failures, and nothing is updated or deleted. It also recorded what
that costs — "status is derived rather than read. The fold from log to four states is real logic in
M4-T3, and a bug there misreports a database rather than failing loudly."

M4-T3 is that fold. Its inputs are the migrations `internal/migration` discovered and the runs a
`history.Store` read back; its output is one answer per migration. Three things had to be settled
before it could be written, and each was a decision rather than an implementation detail.

**A missing history table is reported by a driver, and the reconciler cannot see it.** M3-T6 made
PostgreSQL classify SQLSTATE `42P01` on reads as well as writes, precisely so that a typo in
`history.table` could not read as "no runs yet" and make a fully migrated production database look
virgin. But that sentinel was `postgres.ErrNoHistoryTable`, and invariant #1 forbids core from
importing a driver — `internal/history` importing `internal/driver` is an import cycle besides,
since the driver contract names `history.Store`. So a driver-neutral fold had no way to ask.

**A run's meaning depends on the runs before it.** `MarkFailed` records a run that failed *without
changing the schema*, which makes "a failed rollback leaves the migration applied" true — but only
if it was applied. The sequence `up/applied · down/applied · down/failed` reads as **applied** under
any rule that consults the last row alone, and the migration is **pending**.

**A half-applied migration is neither applied nor pending.** `MarkDirty` records a run that stopped
partway, and M4-T9 requires the next `up` to refuse until that is explicitly resolved. M4-T3's
done-when named four states, and neither of the two that could absorb a dirty run is true of one.

## Decision Drivers

- **Fail toward under-reporting.** Between a status that misses something and one that says a
  migration is applied when it is not, only the second re-runs a production database.
- **Two vocabularies stay apart.** `internal/history`'s package doc already separates `Outcome` — a
  fact about one run — from the derived state, and reserved the name `State` for this task.
- **A guard whose omission is silent is not a guard.** If a caller has to remember to check
  something before acting, the model has put the invariant in the wrong place.
- **The fold is testable to the point of a mutation sweep.** It is the piece ADR 0013 named as
  dangerous, so every case should be a literal.

## Decision

**`history.ErrNoTable` is part of the store contract.** Every `Store` must report a database with
no history table as an error unwrapping to it, on every read path. `postgres.ErrNoHistoryTable` is
deleted rather than aliased: two sentinels for one condition would let a caller check the
driver-local one, be right on PostgreSQL, and be wrong on SQLite.

**`Store` gains a `Reader` half.** `Reader` declares `List` alone and `Store` embeds it.
`Read(ctx, Reader)` is the only function reconciliation needs, and taking the read half states
structurally that reconciliation cannot write, cannot `Ensure`, and cannot repair — invariant #6
expressed as a signature rather than as a rule.

**Absence is a value whose zero is the safe reading.** `Read` maps `ErrNoTable`, and only
`ErrNoTable`, to an absent `Log`. `Log`'s fields are unexported, so `Log{}` is the only literal a
caller can write and it means "this database has no history table"; a present-but-empty log takes a
deliberate `NewLog(nil)`. `Report.Resolved()` returns `([]Status, bool)`, which will not compile
inside a `range`, so the check cannot be skipped by omission. An absent log yields zero statuses
rather than every migration pending.

**The fold replays every run in `(batch, ordinal)` order.** Applied changes what the database
carries, failed changes nothing, dirty leaves the schema between states:

| direction | outcome | effect |
|---|---|---|
| up | applied | carried, anchored on this run |
| up | failed | unchanged |
| up | dirty | dirty, anchored on this run |
| down | applied | given back, anchored on this run |
| down | failed | unchanged |
| down | dirty | dirty, anchored on this run |

`NewLog` sorts at construction, so every `Log` that exists is in run order however it was built.

**Dirty is a fifth state.** `applied · pending · missing · drifted · dirty`. The task's done-when is
amended rather than reinterpreted, per CLAUDE.md §11.

**`missing` means the database still carries it and no file describes it.** A migration applied,
rolled back, and then deleted emits no status at all — it is fully undone, and `migr history` still
prints its rows. A dirty run with no file reports `dirty`, because the schema being between states
dictates the next action rather than the file being gone.

**Precedence is stated once**, in `stateOf`: dirty, then not-carried, then no-file, then a source
digest that disagrees, then applied. Two consequences follow and are deliberate: a rolled-back
migration that was then edited is `pending` rather than `drifted`, since invariant #6 protects only
*applied* migrations; and a dirty migration whose file also changed reports `dirty`, since that is
the condition blocking everything and its repair needs the recorded row.

**Discovered migrations keep the order they were given**, which is `migration.Order`'s sequence and
therefore what `migr up` will run. Versions no file describes are appended, sorted by version.

## Considered Options

### Where the fold lives

#### Option A: `internal/history`

**Pros**

- CLAUDE.md §3 already assigns it "migration history store contract **+ reconciliation**", and the
  package doc reserved the name `State` for it while arguing both vocabularies belong together.
- Importing `internal/migration` makes the reverse an import cycle, so that package's stated
  stdlib-only leaf property stops being review-enforced — the same mechanism ADR 0013 used for
  `driver → history`.

**Cons**

- `internal/driver` gains a transitive dependency on `internal/migration`.

#### Option B: `internal/migration`

**Rejected because:** that package imports stdlib and nothing else, which is what lets `migr
validate` list and order migrations with no database. Pulling the history contract into it would
end that, and §3 assigns reconciliation elsewhere anyway.

#### Option C: A new `internal/status`

**Rejected because:** it would put `Outcome` and `State` in two packages and reopen exactly the
confusion the history package doc exists to close. §3 also enumerates the package set and has no
such entry.

### How "this database has no history" is made unmissable

#### Option D: An absent `Log`, and a comma-ok `Report`

**Pros**

- The dangerous default is the zero value, so a caller who builds a `Log` wrong gets the safe
  answer rather than the unsafe one.
- `Reconcile` stays a pure function of two values, so all forty-odd cases are literals and the
  mutation sweep is cheap.
- `Read` takes a one-method `Reader`, so a test fake is one method rather than six.

**Cons**

- Two calls where there could be one, and a `Resolved()` accessor rather than a plain field.

#### Option E: One `Reconcile(ctx, store, files) (Report, error)` returning `ErrNoTable`

**Pros**

- Impossible to bypass, and an ignored error is loud.

**Cons**

- The fold stops being pure: every case carries a fake store and a context, where every other
  fold-shaped thing in the tree (`migration.Order`, `engine.Groups`, `capability.Set.Resolve`) is
  pure.
- `migr status` on a fresh database becomes an error path that has to be caught to print something
  friendly.

**Rejected because:** the safety it adds over Option D is small — the comma-ok already cannot be
ignored by omission — and the testability it costs is the thing ADR 0013 said this fold most needs.

#### Option F: A pure `Reconcile(files, runs []Record)`, with the caller handling `ErrNoTable`

**Rejected because:** it makes the banned failure a caller's discipline. M3-T6 found that reading a
missing table as zero runs is how a migrated database looks virgin; leaving that one `if` to every
command is how it comes back.

### How many states

#### Option G: Five, with `dirty` folded like the rest

**Pros**

- `migr status` has something true to print for a half-applied migration, which under four states
  it does not.
- M4-T5, M4-T7 and M4-T9 each switch on `State` with no `default`, so `exhaustive` breaks all three
  the day a sixth arrives.

**Cons**

- "dirty" now names something on both axes. (So does "applied", and the package doc already blesses
  that: the two senses agreeing is the ordinary case rather than a confusion.)
- The task's done-when had to be amended.

#### Option H: Four states, with callers reading `Status.Recorded.Outcome.Blocking()`

**Pros**

- Honors the done-when literally and keeps `State` and `Outcome` strictly disjoint.

**Cons**

- Resolving dirty to `applied` makes `up` skip statements that never ran and `down` reverse
  operations that never happened. Resolving it to `pending` makes `up` replay statements that
  already succeeded and discards the `last_statement` M9-T6 resumes from. Both print something
  untrue.
- The guard is a check whose omission is silent, on the one condition that must block everything.
- `Blocking()` answers about one run. Once the fold is stateful the anchor is not always the last
  row, so the two can disagree.

**Rejected because:** a fifth value appears in `migr status`'s output either way; the only question
is whether the type system knows about it.

### What `missing` covers

#### Option I: Only versions the fold leaves carried or dirty

**Pros**

- The alarm keeps its meaning: the database holds schema this repository can neither describe nor
  roll back.

**Cons**

- A rolled-back, deleted migration disappears from `migr status` entirely. Its rows are still in
  `migr history`, but a reader has to know to look there.

#### Option J: Every recorded version with no file

**Rejected because:** it accumulates one permanent, unactionable row per migration ever deleted.
Unactionable noise is how operators learn to ignore an alarm, which is the failure ADR 0009 named
about exit code 4 and this ADR declines to repeat one layer up.

## Consequences

### Positive

- A database with no history table cannot be reported as one with nothing in it, and the guarantee
  is a type rather than a rule.
- The state is a pure function of the log and the files, so `migr status`, `migr up`'s pre-flight
  and `migr validate` cannot reach different answers.
- `Status` carries the recorded digest beside the recomputed one, so M4-T7 renders both without
  recomputing either, and `Recorded.LastStatement` is already there for M9-T6.
- A corrupt row stays an error unwrapping to `ErrCorrupt`, so it can be reported as needing manual
  repair rather than as drift.

### Negative

- **Every store now owes `ErrNoTable` on every read path, and nothing checks that mechanically.**
  The interface shape is unchanged, so a driver that forgets compiles. The driver conformance suite
  covers no history at all today, and a suite that did would need a live server, so this is stated
  in the contract and asserted per driver — PostgreSQL does so in both its unit and integration
  tests. M8 and M9 carry the obligation.
- A rolled-back, deleted migration is not reported by `migr status`. See Option I.
- `NewFile` hashes, so `internal/history` now depends on `internal/checksum` and on
  `internal/migration`.

### Neutral

- `Reconcile` emits two statuses when two files claim one version. `Discover` already refuses that,
  and an error here would be a second definition of a guarantee that already has one.
- There is no `StateCorrupt`. A store returns `(nil, err)` on an unreadable row, so the fold never
  sees one, and a state nothing can produce is a state that rots.
- There is no `State.Blocking()`. `Outcome.Blocking()` works because a dirty run blocks every
  command identically; `drifted` blocks `up` and `down` but not `status`, and `missing` blocks
  neither, so one bool would be three different answers.

## Notes

M13-T14 needs a representation for a shadow-table migration *waiting for confirmation*, and says so
against M4-T3's states by name. That is a sixth value on this axis rather than a flag, for the same
reason `dirty` is a fifth: `migr status` has to print something true for it, and `exhaustive` will
name every switch that has to grow.
