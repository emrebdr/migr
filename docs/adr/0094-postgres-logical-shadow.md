# 0094. PostgreSQL keeps a shadow table current through a replication slot, and no slot outlives the migration that made it

## Status

Accepted

## Date

2026-10-21

## Context

M13-T15's done-when:
- logical decoding drives change capture through a replication slot;
- the copy → hold → cutover lifecycle completes under sustained write load;
- the row-count and checksum comparison M13-T10 uses passes.

The task adds one more requirement: **an abandoned replication slot is an outage, not litter.**
PostgreSQL keeps every byte of WAL for a slot nobody advances, until the disk fills. So this task owns:
- detecting orphaned slots and reporting them by name;
- dropping the slot on every abort path, including the ones that start with the process being killed.

`wal_level = logical` is reported, never set ([ADR 0090](0090-capture-capability.md)).

What existed:
- the lifecycle and its records ([ADR 0089](0089-shadow-table-lifecycle.md));
- the operator's confirmation ([ADR 0093](0093-operator-confirmed-cutover.md));
- MySQL's implementation ([ADR 0091](0091-mysql-binlog-shadow.md)).

PostgreSQL's `Capture` refused, and its session was no `Shadower`.

## Measured first

Measurements were on compose's postgres17; postgres16 runs `wal_level = replica` and refuses.

| Question | Answer |
|---|---|
| `pg_logical_slot_peek_binary_changes(slot, upto = pg_current_wal_lsn())` | Returns exactly the transactions committed before `upto`. Another table's writes are filtered out by the publication. |
| `pg_replication_slot_advance(slot, upto)` with nothing read | `confirmed_flush_lsn` moves at once, and `restart_lsn` follows within about 15 s, so the retained WAL shrinks (297 kB after 49,000 rows elsewhere) |
| An advance inside a transaction that then rolls back | **Stays advanced.** The slot may move only after the position's record commits. |
| `INSERT … SELECT … ORDER BY k FOR SHARE ON CONFLICT (k) DO NOTHING` | Accepted. A copy reaching a row whose delete had not committed waited about 2 s for it and skipped the row. |
| An UPDATE that leaves an out-of-line TOASTed column unchanged | The new tuple carries that column as `u`, with no value |
| `CREATE TABLE … (LIKE t INCLUDING ALL)` | Index and constraint names are regenerated (`s_pkey`, `s_e_key`, `s_w_idx`); check names are kept. A serial shares the original's sequence, still owned by the original. An identity gets a fresh sequence starting at 1. |
| A writer queued behind `LOCK TABLE t, s IN ACCESS EXCLUSIVE MODE` while both are renamed | It lands in the new table. So did a prepared statement queued behind the lock, and a later execution of it. |

**One defect surfaced under load, not in planning.** The first acceptance run lost 11 of 36,645 rows,
each one whose key an UPDATE moved while its TOASTed note was left unchanged. The decoder had turned
that into a delete of the old key and a partial update of a new key the shadow did not hold yet.

## Decision Drivers

- **No change is lost**, whatever order the copy, the replay and a rollback of the cutover come in.
- **No slot outlives its migration** on any path, and a slot that could be someone else's is never
  dropped by migr.
- **The user's table is not changed** to make capture work. REPLICA IDENTITY FULL was not an option.

## Considered Options

### A. How the WAL is read
- **A1: pgoutput through a per-migration publication, polled with `pg_logical_slot_peek_binary_changes`
  on the stream's own connection, parsed with jackc/pglogrepl** ✔ (by agreement):
  - pull-based, matching the engine's poll;
  - no replication-protocol connection;
  - pgoutput is built into every PostgreSQL from 10.
- A2: the streaming replication protocol. **Rejected:** keepalives and a second connection kind, for a
  reader the engine pulls from anyway.
- A3: test_decoding. **Rejected:** its text output is documented as for testing, and its quoting would
  be ours to parse.
- A4: our own pgoutput decoder. **Rejected:** pglogrepl is maintained by pgx's author and parses
  exactly that.

A read peeks up to where the WAL ended as it began, so it returns nothing only once it has read that
far, as the contract states. A peek decodes from the slot's confirmed position, which trails the
stream's cursor until the engine acknowledges. So a transaction committed at or before the cursor is
skipped, having already been handed over.

A read with nothing to hand over moves its cursor to `upto`, so a slot on an idle table under busy
others still advances. A TRUNCATE of the table, or a relation message showing its columns changed,
stops the stream, naming the abandon remedy.

### B. When the slot advances
- **B1: `capture.Stream.Acknowledge(ctx, position)`, called by the engine once the transaction
  recording that position has committed** ✔. It is mandatory on the contract; MySQL and the fakes do
  nothing.
- B2: advance inside `Next`. **Rejected by measurement:** the cutover's catch-up applies inside a
  transaction that can roll back, and an advance survives a rollback.

### C. A cutover that rolls back
**The engine now re-reads from what committed.** A cutover attempt that fails (a lock wait, or a
catch-up past `lock_timeout`) undoes its catch-up while the stream has read on past it. Each retry
first resets the job to the state the store recorded and reopens the stream from that position.
MySQL's catch-up commits as it goes, which is why this never showed there.

### D. What a change carries
- **`capture.Change.Partial`:** an update that left a TOASTed column out. It updates the shadow's row
  and inserts none, since a row the copy has not reached yet gets the value from the copy.
- **`capture.Change.From`:** the old key, on a partial update that also moved the key. It is replayed as
  one statement: the shadow's row is deleted at the old key and inserted at the new one, the left-out
  columns carried from the deleted row.
  - If the shadow has no row at the old key yet, it inserts nothing, and the copy brings the row.
  - If the copy has already brought it to the new key, the insert updates that row to the same values.
  - A whole-row key move stays a delete and an upsert.

