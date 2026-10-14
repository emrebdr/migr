# 0078. Running a data-only migration in chunks, and carrying it on after an interruption

## Status

Accepted

## Date

2026-10-14

## Context

M12-T3's done-when is that *"large-table backfills run in configurable chunks, report progress, and
resume from the last committed chunk after an interruption. Chunk pacing must be pluggable from the
start so M13-T8 can drive it from replica lag — an unthrottled backfill is itself an outage."*

M12-T2 compiled each data operation to one statement over the whole table, inside the one
transaction a migration runs in (ADR 0071). On a large table that one statement holds a lock on
every row it writes for the length of a full scan, and each of the three was measured
`LongExclusive` or `TableRewrite` (ADR 0077). Three things stood in the way of chunking it:

- **a migration is one transaction.** `checkAtomic` refuses more than one, naming M13-T4, and ADR
  0071 made the transaction bracket the whole step loop so that an emulated operation reads what the
  steps before it left;
- **the history is an append-only log** (ADR 0013, ADR 0019). A cursor that moves with every chunk
  can be neither a row per chunk, which would bury the log, nor an edited row, which would make it
  not a log;
- **a resume indexes into a statement list** (ADR 0062), and a chunked operation has none: how many
  chunks it takes is a fact about the table at the moment it runs.

Two decisions were the user's: **which migrations are chunked** (A) and **what carries a stopped
one on** (E).

### What was measured first

Throwaway probes on all five servers; nothing from them ships, and two of them decided what does.

**1. A cursor bound as text compares as the key's own type** everywhere. PostgreSQL gives the
parameter the key column's type from the comparison, so `"id" > $1` with the Go string `"9"` is a
`bigint` comparison, and the same for a `uuid`; SQLite applies the column's affinity to a text
operand, a row value's included; MySQL and MariaDB convert. A text key orders by its collation on
every engine — `'10' < '9' < 'a'` — and the cursor comparison uses the same one, so paging agrees with
itself.

