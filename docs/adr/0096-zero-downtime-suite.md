# 0096. Every operation's lock verdict is held to what a client under load actually waits

## Status

Accepted

## Date

2026-10-22

## Context

M13-T12's done-when:
- `test/conformance/zerodowntime` runs each op against every driver **under concurrent read/write
  load**, and asserts that no client query exceeds a configured latency budget;
- a driver failing the budget must declare that op's lock impact honestly. Declaring `TableRewrite`
  always passes; silently blocking never does;
- the cutover rename is inside the budget.

Before this, a verdict was held only by per-engine oracles:
- PostgreSQL's `relfilenode` and InnoDB's `TABLE_ID`, for "nothing rewrote that was not declared";
- MySQL's `ALGORITHM=INSTANT` acceptance;
- the lock-retry reader of ADR 0082.

None of them measured a client. §1 calls zero-downtime a verified property, and this suite is where
it is verified.

## Measured first

On every compose server and on SQLite, with eight readers and eight writers working the table:

| Question | Answer |
|---|---|
| A retype of 1M narrow rows, PostgreSQL 17 | 0.48 s, **inside** even the first 500 ms budget, so a lying verdict could hide. With a 192-byte pad and an index, 600k rows took 1.04 s. Under the suite's load, 1M such rows made a client wait 1.6–2.6 s. |
| Honest rewrites under load, across every engine, final configuration | 1.4–41 s |
| The noise floor (the worst query with nothing running), final run | 5–50 ms on every server, SQLite in WAL included |
| SQLite's noise floor in the default rollback journal | **2.0 s.** Eight writers wait on each other, so nothing can be judged at any useful budget. |
| The worst of 106 kept promises, final run | 206 ms: PostgreSQL's concurrent `add_index` over the wide pad |
| A shadow-table cutover under the load | 21–74 ms, and no committed write was missing from the swapped-in table |
| postgres16's `rename_column`, under the default 3 s `lock_timeout` | **A reader waited 1.0 s.** The ALTER queued behind autovacuum on the freshly seeded table until autovacuum's one-second `deadlock_timeout` made it yield. |
| MySQL's online NOT NULL rebuild of 1M rows, unthrottled clients | **Never finished.** Its log-apply stage gained as many changes as it applied (about 1,920 per 20 s). |
| The same rebuild, with paced clients, on mysql80gtid | 988 s, then **error 1799**: `innodb_online_alter_log_max_size` exceeded |

## Decision Drivers

- **What is judged is what the gate reads:** `plan.Step.Lock()`, assessed as a run assesses it.
- **What runs is what `migr up` runs:** every rewrite on, chunking, retries and assessment, through the
  engine.
