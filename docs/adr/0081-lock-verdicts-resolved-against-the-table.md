# 0081. Lock verdicts are pessimistic where they depend on the table, and a session may lower them

## Status

Accepted

## Date

2026-10-15

## Context

M13-T1's done-when is that every operation resolves to a lock verdict derived from the connected
server's actual version, and that *"an undeterminable case reports the pessimistic verdict, never an
optimistic guess"*. Most of this landed one driver at a time. M3-T3 (ADR 0016), M8-T3 (ADR 0052) and
M9-T3 (ADR 0059) give every operation on every driver a verdict, built from facts each driver reads
once at `Open`, and each driver has a live instrument behind its verdicts: PostgreSQL's
`relfilenode`, and on MySQL InnoDB's `TABLE_ID` plus acceptance of `ALGORITHM=INSTANT`.

Three things still fell short of the done-when.

**MySQL and MariaDB guessed optimistically about the table.** `add_column` and `drop_column` were
`BriefExclusive` wherever the release supports an instant change. Whether the server *uses* the
instant algorithm also depends on the table, and nothing in the operation says what the table is.
Measured on 8.0.46 and 11.4.13, with 3,000 rows and the table id read before and after:

| Table | `add_column` / `drop_column` | default change, rename |
|---|---|---|
| plain | instant on both | instant on both |
| `ROW_FORMAT=COMPRESSED` | **rebuilt** on both (`INSTANT` is 1845) | instant |
| carrying a `FULLTEXT` index | **rebuilt** on both (1846 / 1845) | instant |
| at 64 instant row versions | **rebuilt** on MySQL (`INSTANT` is 4092); MariaDB has no such count | instant |
| dropping an indexed column | **rebuilt** on MySQL (1845); MariaDB drops it in place and keeps the table | — |

In every rebuilt case a bare `ALTER TABLE` gave no warning: the server quietly picked a slower
algorithm. ADR 0059's Notes recorded the row-version case as a known imprecision. M13-T1 forbids that
imprecision, and the measurement found two more cases like it.

One thing that looked like a fourth case is not one. An `ADD COLUMN` on an **empty** table moves the
id on MySQL even though the change is instant on a populated table. That rebuild copies nothing, so
it costs nothing, and the measurement harness at first read it as a rewrite because its insert had
silently failed.

**PostgreSQL's drops were never measured.** `drop_table`, `drop_index`, `drop_unique` and
`drop_primary_key` were `BriefExclusive`. ADR 0059 §F made MySQL's `drop_table` `LongExclusive`
because the tablespace is unlinked while the lock is held, and left PostgreSQL's answer to this task.
PostgreSQL unlinks a dropped relation's files at commit and releases its locks after that. Measured
on 16 and 17, each the median of several runs, commit included:

| | small | large |
|---|---|---|
| `DROP TABLE` | 2.5 ms at 224 kB | 22 ms at 471 MB |
| `DROP INDEX` | 1.6 ms at 136 kB | 30 ms at 760 MB |

An index over 90 identical characters stays small because of B-tree deduplication, and its drop
barely moved. The large-index row therefore uses distinct values.

**`unknown` was still permitted on any operation.** M2-T2 let a driver report `LockUnknown` until
this task, and the driver conformance suite accepted it on every allowed verdict.

## Decision Drivers