Values are bound as text and cast to the column's type **without its modifier**
(`$1::text::character varying`). So the assignment applies the column's own length or precision
check, as the copy's does, rather than an explicit cast silently truncating. An upsert of one key twice
in a run keeps the last, since `ON CONFLICT DO UPDATE` may not change a row twice.

### E. The shadow and the cutover
- **`CreateShadow`:**
  - runs `LIKE t INCLUDING ALL EXCLUDING INDEXES`;
  - recreates every key, unique, exclusion constraint and plain index from `pg_get_constraintdef` and
    `pg_get_indexdef`, under a name derived from its own and the shadow's (`_<name>_<digest>_new`). An
    operation retargeted to the shadow speaks that name, so a `drop_index` of `x` drops x's copy;
  - copies the table's and its columns' grants (by agreement);
  - gives the shadow the original's owner where the session is not it.
- **`Cutover`, one transaction**, so a crash leaves the swap and its record together or neither (MySQL
  cannot do this):
  1. lock both tables ACCESS EXCLUSIVE under the session's `lock_timeout`;
  2. catch up within the same bound;
  3. move each serial's `OWNED BY` to the shadow, and `setval` each identity to the original's;
  4. rename the original's indexes to retired names (`…_old`), then the shadow's to the original names;
  5. rename both tables.
- **`Admits` refuses** (by agreement):
  - foreign keys, either way;
  - dependent views and rules;
  - triggers and RLS policies;
  - partitioned and inherited tables;
  - REPLICA IDENTITY other than DEFAULT or FULL. A publication of a table with NOTHING would refuse its
    updates and deletes.
- **`Shadows` refuses** `add_unique`, `rename_column` and both foreign-key operations. It carries
  `drop_check`, since a check keeps its name under LIKE here.

### F. Every abort path, and the orphans
| Path | What drops the slot and publication |
|---|---|
| A start that fails before its record | The engine's existing cleanup, `stream.Drop` |
| A start **killed** before its record | The next fresh `Capture` of that derived name drops what it finds before creating its own |
| `migr resolve --rolled-back` | `abandonShadow`, by the recorded name. A slot already gone is no error. |
| A cutover that committed | `stream.Drop` after it |
| A run **killed** between that commit and the drop | The next `migr up` (below) |
| Retiring | Nothing: the slot is gone by then |

**Orphans** (by agreement):
- The optional `driver.CaptureKeeper` on the Conn lists the `migr_` slots on this database, with
  retained WAL and `wal_status`, and releases one by name.
- `engine.Orphans` marks a slot orphaned where no copying or syncing state owns it. It is **ours** where
  its state cut over, or where its derived name is a version this project carries with no state.
- `migr up`, under the migration lock, drops ours before running anything, warns of each, and its
  summary says *released the orphaned replication slot …*. A slot in use is left and said so.
- Anything else is someone else's: `migr status`, `migr cutover` and `migr plan` report it with the
  `pg_drop_replication_slot` remedy (`--json`: `orphaned_slots`).
- `migr validate --connect` does not list them. It reads no history (ADR 0064), and between two peeks a
  live hold's slot is as inactive as an orphan's.

### G. What the plan says
The capture fact's `Retention` reads `max_slot_wal_keep_size`:
- unset (`-1`): *kept with no limit … fills the disk if nothing does*;
- set: *at most N, past which the server invalidates it*.

`migr plan` already prints it beside a shadow-table migration.

## Decision Outcome

A1, B1, C, D, E, F and G.

**Acceptance, on postgres17:**
- the setup: eight writers, an `int32` → `int64` change and a widened `varchar`, over 20,000 rows, some
  with notes TOASTed out of line;
- **no failed write**;
- the table equals a control by count and an md5 over every row;
- **ids continue past the original's identity**;
- the indexes carry the original's names;
- no slot remains.

The same suite also holds:
- a stop mid-copy and one mid-hold, each carried on to the same equality;
- abandoning at copying and at syncing, leaving no slot, no publication, no copy and no state;
- an ALTER and a TRUNCATE under the hold, each stopping it;
- a slot a killed start left, dropped and remade;
- our orphan released and a stranger's kept;
- each Admits refusal;
- grants and an untouched TOASTed note surviving the cutover.

In the end-to-end suite, a real `migr up` process holds on postgres17 and `migr cutover --confirm`
cuts it over, after which no slot remains.

## Consequences

### Positive
- PostgreSQL runs every shadow-table migration MySQL does. Its cutover is atomic with its record, and
  its catch-up is bounded the same way.
- No path that migr controls leaves a slot. The one it cannot see, someone else's, is named with its
  size.

### Negative
- **Creating a logical slot waits for every transaction already running.** A long-running transaction
  delays the start, bounded only by Ctrl-C.
- **A peek re-decodes from the last acknowledgement.** A backlog bigger than one batch (10,000 changes)
  is decoded more than once while it drains.
- **A published table cannot have REPLICA IDENTITY NOTHING**, which is why that is refused rather than
  worked around.
- The shadow is owned by the migrating role until the cutover, unless the original's owner is set on it.
- An existing `<history.table>_shadows` table from before ADR 0093 still lacks `behind`.

### Neutral
- `driver.CaptureKeeper` is optional, and only PostgreSQL implements it: MySQL holds nothing on the
  server for a reader.
- jackc/pglogrepl joins go.mod, imported by `internal/driver/postgres` alone.
