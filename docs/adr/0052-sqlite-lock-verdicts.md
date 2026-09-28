# 0052. SQLite's lock verdicts, and why an emulation cannot be declared before it can be compiled

## Status

Accepted

## Date

2026-09-28

## Context

M8-T2 left every operation this driver compiles declared `Native(LockUnknown)`. The support axis was
derived from the compiler ([ADR 0015](0015-payload-dependent-refusals.md)); the lock axis — what an
operation costs a live database — was not answered at all. M8-T3 answers it, makes the support axis
version-aware, and runs into a collision its own done-when did not anticipate.

**The collision.** M8-T3's done-when asks for `drop_column`, `alter_column` and the constraint
changes to be *declared `Emulated` with named strategies*. `capability.Emulated` is an **allowed**
verdict, and the driver conformance suite asserts `Resolve(op).Allowed()` is true exactly when
`Compile` succeeds — a biconditional it exists to prove, and which a driver does not get to fork. A
12-step table rebuild needs the table's current definition read out of `sqlite_master`, and
`Compile` is pure by contract: no context, no connection ([ADR 0011](0011-driver-contract.md)). So
the seven rebuild operations cannot be declared `Emulated` and refused by the compiler in the same
commit. [ADR 0051](0051-sqlite-type-map-and-the-rebuild-boundary.md) recorded this as the note
M8-T3 would need; this is that decision.

**The lock axis is where SQLite differs from PostgreSQL most.** SQLite takes **one database-wide
write lock**. There is no per-table locking, so an operation on one table blocks writers everywhere —
and in the default rollback-journal mode, which this driver does not change (ADR 0050), it blocks
readers too. CLAUDE.md's four verdicts were written against an engine with per-relation locks, and
mapping them onto a single-writer engine is the substance of this task.

## Decision Drivers

- Invariant #13: lock impact is **measured, not assumed**, and a driver that reports the wrong
  verdict is worse than one that reports `Unsupported` — it turns a "safe" migration into an outage.
- `--require-safe` (M13-T6) exits 5 on an operation that exceeds `safety.max_lock_impact`, so a
  verdict is a gate rather than a label.
- The driver conformance suite's biconditional is the only mechanical check that a driver's two
  answers agree, and it must hold at every commit.
- CLAUDE.md §11: if a task turns out to be wrong, fix the task rather than quietly doing something
  else.

## Considered Options

### A. Where the `Emulated` verdicts land

**A1. Name the strategy here, flip the verdict in M8-T4.** The seven keep refusing; the refusal
carries the strategy string M8-T4's `capability.Emulated` will carry, as a shared constant so the
two cannot drift. TASKS.md moves the `Emulated` declaration into M8-T4's done-when, which is the
commit that can produce the statements the verdict promises.

**A2. Build the seam an emulation needs, here.** An optional `Expand(ctx, op) (ir.Operations, error)`
on `Conn`, consulted by the engine before `Planner.Compile`, decomposing one operation into neutral
ones against the live schema. `Emulated` becomes keepable immediately, the whole rebuild stays
visible in `migr plan`, and M13-T5's `auto_rewrite` wants the same shape.

**A3. Merge M8-T3 and M8-T4.** Every done-when met at once, in one very large change.

### B. The cheapest honest verdict on a single-writer engine

**B1. Nothing is `LockFree`; `BriefExclusive` is the floor.** Even `CREATE TABLE` takes the
database-wide write lock.

**B2. Keep `LockFree` for operations that touch nothing existing**, mirroring PostgreSQL's table,
and record the single-writer caveat in `doc.go` rather than in the verdicts.

### C. How the verdicts are held to the engine

SQLite is in-process, so a live database costs a `t.TempDir()`. Two instruments are available and
neither is a clock:

- **the rollback journal**, whose size after a statement is the number of pages it rewrote *in
  place* — the signature of touching rows that were already there;
- **`PRAGMA page_count` and `PRAGMA freelist_count`**, whose deltas are the pages it allocated and
  released — the signature of building an index or dropping a table.

**C1. Run every operation at two table sizes and classify by what scaled.** Constant work is
`BriefExclusive`; work that scales without rewriting is `LongExclusive`; a journal that scales is
`TableRewrite`. Two controls calibrate it: an `UPDATE` over every row and an `UPDATE` over one row.

**C2. Declare from documented behaviour** and leave verification to M13-T12's `zerodowntime` suite.

## Decision Outcome

**A1, B1, C1.**

```
create_table    brief_exclusive     drop_table    long_exclusive     drop_column  table_rewrite
create_view     brief_exclusive     add_index     long_exclusive     the seven    table_rewrite
drop_view       brief_exclusive     drop_index    long_exclusive     raw          unknown
add_column      brief_exclusive     add_unique    long_exclusive
rename_table    brief_exclusive     drop_unique   long_exclusive
rename_column   brief_exclusive
```

**`drop_column` is `table_rewrite`, and the measurement is what found it.** SQLite's native
`DROP COLUMN` is not a metadata change: it rewrites the whole table content to omit the column. Its
journal grew 6.1× for a 10× larger table, against 7.7× for an `UPDATE` that rewrites every row and
1.0× for one that touches a single row. So the operation M8-T2 compiles to one short statement is
the most expensive thing in the table.

**`drop_index` and `drop_unique` are `long_exclusive`, and the plan for this task had them brief.**
Freeing an index walks its b-tree: the freelist grew 1 → 4 pages across the same size step while the
journal stayed flat. Work proportional to the table, without rewriting a row — which is exactly what
separates `long_exclusive` from the two verdicts either side of it.

