# 0089. A shadow-table migration is four recorded stages, and the hold between copy and cutover has no end of its own

## Status

Accepted

## Date

2026-10-20

## Context

M13-T9 asks for the shadow-table lifecycle "modelled in the engine as a driver-implementable strategy":
- shadow table creation;
- chunked backfill;
- log-based change capture;
- an indefinite in-sync hold;
- cutover;
- retirement.

It also asks for two properties:
- **every stage independently resumable from a recorded stream position**;
- **an abort path that leaves behind no shadow table and no capture resource**.

[ADR 0014](0014-online-change-capture.md) had already decided the shape:
- capture reads the replication log, with no triggers (A2);
- the stream is opened by the `Driver` and owns its own connection (B2);
- the cutover waits for a confirmation (C3);
- the original is retained under a derived name (D2).

What 0014 left open is how the engine runs the stages, what it records and when, and how a run that stops
anywhere is carried on or abandoned.

No driver reads a log yet: MySQL's binlog reader is M13-T10 and PostgreSQL's logical decoding is
M13-T15. So the lifecycle is proven against an in-memory server in `internal/engine` and a fake in
`internal/cli`, each of which can be killed at any point. Against compose it is proven only as far as
the honest refusal, which [ADR 0090](0090-capture-capability.md) covers.

## Decision Drivers

- A run may stop anywhere, including by `SIGKILL`, and the next one must read only what committed.
- **Sync is a state that is held, not a phase that ends** (ADR 0014). A design that runs to completion
  cannot be retrofitted into one that waits.
- A capture resource left behind is an outage: an abandoned PostgreSQL slot retains WAL until the disk
  fills.
- A migration must never start waiting for a person unless its author asked for that.

## Considered Options

### A. What selects a shadow table
- **A1: the migration declares it** ✔ (by agreement):
  - `migration.discover` gains an optional `online` member, `"shadow_table"` being the one strategy;
  - Go uses `migr.Online` and `migr.ShadowTable`, and TypeScript `online: SHADOW_TABLE`;
  - an unknown strategy is refused at discovery, exit 2;
  - an optional result member is no protocol break (§10.2), as `phase` wasn't;
  - a `.sql` project cannot declare it, every operation there being `raw`, which no shadow can carry.
- A2: automatically above `safety.max_lock_impact`. **Rejected**: an existing large `ALTER` would start
  waiting for a confirmation nobody knew to give.
- A3: a project-wide switch. **Rejected** for the same reason, one project at a time.

### B. The stages, and what records each
The stages are `copying`, then `syncing`, then `cut_over`. Retirement deletes the row. Each stage is
recorded in `<history.table>_shadows` **in the transaction of the work it reports**:

| Stage | Committed in one transaction |
|---|---|
| Start | the empty shadow, already changed (same transaction where DDL is transactional, the DDL first where it is not, as M13-T4's parts do), the state row (stage `copying`, position the stream's `Start`), and the dirty history row |
| Copying | each chunk with its cursor and row count |
| Copying and syncing | each read of the log with its `Through` position |
| Cutover | the driver's swap, a catch-up read under its lock, stage `cut_over`, and the applied row |

**The stream opens before the copy reads a row**, so every write the copy misses is in the log after
the recorded start.

**Each copy chunk is an insert-ignore, and each captured change is an upsert or delete by key, carrying
the whole row.** So the interleaving is correct in any order, and replaying from a recorded position
after a crash rewrites rows to values they already hold.

**A start that recorded nothing is cleaned by the derived names.** A fresh start drops a leftover
shadow before creating one, and a failure before the record drops the copy and releases the stream.

### C. The hold
Once a copy chunk reports `Done`, the stage becomes `syncing`. The engine then loops:
1. read the log;
2. apply the batch with its position;
3. report the batch's `Behind`;
4. read the state row's `confirmed`;
5. sleep a second if nothing arrived.

- **`safety.migration_timeout` bounds the copy and never the hold**, because sync is a state rather
  than a phase.
- **Ctrl-C detaches**: the stream is `Close`d, keeping its resource; the state stays `syncing`, and
  the next `migr up` reopens the stream from the recorded position.
- **The confirmation is a column the engine reads and never writes.** A save leaves it as it is, so a
  confirmation written from another process is never overwritten. In this task only tests write it;
  `migr cutover --confirm` is M13-T14's.
- **The migration lock is held across the hold**, as for any run. Whether confirming needs that lock is
  M13-T14's question.

### D. Contract
- **`driver.Driver.Capture(ctx, dsn, capture.Request) (capture.Stream, error)` is mandatory**, as ADR
  0014 B2 decided.
  - All three drivers refuse it with `capture.ErrUnsupported` and a reason, SQLite permanently.
  - The DSN is a parameter so that no struct carries a secret. Putting it in `Request` was rejected.
