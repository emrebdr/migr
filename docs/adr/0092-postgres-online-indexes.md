# 0092. PostgreSQL builds and drops every index concurrently, and migr repairs what a stopped one leaves

## Status

Accepted

## Date

2026-10-21

## Context

M13-T11's done-when:
- `CREATE INDEX CONCURRENTLY`, `DROP INDEX CONCURRENTLY` and `REINDEX CONCURRENTLY` are used
  automatically where safe;
- that includes **the invalid-index cleanup path**: a failed concurrent build leaves an invalid index
  behind that silently degrades queries, so detecting and reporting it is part of the task.

M13-T5 (ADR 0085) rewrote `add_index` as `CREATE INDEX CONCURRENTLY`. It named the INVALID index a
failed build leaves, in `InvalidIndexError`, and left four things here:
- `add_unique` and `add_primary_key` through a concurrent index;
- the drop and REINDEX forms;
- the cleanup;
- lifting ADR 0084's rule that a statement run outside any transaction is never retried.

Reading the code for this task found two more:
- **`safety.statement_timeout` (30 s) bounded every concurrent build too**, so any large index failed
  and left an INVALID one;
- **introspection did not read `indisvalid`**, so `migr diff` and the preflight saw an INVALID index as
  a real one.

## Measured first

On compose's PostgreSQL 16 and 17:

| Question | Answer, identical on both |
|---|---|
| `ADD PRIMARY KEY USING INDEX` on a nullable column, 3 M rows | **87 ms / 80 ms**: it scans to prove NOT NULL |
| The same, behind a validated `CHECK (v IS NOT NULL)` | **1.8 ms**: the check stands in for the scan |
| `ADD CONSTRAINT … UNIQUE USING INDEX`, 3 M rows | 1.6 ms |
| `CREATE INDEX CONCURRENTLY` behind a running statement, `lock_timeout = 1s` | `55P03`, index left **INVALID** |
| `DROP INDEX CONCURRENTLY` behind a running statement, `lock_timeout = 1s` | `55P03`, index left **in place and INVALID** |
| `CREATE UNIQUE INDEX CONCURRENTLY` over a duplicate | `23505`, index left INVALID |
| `REINDEX INDEX CONCURRENTLY` of that index, duplicate still there | Fails, leaving **`w0_u_ccnew` INVALID** beside it |
| The same after removing the duplicate | Valid |

One thing was measured in the end-to-end suite rather than planned for:
- **A build waits only on transactions holding a snapshot.** An idle READ COMMITTED transaction holds
  none, so a live test's open transaction had to be running a statement.
- **The step-0 measurement itself left `w0_u` INVALID** in postgres17's `public` schema. `migr status`
  found and listed it, through the real CLI, before anything here was built to look for it there.

## Decision Drivers

- **No write blocked**, which the M13-T5 harness measures by a writer's 50 ms lock waits.
- **Never silent:** migr changing the database on its own is reported every time, and an INVALID index
  is reported wherever migr reads the schema.
- **REINDEX only where it cannot rebuild the wrong thing.**
- **A killed run has repaired nothing**, and is the case that matters most.

## Decision

### A. Three more operations become concurrent, under the one `concurrent_index` switch

- **`drop_index`** → `DROP INDEX CONCURRENTLY`, bare, `lock_free`.
- **`add_unique`** → `CREATE UNIQUE INDEX CONCURRENTLY` (bare), then
  `ADD CONSTRAINT … UNIQUE USING INDEX`. Cost `brief_exclusive`.
- **`add_primary_key`**, on 12 and later:
  1. per key column, `ADD CONSTRAINT …_nn CHECK (c IS NOT NULL) NOT VALID`;
  2. their `VALIDATE`s, bare;
  3. the concurrent unique build;
  4. `ADD [CONSTRAINT name] PRIMARY KEY USING INDEX`;
  5. drop the checks.

  Cost `brief_exclusive`, by the measurement above. An unnamed key's index is `<table>_pkey`, the name
  PostgreSQL would give it, shortened with a digest past 63 bytes.

One switch covers all four builds and drops: what an operator turns off is "build indexes
concurrently", not one operation's way of doing it.

### B. A statement that blocks no write is bounded by `safety.concurrent_timeout`

`driver.Statement.Unblocking` marks a statement that blocks no read or write once its own lock is held:
- every `CONCURRENTLY` statement;
- every `VALIDATE CONSTRAINT`.

The engine bounds such a statement by `safety.concurrent_timeout` instead of `statement_timeout`.
- Unset means no bound. `migration_timeout`, Ctrl-C, and `lock_timeout` on the lock it first waits for
  still apply.
- Set, it must be longer than `lock_timeout`.

A raw `no_transaction` statement is never `Unblocking`, since migr cannot vouch for what it does. The
golden header records the flag as `unblocking`, so a driver cannot change it without a diff.

### C. `driver.Repairer` cleans up after a statement no transaction rolled back