- **No SQL above a driver** (invariant #3).
- A pessimistic verdict always passes; an optimistic one never hides.

## Considered Options

### A. What the budget holds
- **A1: one budget, 1.5 s by default or `MIGR_ZERODOWNTIME_BUDGET`, holding the directions whose
  worst step is `lock_free` or `brief_exclusive`** ✔ (by agreement):
  - **why 1.5 s:** 500 ms was agreed first. Then MySQL's `rename_table` held a write for 543 ms in one
    run of three, and MySQL's `lock_wait_timeout` takes whole seconds, so a brief statement there may
    hold clients up to 1 s by design. 1.5 s is that second plus a margin, raised by agreement, and the
    worst kept promise anywhere is 206 ms;
  - `long_exclusive`, `table_rewrite` and `unknown` say a client will wait, and pass;
  - a cheap operation declared at its worst is logged as an over-report and passes;
  - a server whose noise floor reaches the budget fails, since nothing on it can be judged;
  - a malformed override fails rather than falling back.
- A2: two budgets, one per promise. **Rejected:** more tuning and more flakiness, for a distinction no
  measured operation needed.
- A3: also failing `long_exclusive`. **Rejected:** the task makes an honest worst a pass.
- A4: asserting over-reports. **Rejected:** an over-report costs a flag, while an under-report costs
  an outage.

### B. Each operation, both ways
- **B1: eighteen cases, each with its setup unmeasured, and its change applied, then rolled back, both
  under load** ✔ (by agreement). Each `drop_*` is measured as the down of the `add_*`/`create_*` it
  undoes, through the engine's own derivation.
- Coverage is derived from `ir.Kinds()`: a kind no case reached fails the run. A kind the server refuses,
  by declaration or by its compiler, is an honest pass.
- The data operations run as data-only migrations, so in chunks, with an authored empty down.
- The shadow case runs `alter_column` through a shadow on every server that permits capture. Its
  cutover is confirmed from a second session once the copy syncs, so **copy, hold, catch-up and the
  rename all fall inside the window.**
- B2: up only. **Rejected:** half the coverage, and a down is where a derived inverse first meets a
  live table.

### C. The table and the clients
- **C1: one table of 1,000,000 rows, with a 192-byte pad and an index on `label`, reused across cases**
  ✔. At that size every engine's rewrite takes at least 3× the budget. Cases whose result doesn't grow
  with the table get a side table of 50,000 rows: rename, keyless, NOT NULL, the data operations and
  the shadow. The side table is created and seeded before its setup runs.
- **Tightening to NOT NULL runs on the side table everywhere** (by agreement), because MySQL's online
  rebuild cannot finish on the full table under load. A second case runs it on the full table **only
  where the session rewrites it**, which is PostgreSQL's validated check, the promise a full table is
  needed for.
- **Each client pauses 2 ms between queries** (by agreement): a busy application rather than a
  benchmark, roughly 4,000 queries a second across sixteen clients.
- **Sessions run with `safety.lock_timeout` at four fifths of the budget**, `zerodowntime.LockWait()`
  (agreed as below the budget; first half of it). A `brief_exclusive` statement waits for the lock it asks for, and every client behind it
  waits too, for as long as the lock timeout allows, so a promise held to the budget needs a lock
  timeout under it:
  - PostgreSQL runs at 1.2 s. **Not less:** autovacuum yields to a waiter only after `deadlock_timeout`,
    1 s. Measured, at 750 ms every attempt of a concurrent build and a VALIDATE on postgres16 gave up
    behind autovacuum before it was asked to yield, and the run stopped after six;
  - MySQL runs at its whole-second minimum of 1 s;
  - SQLite keeps the default. It has no lock queue, since its busy handler retries rather than lining
    clients up behind a waiting statement. Measured, a 250 ms `busy_timeout` let the load's own writes
    starve migr's BEGIN IMMEDIATE on the history table.
- **The table carries a second index, on the pad**, and `add_index` builds over `(pad, label)`.
  Measured:
  - PostgreSQL 17's retype took 3.6 s with nothing else running, over twice the budget;
  - a plain build over the pad took 2.2 s, where one over the all-NULL `extra` took 112 ms and let a
    lying `lock_free` pass;
  - not over `n`, which every write changes: MySQL's online build logs each change to an indexed
    column, and one over `(pad, n)` failed with error 1799.

### D. The load is the driver's
- **D1: a `Load` interface each driver's own test implements:** seed, read, write, a view body, a raw
  statement, drop, and "gone" ✔. The suite composes no SQL.
- D2: a SQL-composing suite. **Rejected by invariant #3.**
- Each query is timed from send to answer, and one still in flight counts for as long as it has waited.
- The load never cancels a query it sent: pgx closes a connection whose query's context is canceled,
  which broke every case's cleanup on the first run.

### E. SQLite's load
- **The load runs WAL with its writers on one connection, which is how an application runs SQLite
  under concurrent clients.** The default rollback journal's 2.0 s floor is SQLite's own write
  serialization, not anything migr does.

## Found by the suite, and what was done

1. **The SQLite driver's deferred transactions failed at once in WAL.** A migration's transaction
   reads and then writes, and a deferred BEGIN that another connection writes past fails with
   `SQLITE_BUSY_SNAPSHOT` (517) without waiting out `busy_timeout`. That broke every rebuild and every
   chunk.

   **Fixed:** the driver now begins every transaction IMMEDIATE, through `_txlock=immediate`. A
   connection string asking for `deferred` is refused, and one asking for `immediate` or `exclusive` is
   kept. Every BUSY extended code, and a BEGIN itself, is classified as `ErrStatementLockTimeout` and
   retried.
2. **`migr up --unattended` never cuts over under sustained writes.** It waits for a read that finds
   nothing new (ADR 0093), and eight writers never leave one.

   **Not fixed here:** it is M13-T14's behavior, recorded for that line of work in TASKS.md. The suite
   confirms its cutover as an operator would.
3. **MySQL's online rebuild starves under sustained writes.**
   - Unthrottled, it never finishes.
   - Paced, it took 988 s and then failed with error 1799.
   - Either way it outlasts the default 30 s `safety.statement_timeout`.

   It keeps its lock promise, `table_rewrite`, but a hot table may never get the change.

   **The suite raises its own statement bound to 10 min**, since it judges waits, never durations, and
   it runs the case on the side table (C). Documenting this is M13-T13's job.
4. **A `brief_exclusive` promise holds only under a short lock timeout.** migr's default 3 s
   `lock_timeout` lets one rename hold readers for 1.0 s behind autovacuum. **The suite runs at half the
   budget** (C). Documenting this is M13-T13's job.
5. **Two kinds of over-report:**
   - a chunked data operation is declared at its one-statement cost (`table_rewrite`,
     `long_exclusive`), and clients measured 20–40 ms;
   - a drop of a small table is declared `long_exclusive`, and measured in milliseconds.

   Both pass, by A1.

## Proving it catches a lie

A bounded mutation sweep lowered one real verdict, or broke one guarantee, at a time:

| Mutation | Caught by |
|---|---|
| PostgreSQL retype declared `brief_exclusive` | the retype case, a client waiting past the budget |
| PostgreSQL's concurrent index build made plain, its step still `lock_free` | **at first nothing**: the build over the all-NULL `extra` took 175 ms. The case now indexes the 192-byte pad, a `string(255)` so MySQL can index it, and a writer waited 2.3 s. |
| MySQL retype declared `brief_exclusive` | the retype case |
| SQLite rebuild declared `brief_exclusive` | the retype case |
| SQLite transactions begun deferred | the backfill case, 517 |
| MariaDB's cutover prober skipped | **not by this suite**, since nothing in its load holds a lock on the shadow and the rename queues correctly without the prober. M13-T17's stray-reader test is what catches it. |
| The budget ignored; in-flight queries not counted | the unit tests |
| The down never measured | the coverage check, every `drop_*` unreached |

A noise floor read straight after seeding measured 638 ms on mysql80gtid, the server still flushing.
So the floor is read after 2 s of unmeasured load, and every window lasts at least 50 ms: a view drop
finished between two paced queries and overlapped none.

## Decision Outcome

A1, B1, C1 and D1. The run is `make test-zerodowntime` (tag `zerodowntime`), and CI runs it after
`test-integration` in the integration job. Every compose server and SQLite pass.

## Consequences

### Positive
- §1's zero-downtime claim is a measurement: every lock-free or brief verdict on four engines held a
  client to milliseconds, and the shadow cutover's rename is inside the budget.
- A new operation fails the run until it has a case; a verdict lowered without reason fails where a
  client notices.

### Negative
- **It takes minutes and disk:**
  - about 18 minutes for a full run: 2.5 for both PostgreSQLs, 14 for the four MySQL and MariaDB
    servers, and 1.5 for SQLite, mostly honest rewrites of a million rows with two indexes;
  - three consecutive full runs on fresh databases passed;
  - about 7.5 GB across compose's volumes per full run, mostly binary logs;
  - a fresh CI runner pays that once, while a laptop running it repeatedly needs `docker compose down -v`
    between runs.
- **What it holds is migr under a lock timeout below the budget,** not migr's shipped 3 s default.
- **A latency budget is a wall-clock assertion.** On a slow machine it has to be raised by
  `MIGR_ZERODOWNTIME_BUDGET`, and the noise-floor check says when.
- It judges point reads and writes by key. A workload of long scans would wait differently, and is not
  measured.

### Neutral
- SQLite connection strings now carry `_txlock=immediate` unless they name `immediate` or `exclusive`
  themselves.