**The first instrument chosen could not see the most important row.** Page and freelist counters
alone reported `drop_column` as disturbing *nothing*: a rewrite leaves the pages allocated with free
space inside them, so neither counter moves. The journal is what sees it. Recorded because the
obvious measurement was the wrong one, and a test built on it would have declared the most expensive
operation in the driver `brief_exclusive` while appearing to have measured it.

**The support axis is resolved against the detected library.** `rules` and `verdict` are now methods
on `serverVersion`, and `drop_column` is refused below **3.35** with a reason naming the release, the
library, and the rebuild. This is ADR 0015's recorded asymmetry in the same direction as
PostgreSQL's `uuid` default: the compiler emits the same statement either way, so the declaration is
the only place a version-dependent answer can live — and it is the one place this driver's
biconditional can break. It is unreachable through `Open`, because `interpretVersion` floors the
library at 3.25 and `modernc.org/sqlite` links 3.53, so the conformance suite runs over a live
session and never meets it.

**For this driver the version is a fact about the build, not about the database.** The library is
linked into the binary, so `AtLeast(3, 35)` is true for every session this build can open and the
branch above is dead until the dependency is pinned older. It is written and tested anyway, against
a `serverVersion` constructed directly — which needs no database, exactly as PostgreSQL tests its
version-dependent cases. Invariant #13's *asked, never hardcoded* is what is being kept.

CLAUDE.md §4.2 and [ADR 0012](0012-capability-resolution.md) both use SQLite 3.35's `DROP COLUMN` as
the canonical example of a verdict that must be resolved at runtime. For an in-process engine that
is a weaker sentence than it reads as, and CLAUDE.md now says which is true.

**Lock impact does not vary by version here**, which is worth stating because the support axis does:
the operations whose cost changed across SQLite releases are the ones this driver refuses, and
`drop_column`'s *support* moves with the version while what it costs does not.

## Consequences

### Positive

- **Invariant #13 is mechanical for the first time.** Every verdict is held to what the operation
  did to the database file, at two table sizes, inside `make check` and on both CI legs. A SQLite
  release that changed `DROP COLUMN`'s implementation would fail a test rather than quietly make a
  documented claim false.
- The biconditional holds at every commit, so the conformance suite keeps meaning what it means.
- M8-T4 inherits both the strategy string and the impact constant, so it flips a verdict rather than
  deciding what a rebuild costs while implementing one.
- `--require-safe` will have something to gate on for this engine the day M13-T6 lands, and it will
  refuse `drop_column` — which is correct, and which nobody would have guessed from the statement.

### Negative

- **M8-T3's headline done-when is met one task later than written.** The `Emulated` declaration is
  the part that needs a compiler, and this task does not have one for it.
- **The measurement counts work, not time.** It cannot separate a brief exclusive lock from a long
  one by duration — it separates them by how many pages were touched, which is the thing the verdict
  is *about* but not the thing an operator feels. M13-T12's `zerodowntime` suite is still owed.
- **The seeded tables cost about two seconds** in `make test`. That is the price of the layer, and
  it is paid on every run.
- **A `brief_exclusive` floor may read as pessimistic** to an operator who has put the database in
  WAL mode themselves, where readers are not blocked. The verdict describes the mode migr found.

### Neutral

- Nothing outside `internal/driver/sqlite` changed. `internal/cli` renders the support axis and not
  the lock axis (M13-T6), and its tests use their own fake driver.
- `migr plan` on a SQLite project still stops at the migration lock, which is M8-T5, so these
  verdicts have no CLI surface yet.

## Rejected

**A2, building the `Expand` seam now.** It is the right shape and probably what M8-T4 wants — but
designing a driver-contract change before the thing that needs it exists is how a seam ends up
fitting nothing. Writing the rebuild is what reveals what the seam must carry: whether it returns
neutral operations or statements, whether it runs before or inside the transaction, and what
`migr plan` shows for a decomposition whose shape depends on the live schema. M8-T4 owns that ADR.

**A3, merging the two tasks.** M8-T4's done-when is an integration suite proving no referencing
object is lost across a rebuild. Folding a contract change, the rebuild SQL, and this declaration
into one commit makes all three harder to review and impossible to bisect.

**B2, keeping `LockFree` for metadata-only operations.** It would read the same as PostgreSQL's
table and be false: a concurrent writer *is* blocked, which is precisely what `lock_free` promises it
is not. Worse, it is a promise `--require-safe` acts on — an operator setting
`max_lock_impact: lock_free` to mean "nothing that touches production" would get a `CREATE TABLE`
that stops every write in the database. A caveat in `doc.go` cannot fix a verdict that a gate reads.

**C2, declaring from documented behaviour.** The documentation would have got `drop_column` right
and `drop_index` wrong, and nothing would have said so. For the one driver where the measurement is
cheap, not making it is choosing to leave invariant #13 a claim.

**Measuring with page counters alone.** They cannot see a rewrite in place, which is the verdict
that matters most. Kept alongside the journal rather than replaced by it, because they are what sees
an index being built and a table being freed — neither of which touches a page the journal records.

## Notes

Revisit B1 if migr ever manages `journal_mode` (ADR 0050 left that to the operator). In WAL mode
readers do not block, and the floor for a read-heavy workload would genuinely be different — though
the single-writer rule, and therefore the verdicts' ordering, would not change.

Revisit C1's thresholds if they prove brittle. They are calibrated against the two controls rather
than tuned to the operations, so the failure mode is a control moving, which fails loudly.
