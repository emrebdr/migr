# 0022. A rollback inverts the whole batch before it runs any of it

## Status

Accepted

## Date

2026-08-03

## Context

[ADR 0021](0021-apply-executor.md) built the apply queue: lock, ensure, reconcile, allocate a batch,
then per migration emit → compile → group → run → record-before-commit. It chose a **lazy** seam —
operations are emitted only for the migrations that will run, one at a time — and recorded the cost
honestly: a migration that will not load is discovered only after the ones before it have applied.

Rolling back cannot pay that cost, and the reason is in M4-T6's own done-when: *"an irreversible
migration halts with a clear message before doing anything."*

The repository arrived at this task **committed to two incompatible mechanisms for producing a
migration's down operations, and nothing in it resolves the contradiction.**

Derivation is assumed by [ADR 0008](0008-reverse-operations.md), whose Context opens *"Rolling a
migration back means running the inverse of what it did, in reverse order. For that to be mechanical
rather than hand-written, every operation has to answer one question: what undoes you?"* and whose
Decision Outcome says *"the list is inverted and re-ordered only if every operation in it can be
inverted, so the rollback executor refuses before acting rather than stopping halfway."* M1-T5's
completion note says the same (*"under M4-T6 any migration adding a unique constraint would have
blocked `down` for its whole batch"*), as does M3-T7's (*"which is the property M4-T6's 'halts
before doing anything' will rest on"*), as does `docs/ir.md` §8.3.

An authored `Down` is assumed by CLAUDE.md invariant #10 (*"Every migration has `Up` and `Down`"*),
by `engine.Source`'s `Emit(ctx, m, ir.Direction)`, by the runtime protocol's
`migration.execute {id, direction, mode}`, by every M6 and M7 SDK task, and by `ir.Raw.Reverse`'s own
refusal message: *"write the undo as a raw operation in Down"*.

`ir.ReverseAll` had **zero production callers**. `history.Reconcile` had been able to fold `down`
rows since M4-T3 and had never been handed one outside its own tests. `Store.MarkFailed` on a down
run is what makes `internal/history`'s *"a failed rollback leaves the migration applied"* true, and
nothing had ever produced one.

Three further things turned up while the queue was written.

**"Newest first" is neither version order nor the reverse of the reconciled report.**
[ADR 0018](0018-declared-dependencies.md) lets a declared dependency point backwards in time — that
is its stated use case — so a later-timestamped migration can run first. And `Report.Resolved()`
returns the order the *current* files would run in, which a dependency edge added after both applied
makes disagree with what actually happened.

**A `missing` version cannot be stepped around.** `migr up` skips a version the database carries with
no file, because there is nothing to run for it. A rollback runs newest first, so such a version sits
in front of every migration older than it.

**The fixture could not be reversed.** Every staged operation in the engine's tests was an
`ir.DropTable` with no `from`, which is irreversible by construction — so nothing in the test suite
could have exercised a rollback at all.

## Decision Drivers

- **A rollback that stops halfway leaves a schema no migration describes**, which is worse than one
  that never started. ADR 0008 states this one level down for a single operation list.
- **Irreversibility is a property of the migration, not of the run**, so it is knowable before
  anything happens.
- **Three SDKs must not reach three answers about one migration** — the argument that put statement
  grouping in `internal/engine` rather than in each driver.
- **Invariant #10 is a promise to migration authors**: a `Down` someone wrote must not be quietly
  replaced by one this framework guessed at.
- **`engine.Options` is unchanged** (M4-T6's own text), so whatever is added is a sentinel or a
  behaviour, not a field.

## Decision

**Every inverse is produced, compiled and refusal-checked before the first statement runs.**
`reconcile` → `classifyDown` → **`invert` the whole queue** → `allocateBatch` → run. A rollback is
two-phase where an apply is lazy, and that is the one structural difference between them.

**The source is asked for the down; the engine derives one when it authored none.**
`Source.Emit(ctx, m, ir.Down)` first. A source that returns `engine.ErrNoDownAuthored` gets
`Emit(ctx, m, ir.Up)` inverted with `ir.ReverseAll`. Derivation lives above the SDK line.

**A rollback undoes in `(Batch, Ordinal)` descending order**, read from the run that applied each
migration — the exact inverse of `history.byRun`.

**A `missing` version refuses the whole rollback run**, where `up` skips it.

**A down row's `Checksum.IR` covers the operations the down run emitted**, not a copy of the up
row's.

**`allocateBatch` runs after `invert`**, so a refused rollback reports batch zero — which is what
that field's doc already promised.

**An irreversible rollback exits 1.** `internal/cli/exit.go` is not touched.

## Considered Options

### A. When the inverses are produced

- **A1: eagerly, the whole queue, before the first statement** ✔.
- A2: lazily, per migration, as an apply does. **Rejected on merit.** A rollback runs newest first,
  so an irreversible migration at position three is met only after positions five and four have been
  undone — leaving a database carrying migrations whose successors are gone. That is the state the
  done-when exists to prevent, and ADR 0008 already refuses the same shape one level down by failing
  `ReverseAll` whole rather than returning a partial list.
- A3: a reversibility pre-flight, then a lazy run. **Rejected**: it doubles the runtime subprocesses
  M6 will spawn, and the pre-flight's verdict is reached against different database state than the
  run's — a guarantee that can lie is worse than no guarantee.

The price is stated in Consequences: a down is emitted while the migrations after it are still
applied, which is the mirror image of the reason an apply must stay lazy.

### B. Where a migration's down comes from

- **B1: ask the source for `ir.Down`; derive with `ir.ReverseAll` when it reports none** ✔. Both
  halves of the contradiction in Context are true, in different roles: the authored down is what
  invariant #10 promises, and derivation is what makes rollback mechanical for the migrations that
  did not write one.
- B2: always derive from `ir.Up`. **Rejected**: it makes `Source.Emit`'s direction dead, and it
  contradicts invariant #10, the runtime protocol, and every SDK task that exposes `Down` as
  something an author writes. It would also run a derived inverse over the top of a `Down` someone
  wrote and reviewed.
- B3: always ask for `ir.Down`; derivation is each SDK's business. **Rejected**: three SDKs would
  reimplement inversion independently and could reach three different inverses for one migration,
  which is the framework's central claim rather than a detail. `ir.ReverseAll` would also keep its
  zero callers, and M4-T6's own text would be wrong rather than implemented.
- B4: derive in the driver. **Rejected**: invariant #1, and three drivers must not reach three
  answers either.

### C. How a source says it authored no down

- **C1: `engine.ErrNoDownAuthored`, a sentinel beside the interface that consumes it** ✔.
  `history.ErrNoTable` is the precedent exactly — defined beside `Store`, owed by every
  implementation.
- C2: a third return value, or a `HasDown` method. **Rejected**: `Emit`'s signature and `Options` are
  unchanged by the task, and a bool that is false at every `Up` call site is the stub shape ADR 0011
  rejects.
- C3: `nil, nil`. **Rejected**: a deliberately empty down becomes indistinguishable from an absent
  one, and would be silently replaced by a derived inverse its author declined.
- C4: the sentinel in `internal/ir`. **Rejected on balance** — every source already imports `ir` for
  `ir.Direction`, so it is the cheaper import. But a contract whose interface is in one package and
  whose sentinel is in another has two homes. The cost is recorded as a Negative, and this is the
  move if it ever bites.

### D. What order a rollback undoes in

- **D1: `(Batch, Ordinal)` descending, from the run that applied each migration** ✔. It is the
  recorded fact, it is the history table's own primary key so ties are unrepresentable, and it is
  `history.byRun` reflected.
- D2: the reverse of `Report.Resolved()`. **Rejected**: that is the order the *current* files would
  run in. Add a dependency edge after both migrations applied and it silently disagrees with what
  happened.
- D3: version descending. **Rejected**: ADR 0018 makes a backwards-pointing dependency the use case,
  so version order is not apply order. The concrete failure is undoing a migration while the one
  built on top of it is still applied.

### E. What a `missing` version does to a rollback

- **E1: refuse the whole run** ✔.
- E2: skip it, as `up` does. **Rejected**: the deciding case is `migr down` against a misconfigured
  `migrations.dir` — every applied version reconciles to `missing`, and a skip rule reports "nothing
  to undo" against a fully migrated database. `migr up` reporting "nothing to do" there is harmless;
  `migr down` doing so is the operator concluding the tool works. Stepping around a gap also undoes
  the migrations older than it while it stays, which is a schema nothing describes.

A version applied, rolled back and then deleted is unaffected: reconciliation emits no status for it
at all, so there is nothing here to refuse.

### F. What a down row's `Checksum.IR` covers

- **F1: the inverse operations the run emitted** ✔. [ADR 0009](0009-migration-checksum.md) gives
  that half exactly one job — recording what one execution emitted — and this execution emitted the
  inverse.
- F2: a copy of the up row's digest. **Rejected**: the row would lie about what it did, and there
  would be no value anywhere answering "did the down that ran match the down `migr plan` printed".

The source half is recorded but never compared on a down row: `stateOf` reaches its drift branch only
when the migration is still carried, and a completed down leaves it uncarried.

### G. Are re-emitted `Up` operations checked against the recorded IR digest

- **G1: no** ✔, with the hazard stated plainly in Consequences.
- G2: refuse when they disagree. **Rejected**: ADR 0009 forbids the IR half deciding anything, and
  its own notes record that the emitted-IR digest depends on encoder behaviour. An encoder change
  would refuse every derived rollback of anything an older build applied, with no override in
  existence. ADR 0008 already names the real fix — verify the inverse against the live schema — and
  that needs introspection (M11-T2).

## Consequences

### Positive

- `migr down` exists as a function, and apply-then-rollback restoring the schema digest exactly is
  asserted against two live servers.
- `ir.ReverseAll` has a caller; `history.Reconcile` is handed a down row; `Store.MarkFailed` produces
  the "a failed rollback leaves the migration applied" case the history package has documented since
  M4-T3.
- Every refusal a rollback can make — irreversible, missing, drifted, dirty, and all four of
  `checkRunnable`'s — now fires before the first statement of the first migration. That is strictly
  stronger than the apply path, where `checkRunnable` fires per migration as the queue reaches it.
- `Apply` and `Rollback` share one options check, one lock bracket, one reconcile, one prepare and
  one perform, so invariant #11's deferred release is written exactly once.
- The staged test fixture is reversible for the first time, which is what made any of this testable.

### Negative

- **A down is emitted while the migrations after it are still applied**, not against the state it
  will run against. This is the mirror image of A2's argument and there is no shape that avoids
  both.
- **Deriving an inverse re-emits `Up` against a database where it is already applied.** ADR 0009
  permits a migration to branch on live query results, so one that emits differently the second time
  yields an inverse of something that never ran. The scope is narrow — `applied` guarantees the
  source is byte-identical, so only a live-state branch can do it, and only for a migration that also
  authored no down, which invariant #10 says it should have — but it is real and unrecoverable from
  the history, which records only a digest. G2 was rejected as the fix; M11-T2's live verification is
  the fix ADR 0008 itself proposes.
- **A version's rows can now carry two different IR digests**, so nothing may compare IR halves
  across rows of one version. Nothing does; M5-T3's `migr history` renderer must not start.
- **`engine.ErrNoDownAuthored` makes a source import `internal/engine`**, which retires ADR 0021's
  note that a source satisfies the interface structurally without doing so.
- The migration lock is held across every emission in the queue, which under M6 is a runtime
  subprocess per migration.
- An irreversible rollback exits 1, which is indistinguishable from a runtime error to anything
  reading exit codes. Exit 2 would be the honest answer for a failure knowable with no database, and
  reversibility *is* knowable that way — but only in `migr validate` (M5-T5), which can pre-flight it
  without needing to know what is applied.

### Neutral

- `engine.Rollback` undoes everything the database carries. `--step`, `--to`, and the "last batch"
  default are M4-T8's, and nothing reaches this from a command line until M5-T2, which depends on
  M4-T8.
- No new irreversibility sentinel: `ir.ErrIrreversible` survives every wrapper through `errors.Is`,
  which is the same reasoning ADR 0021 used to reuse `migration.ErrDuplicateVersion`.
- `MigrationError.Operation` is nil for an irreversible refusal. `ReverseAll` deliberately returns
  nothing but an error, and re-walking the operations to recover the value would re-run the walk that
  just failed. The kind and the reason are in the message, which is what invariant #9 asks of one.

## Notes

The inverse of a migration groups independently of the migration itself: `CREATE INDEX CONCURRENTLY`
is standalone and its `DROP INDEX` is not. So a migration that applied cleanly may have a down that
`checkRunnable` refuses, and M13-T4 deletes those two preconditions for both directions at once.

M4-T8's narrowing lands in both `classify` and `classifyDown`, after the state switch and before
`number` — `--step 2` means "the first two pending" one way and "the last two applied" the other.