**2. On MySQL and MariaDB a row-value comparison scans the key from its start, and an `UPDATE` locks
what it scans.** Over a 50,000-row table keyed `(a, b)`, `(a, b) > (?, ?) ORDER BY a, b LIMIT 1
OFFSET 99` near the end read **49,101** handler rows on both servers, where `a >= ? AND (a > ? OR
b > ?)` read 101; the matching `UPDATE` read 50,003 against 111. Worse, under `REPEATABLE READ` the
row-value `UPDATE` left **every row of the table** locked for the chunk's transaction — a write to
`a = 10`, far from the chunk, timed out on both servers — where the spelled-out form locked its own
range and the gap at its upper bound. A single-column key is a plain comparison and needs neither.
PostgreSQL (an index condition, bitmap scan of the chunk's rows) and SQLite (`SEARCH … USING INDEX`)
both answer the row value with a range.

**3. Found by the integration test rather than a probe: PostgreSQL's `ORDER BY` resolves an
unqualified name against the output columns first.** The bound read selects the key as `"id"::text`,
so `ORDER BY "id"` sorted the keys as text: a chunk of 10 over keys 1 to 60 covered keys 1 to 18 —
`1, 10, 11 … 18` being the first ten in text order — and over a million rows the tenth key in text
order is `1000002`, one chunk taking the whole table. No row was lost or written twice, because the
write's range compares as the key's own type; the size was what broke. The `ORDER BY` is qualified.

**4. A real `SIGKILL`** of `migr up` mid-chunk, on SQLite, then a second `migr up`: the dirty row and
the checkpoint had committed with the chunk they describe and nothing after it, which is the property
the design depends on. It is now an untagged end-to-end test.

## Decision

### A. Only a data-only migration runs in chunks (user's choice)

A migration whose every operation is a data operation has every step marked `Chunked` by the
planner, when `safety.chunk_rows` is above zero. Any other migration — one that also changes the
schema — runs its data operations as M12-T2 does, one statement in the migration's transaction, and
`migr plan` says so and says how to change it: *put it in a migration of its own*. That is what
`mixed-data-migration` already advises. Each chunk commits on its own, so a migration that also
changed the schema could no longer be one transaction — which is the split M13-T4 owns — while no
step of a data-only migration reads what another left uncommitted. Nothing in the IR changed, and
neither SDK did.

### B. A chunk is a keyed range over the primary key, two statements in one transaction

`driver.Chunker` is a session that runs one `driver.Slice` — the operation, the key's columns, the
cursor after which it starts, and how many rows it covers — inside the transaction the engine opened,
and reports what it wrote, the key of the last row it covered, and whether any row lay beyond it.
Each driver composes the same two statements:

1. the bound: the key of the `rows`-th row after the cursor, `ORDER BY key LIMIT 1 OFFSET rows-1`;
2. the write: the operation's own statement, narrowed to `key > cursor AND key <= bound`.

Where no `rows`-th row exists, the write runs over everything after the cursor and the chunk is the
last one. The key is read from `Conn.Introspect`, which the contract already has, so the engine
composes no SQL (invariant #3); a table with no primary key is refused before any chunk runs, naming
both ways on — give it a key, or set `safety.chunk_rows: 0`. `driver.ChunkCompiler` renders the same
two statements with no database, for `migr plan` and for nine golden files under
`test/golden/<driver>/chunk/`, each holding a one-column and a two-column key. MySQL spells the
cursor out as nested comparisons (measurement 2); PostgreSQL qualifies its `ORDER BY` (measurement
3); SQLite qualifies every column, for ADR 0077's reason.

`WHERE column IS NULL LIMIT n` repeated was the alternative. It needs no cursor, since each chunk
shrinks the set the next reads — but it rescans from the start every time, which is quadratic on a
column with no index, and `data_transform` and `data_copy` have no predicate that shrinks.

### C. The checkpoint lives in a table of its own beside the history, and commits with its chunk

`history.Checkpointer` is a store that saves, reads and clears a `history.Checkpoint`: the
migration, the direction, the operation, the cursor, and how many chunks and rows. All three
drivers' stores satisfy it, against `<history.table>_progress`, created from IR in `Ensure`
(`history.ProgressSchema`, invariant #3) — so seeds get `migr_seeds_progress` from `Conn.Seeds()`
for nothing, and a history that predates this build gains the table on its next `Ensure`.

It is the **one mutable table migr keeps**, and it is not history: it holds where a run *is*, keyed
by migration and direction, and the transaction that appends a migration's `applied` row deletes its
checkpoint. Every chunk's transaction writes the chunk and its checkpoint together, so a run killed
between two chunks has exactly the checkpoint of the last one that committed — measured with a real
`SIGKILL` (measurement 4). `Introspect` excludes both checkpoint tables on all three drivers, or every
`migr diff` would propose dropping them, and `migr.yaml` refuses a history or seed table named as the
other's checkpoint table.

### D. The first chunk marks the run dirty, in its own transaction

A run is recorded **dirty** in the transaction of its first chunk, and **applied** in the
transaction of its last, with the checkpoint cleared beside it. That makes a crash, a canceled run
and a failed chunk leave the same evidence: from the first committed chunk the table is between
states, and a dirty row is what says so — so `migr down`, and every other migration, refuse until the
run is carried on or settled. A run whose first chunk is also its last never writes the dirty row,
since it never leaves anything between states; a small table's log stays one row.

Two rows for one migration in one batch need two ordinals, and the `(batch, ordinal)` key refuses
the second one at the first's. So a run that writes the marker takes the next ordinal for everything
it writes afterwards, and every migration after it in the batch moves one on. The row's
`last_statement` names the operation the run is in, which is the nearest thing a chunked run has to
a statement — the checkpoint is what is exact, and every rendering of the row prints it.

### E. The next `migr up` carries a stopped run on (user's choice)

`migr up` reads the checkpoints with the history and queues a dirty migration rather than refusing
it **only** where its stopped run was going up, a checkpoint names it, and both digests still match:

- the **source** digest, or the run is refused as drift with `Resuming` set (exit 4);
- the **operations**, or it is refused as `ErrNotResumable` — a cursor through rows one value filled
  is meaningless under another;
- and the migration must still run in chunks, or it is refused the same way: `chunk_rows: 0` now
  would run the operation over the whole table from the start.

It carries on at the operation the checkpoint names, after its cursor, and writes no second marker.
Every other dirty state is refused exactly as before. `migr resolve --resume` takes the same path for
a run going either way — a rollback that stops partway is settled by the command that says what it
is doing, never by the next `migr down` — and `--applied` and `--rolled-back` clear the checkpoint in
the transaction their row goes in, so a later run of that migration never resumes from a cursor
through rows it did not write.

This is not invariant #6's repair path. Nothing recorded is rewritten, the log only grows, and what
is carried on is the migration's own work, at the place its own committed row and checkpoint say.

### F. Pacing is a seam the engine asks before every chunk

```go
type Pacer interface {
    Pace(ctx context.Context, done Progress) (Pace, error)
}
type Pace struct { Rows int; Wait time.Duration; Throttle string }
```

The engine asks before every chunk, the first included, what the chunks so far have done; waits
`Wait` through an injected `Sleeper` that returns when the run is canceled; sizes the next chunk by
`Rows`; and reports `Throttle` in that chunk's progress. `engine.Steady{Rows, Pause}` is the pacer
`migr.yaml` builds — `safety.chunk_rows`, default 1000, and `safety.chunk_pause`, default 100ms
between chunks and never before the first. A pause of zero is refused: chunks back to back are the
unthrottled backfill the task calls an outage. M13-T8 is a `Pacer` that reads replica lag, waits
while it is above `safety.replica_lag_threshold`, and says so in `Throttle`; a test drives the
engine with a pacer that resizes, delays and throttles, so the seam is held before anything uses it.

### G. Progress is a callback, and `-v` is what prints it

`engine.Options.Progress` is told after every committed chunk: the migration, the operation, the
table and key, how many chunks and rows, the cursor, whether it was the last, and the throttle.
`internal/cli` logs it at progress level, so `-v` prints a line per chunk on stderr and `--json`
makes it a JSON log line. `migr up` reports a chunked run as *N chunks, M rows written*, and
*resumed* where it carried one on; `migr status` prints a stopped run's checkpoint in place of a
statement number; `migr plan` prints one chunk's two statements, with every bound argument a
placeholder, and how large a chunk is and what it pages by.

## Alternatives rejected

- **Chunking wherever a data operation appears** (user's choice). It splits a migration into a
  schema transaction, the chunks and the rest — most of M13-T4's multi-run loop, and that task's
  question about an expansion after a split, arriving a milestone early.
- **An opt-in field on the operations** (user's choice). An IR change in both SDKs and every
  fixture, and a large-table backfill an author forgot to mark is the outage this exists to prevent.
- **Resuming only through `migr resolve --resume`** (user's choice). A deploy interrupted partway is
  routine, and failing the next deploy over a run whose own row and checkpoint say exactly where to
  carry on buys an operator nothing.
- **A dirty row only when a run fails.** A `SIGKILL` runs no code, so the run killed between two
  chunks would leave the migration *pending* with half its rows written — and the next run would
  start from the first row, duplicating every copied one.
- **The cursor in the history log**, a row per chunk or an edited row. One buries the log; the other
  makes it not a log.
- **`IS NULL LIMIT n` repeated** (B): quadratic, and the transform and the copy have no predicate
  that shrinks.
- **A row-value cursor on MySQL**, for uniformity: measurement 2, a full-index scan that locks every
  row of the table.
- **Counting a table's rows to report a percentage.** It is a full scan before the first chunk, on
  exactly the table this exists not to scan; rows written and the cursor are what progress reports.

## Consequences

- Every data-only migration — M12-T2's own end-to-end fixture among them — now runs in chunks by
  default, and its `migr plan` shows the chunk rather than the whole-table statement.
- A data-only migration over a table with no primary key fails where it ran before; the refusal
  names `chunk_rows: 0`.
- A chunked run holds the migration lock for its whole length, which is the length of the table, and
  every other `migr up` waits behind it or exits 3; `migr status` takes no lock and reports it.
- A chunked migration that stops is **dirty** for every reader of the history until the next `migr
  up`, which is correct — its table is between states — and is what M13-T14's sixth state may one day
  say more precisely.
- The lock verdicts are unchanged: a chunk's lock is bounded by its size, but `migr plan` prints no
  lock axis until M13-T6, which is where a chunked operation's verdict is the honest one to revisit.