The engine asks it twice, and only about a statement outside any transaction.

**`AfterFailure`**, given the error:
- **A build's INVALID leftover** is dropped (`DROP INDEX CONCURRENTLY IF EXISTS`), and the statement
  is `Rerunnable`.
- **A drop, or a `VALIDATE`,** is `Rerunnable` as it stands.
- **The failure was the leftover itself** (the build met `42P07` on the INVALID index of a run killed
  before it wrote a row): the repair also says `Retry`, so the statement is tried again at once. A
  run killed during its first statement writes no history row, so the next `migr up` starts fresh
  rather than resuming. Without this, that run would have failed on the name and the one after it
  would have succeeded.
- **A valid leftover exactly matching the build**, met the same way, is `Done`: the statement counts as
  run.

**`BeforeResume`**, before a resume reruns the statement a stopped run last sent:
- **A valid, matching index** is `Done`, and the build is skipped.
- **An INVALID index that is exactly the one the operation builds** (its table, its columns in order,
  its uniqueness, no predicate, no expressions) is rebuilt with `REINDEX INDEX CONCURRENTLY` on 12 and
  later, then `Done`. If the rebuild fails, it and every `_ccnew` copy are dropped, and the run stops
  with the rebuild's error.
- **Any other INVALID index of that name** is dropped, and the statement reruns.
- **A drop whose index is gone** is `Done`.

**What the engine does with the answer:**
- A `Rerunnable` lock wait is retried with the ordinary backoff and `lock_retries`. A `Retry` is
  retried too, counted the same way. This lifts ADR 0084's rule for a repaired statement only.
- Every repair is reported: a warning, `Run.Repairs`, and `repaired: …` on `migr up`'s line.
- A repair that fails stops the run, naming both failures. `InvalidIndexError`'s manual remedy stays
  for exactly that case.

**Where REINDEX is used, and why only there.** REINDEX rebuilds an index as it is *defined*, so it is
right only where the definition is known to be the one wanted. Straight after a failure, dropping and
rebuilding is equally fast and shares the cause the build just met. Before a resume after a kill, the
leftover's definition is checked against the operation, so REINDEX is the one call that turns it valid
in place.

### D. An INVALID index is a fact the schema carries, and every report names it

- **`schema.Index.Invalid` and `schema.Unique.Invalid`**, set from `indisvalid`:
  - the preflight's name check does not count an INVALID index as taken, since it is a leftover the run
    repairs;
  - a marker beside it makes `migr diff` decline to compare it (ADR 0065).
- **`driver.InvalidIndexReader.InvalidIndexes`** lists every INVALID index in the namespace, migr's own
  tables excluded, with the server's definition.
- **The reports:**
  - `migr status`, `migr plan` and `migr validate --connect` print each one under *INVALID indexes*,
    with both remedies: `REINDEX INDEX CONCURRENTLY` and `DROP INDEX CONCURRENTLY`;
  - `--json` carries `invalid_indexes`;
  - `migr up` and `migr resolve` warn of any left after they run.

  An INVALID index migr did not make is reported and never touched: only one a statement of the
  running migration built or dropped is repaired.

## Rejected

- **Dropping inside `Exec`.** It is invisible to the engine, which could then neither retry nor report
  it. A statement that quietly runs a second one is the substitution ADR 0085 refused to make silent.
- **REINDEX in a pure `Rewrite`.** Whether a leftover exists is a fact about the database, and a
  rewrite reads nothing.
- **REINDEX straight after a failure.** It meets the same duplicate or the same lock wait, and leaves a
  second INVALID index when it fails.
- **Keeping `statement_timeout` on builds.** It turns the commonest large-table migration into a
  failure, which only the cleanup would hide.
- **A switch per form.** Four keys for one decision.
- **Repairing every INVALID index migr finds.** An operator's own failed build is theirs to judge.
  migr reports it.

## Consequences

- `driver.Statement` has a third field, rendered in golden headers. `driver.Repairer` and
  `driver.InvalidIndexReader` are optional, and only PostgreSQL implements them.
- `safety.concurrent_timeout` is new, unset by default.
- **A raw `no_transaction` `CREATE INDEX CONCURRENTLY`** is repaired too, with a drop and never a
  REINDEX, since nothing says which index it builds. The end-to-end suite holds that.
- **MySQL and SQLite change nothing.** MySQL picks its own online algorithm and has no INVALID state,
  and SQLite has one writer.

### What holds it

**Untagged:**
- each rewrite, its flags and its version gate;
- every branch of the repair decision;
- the engine's retry, skip and stop paths;
- the CLI's reports.

**On PostgreSQL 16 and 17:**
- the three new forms under a writer, with no write blocked;
- the same schema as the plain statements;
- a duplicate cleaned up and then resumed;
- a lock wait repaired and retried;
- a build outliving `statement_timeout`;
- a killed build rebuilt by REINDEX, a stranger index dropped, and a fresh run after a killed first
  build;
- detection with the marker.