- **`capture.Stream` has `Start`, `Next`, `Close` and `Drop`.** Close keeps the server resource and Drop
  releases it, idempotently.
- **`capture.Request.Name` is derived from the table and version**, so an abort finds the resource
  however early a crash came.
- **`driver.Shadower` is optional on the Conn**, as `Chunker` is:
  - `Shadows(op)`, `CreateShadow`, `DropShadow` and `DropRetired` are pure;
  - `CopyChunk` and `ApplyChanges` run inside the open transaction;
  - `Cutover(ctx, s, catchUp)` takes its own brief lock, calls `catchUp` under it, and swaps. The engine
    never learns how an engine locks.

  Optional is safe here where 0014 B3 rejected it for capture. Absence is never silent:
  `Facts.Capture` and the preflight name it before anything is created.
- **The derived names fit 63 bytes**, shortened with a digest where they would not:
  - the shadow is `_<table>_migr_<version>_new`;
  - the retired table is `_<table>_migr_<version>_old`;
  - the resource is `migr_<version>_<digest>`.

  None resembles a history table (ADR 0014 D2).

### E. Refused before anything is created
In `performShadow`, before the stream opens, in this order:
1. the server does not permit capture, naming every missing setting or privilege;
2. no `Shadower`;
3. no capture opener or no shadow store;
4. an operation that is not a change to one existing table's columns, indexes or constraints;
   `add_primary_key` and `drop_primary_key` are refused too, the copy being matched by the key;
5. more than one table;
6. a table not in the database, or one with no primary key;
7. a table already holding the retired name.

`migr plan` marks the refusal with `×`, and `migr validate --connect` reports it under `capability`.
That is "the ordinary capability path". These refusals are exit **1**: the same migration runs against a
server that has what it lacks.

### F. Carrying on and abandoning
- **A dirty migration with a state row is carried on by `migr up`**, from the cursor while copying and
  from the position while syncing. It is refused, as a chunked resume is:
  - a moved source digest is drift, exit 4;
  - a moved operation list is `ErrNotResumable`;
  - a migration that no longer declares the strategy is `ErrNotResumable` too.
- **A crash between a committed swap and its record** is read from the catalog: no shadow and a retired
  table present. The record is completed and the stream released.
- **`migr resolve --rolled-back` is the abort**: it releases the stream and drops the shadow, both by the
  names the row recorded, and then clears the row and appends `resolved`. `--applied` and `--resume` are
  refused, because only a cutover applies one, and carrying it on is `migr up`'s.
- **`engine.Retire` drops the retired original and clears the row**, a separate step called by nothing in
  a run.
- A rollback of a cut-over shadow migration takes the ordinary inverse path. Rollback by renaming back
  is not built here.

### G. Cost
`plan.Step.Shadowed` makes `Step.Lock()` `brief_exclusive`, "through a shadow table: the rename at
cutover", so `--require-safe` passes a shadowed `TableRewrite`. A shadowed step is never rewritten or
chunked, and `migr plan` prints none of its own statements, since none of them is what runs.

## Decision Outcome

A1, B through G.

Measured in `internal/engine`'s in-memory world:
- **The full lifecycle** under concurrent writes ends with the table equal to what the app wrote, the
  original retained, and the capture released.
- **A kill at eight points**, each followed by a fresh run, cuts over correctly. The points:
  - creating the shadow;
  - the first and third copy chunks;
  - the first apply;
  - the second and fifth reads;
  - the cutover;
  - the swap.
- **The next run opens its stream from the exact recorded position.**
- **Abandoning** leaves no shadow, no resource and no row.
- **Every preflight case** creates nothing and opens no stream.
- **Ctrl-C** keeps the resource and the next run finishes, and a hold outlasts `migration_timeout`.

Against compose, `migr up` and `migr plan` refuse before anything is created, each for its own reason:
- PostgreSQL 16 for `wal_level`;
- PostgreSQL 17 because the driver reads no log yet;
- MySQL 8.0 for `REPLICATION SLAVE`.

## Consequences

### Positive
- A run can stop at any point and the next one carries on from what committed, which is what an
  indefinite hold needs.
- A capture resource never outlives an abort or a cutover, whatever order things failed in.
- M13-T10 and M13-T15 implement `Capture` and `Shadower` and change nothing in the engine.

### Negative
- **`migr up` can now wait indefinitely**, and in this task nothing but a test can end the wait with a
  cutover: Ctrl-C detaches, and `migr cutover --confirm` is M13-T14's.
- The hold holds the migration lock, so no other migration on that database runs meanwhile.
- A cut-over migration's rollback is a plain `ALTER`, the cost the shadow avoided.

### Neutral
- `<history.table>_shadows` is a second mutable table, created by `Ensure` and excluded by `Introspect`
  on all three drivers.
- No real driver performs the lifecycle yet. Each refuses honestly.