- A ceiling (`max_lock_impact`, `--require-safe`) must never pass an operation that rebuilds a live
  table: under-reporting costs an outage, and over-reporting costs a flag (invariant #13).
- `Set.Resolve` performs no I/O. That is what lets `migr plan` annotate every operation and lets
  version gates be tested without a database (ADR 0012).
- Where it can be shown, the verdict should still credit the instant change M9-T3 measured, because
  `add_column` is the operation people ask about most.
- migr must never need `PROCESS`: no deployment grants it for migrations.
- The wire protocol should not change for a fact no runtime can act on.

## Considered Options

### Option 1: A pessimistic verdict that a session may lower, by reading the table

`Resolve` reports the rebuild, together with the cheaper cost and the condition it holds under. An
optional session interface, `driver.Assessor`, reads the table and confirms the cheaper cost.

**Pros**

- `Resolve` stays pure, and its answer is always safe on its own.
- The instant change is credited exactly where the server will make it, and both directions are held
  to the table id.

**Cons**

- A second, I/O-bound step that callers must remember to ask. Nothing calls it until M13-T6.

### Option 2: Declare `add_column` and `drop_column` a rewrite on MySQL, with no lowering

**Pros**

- Simple, and honest.

**Cons**

- It discards M9-T3's INSTANT measurement for the operations it matters most for.

**Rejected because:** it is the verdict ADR 0059 called useless for its most common question. The
honest answer needs only a catalog read, and that read exists.

### Option 3: Read the table inside `Resolve`

**Rejected because:** a verdict that needs a round trip makes `Resolve` fallible and context-bound,
which is everything ADR 0012 designed out. It would also have nothing to read for a table that an
earlier migration in the same queue creates.

### Option 4: Keep the optimistic verdict and document it again

**Rejected because:** it is the optimistic guess the done-when forbids.

### Option 5: Send the condition over `host.capability.get`

**Rejected because:** a runtime has no table to read and could do nothing with it. The wire keeps
carrying `lock_impact`, now the pessimistic cost, so no protocol version moves.

## Decision Outcome

Option 1.

**`capability.Verdict` gains a cost that holds under a condition.** `Native(TableRewrite).Unless(
BriefExclusive, condition)` keeps `Lock()` at the rewrite, which is what every ceiling compares, and
adds `Lowest()` and `Condition()`. `Confirmed()` returns the verdict at its lower cost and keeps its
support axis and strategy. Every malformed call returns the verdict unchanged: a blank condition, an
impact that is not lower, unknown as the lower cost, or a refused verdict. `String()` names both
costs. JSON marshals only `Lock()`.

**`driver.Assessor` is optional on a session, as `driver.Expander` is.**
`Assess(ctx, op, verdict)` returns the confirmed verdict where the table meets the condition, and the
verdict unchanged where there is no condition, no table yet, or a fact it cannot read. It reads
through the session's open transaction, so a migration's earlier steps are visible.

**MySQL and MariaDB.** An `add_column` or `drop_column` that would otherwise be `BriefExclusive` is
declared a rewrite lowerable to `BriefExclusive` where:
- the table is not `COMPRESSED` and carries no `FULLTEXT` index;
- on MySQL from 8.0.29, the table has fewer than 64 instant row versions;
- on MySQL, for a drop, the column is in no index.

The row format comes from `information_schema.TABLES` and the indexes from `STATISTICS`, both
readable without `PROCESS`. The row-version count is only in `INNODB_TABLES`, which refuses a session
without `PROCESS` with error 1227. **Without the grant, a MySQL ≥ 8.0.29 add or drop therefore stays a
rewrite.** That is the pessimistic answer to a fact that could not be read, and the condition text
says so. A second compose user, `migr_plain`, holds exactly a deployment's grants, and a test holds
both servers to this behavior.

Versions below the instant release keep a plain rewrite with no condition, because no table could
make them cheaper. No other operation was measured to depend on the table, so none carries a
condition, and a unit test says so for every other kind.

**PostgreSQL's four storage-deleting drops are `LongExclusive`.** The rule was fixed before measuring:
only a cost that stayed flat with size would keep `BriefExclusive`. Both drops grew about ten-fold, so
`drop_table`, `drop_index`, `drop_unique` and `drop_primary_key` move. `drop_column`,
`drop_foreign_key`, `drop_check` and `drop_view` delete no storage and stay `BriefExclusive`. The
drivers now agree with each other: MySQL's `drop_table` and SQLite's drops were already
`LongExclusive`.

**`unknown` belongs to `raw` alone.** The driver conformance suite refuses `LockUnknown` on any
allowed verdict for another operation. It also refuses a lowered verdict whose condition is blank or
whose lower cost is not lower, which cannot be built, so this check is a backstop. All three drivers
already passed it.

PostgreSQL and SQLite implement no `Assessor`: nothing measured in either depends on the table.
PostgreSQL stores every non-volatile default whatever the table holds, and SQLite's verdicts already
assume its whole-file write lock.

## Consequences

### Positive

- No verdict any driver reports is cheaper than what the server measurably does to some table it
  could be asked about.
- The three drivers give one answer to one question about deleting storage.
- `--require-safe` (M13-T6) has a verdict it can gate on. M13-T6 needs a session for the cheaper
  answer, and it has one.

### Negative

- **Until M13-T6, nothing calls `Assess`.** `migr plan` prints no lock axis, and
  `host.capability.get` now sends a MySQL `add_column` as `table_rewrite`. That is pessimistic and
  true, but a Go or TypeScript migration that branched on it would see a different answer than
  before.
- **MySQL ≥ 8.0.29 without `PROCESS` never lowers.** That deployment pays a flag on every column add
  and drop, and the only remedy is a grant migr otherwise never asks for.
- **A plan that has not run yet can only assess the first queued migration**, for the reason
  `migr plan`'s `~` marker states: a table that an earlier migration in the queue creates or changes
  is not yet what it will be.
- The PostgreSQL drop measurement ran on Docker's filesystem. A filesystem that unlinks faster is
  still `LongExclusive` here, and that over-report is accepted.

### Neutral

- ADR 0059's Notes paragraph, about the 64-version ceiling, and the question its §F left open are
  answered here. ADR 0059 is otherwise unchanged and stays Accepted.
- `test/initdb/unprivileged-user.sql` runs on a fresh volume only, so an existing compose volume needs
  `down -v` once.

## Notes

The brief/long boundary is still a duration under contention, and M13-T12's suite under load is what
measures it. A verdict here names how long a lock is held, and the instrument behind each one is a
rebuild or a delete measured on its own.
