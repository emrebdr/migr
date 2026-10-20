# 0091. MySQL keeps a shadow table current from its binary log and swaps it in by RENAME under LOCK TABLES

## Status

Accepted

## Date

2026-10-20

## Context

M13-T10 makes the lifecycle ADR 0089 modelled run on a real server. Its done-when, on MySQL 8:
- row-format binlog change capture works;
- the copy runs in chunks, with throttling;
- the cutover is an atomic rename;
- an abort cleans up at any stage.

**The acceptance test:** an online column type change on a table under sustained concurrent write load,
with zero failed writes and zero lost rows, verified by row-count and checksum comparison.

Two more requirements:
- the recorded position is a GTID where the server has GTIDs and a file and offset where it does not;
- `binlog_format=ROW` is reported and never set.

ADR 0089 left two driver halves to fill: `mysqlDriver.Capture`, and `driver.Shadower` on the session.
Its hand-off said nothing in the engine would move. Three small things did, and they are recorded below.

## Measured first

On compose's `mysql80` (8.0.46, GTIDs off) and a new `mysql80gtid` (8.0.46, `gtid_mode=ON`):

| Question | Answer |
|---|---|
| Settings | `log_bin = 1`, `ROW`, `FULL`, `binlog_row_value_options` empty, `binlog_expire_logs_seconds = 2592000`, `REPEATABLE-READ` |
| `RENAME TABLE t TO r, s TO t` under `LOCK TABLES t WRITE, s WRITE, h WRITE` | Accepted (8.0.13 is the release that allows it). A writer queued behind the lock waited about 2 s, then **landed in the new `t`**, carrying a value too large for the old column's type. |
| `ALTER TABLE s AUTO_INCREMENT = n` under that lock | Accepted |
| `information_schema` and `SHOW CREATE TABLE` under that lock | Readable |
| A table not in the lock, under it | Error 1100 |
| `START TRANSACTION` under the lock | **Releases it**: the unlocked table became readable straight afterwards |
| `CREATE TABLE s LIKE t` with `CONSTRAINT m_chk_v CHECK (…)` | The copy's check is renamed to `s_chk_1`, so a user-named check does not survive |
| A generated check name (`t_chk_1`) through LIKE and the swap | `s_chk_1` becomes `t_chk_1` again after the rename, and the retired table's becomes `r_chk_1` |
| `CREATE TABLE s LIKE t` and `AUTO_INCREMENT` | Not carried: the copy restarts from what its rows imply |
| `INSERT INTO s … SELECT … ORDER BY k FOR SHARE ON DUPLICATE KEY UPDATE k = s.k` | Accepted. It inserted the missing rows and kept the one already present. |

One answer was **not measurable** without the server's debug-sync points: whether a reader of the log
can see a transaction's row event before InnoDB's commit is visible to other sessions. MySQL's two-phase
commit writes and syncs the binary log before the engine's commit stage, so the documented answer is
yes. The design below is built for that answer.

## Decision Drivers

- **Zero failed writes:** nothing migr does may make an application write fail, a deadlock included.
- **Zero lost rows:** the copy must equal the table, by checksum, whatever interleaving the load
  produces.
- **ADR 0089's two properties:** any interleaving is correct, and a replay after a crash is harmless.
- **Invariant #12:** every lock wait is bounded and retried, the cutover's included.
- **The capability system:** a server this reader cannot read is refused before anything is created,
  through `Admits` and the capture fact.

## Decision

### A. The log is read through go-mysql-org/go-mysql v1.14.0

`go-sql-driver/mysql` exposes no raw packets. A binlog reader needs:
- the replication handshake and both authentication plugins;
- `COM_BINLOG_DUMP` and its GTID form;
- a row-event decoder for every column type;
- compressed transaction payloads.

go-mysql is the client gh-ost and Canal-class tools use. **Only `internal/driver/mysql` imports it.**

It brings these modules into `go.mod`:
- `pingcap/errors`, `pingcap/tidb/pkg/parser`, `pingcap/log` and `pingcap/failpoint`;
- `go.uber.org/zap`, `atomic` and `multierr`;
- `shopspring/decimal`, `klauspost/compress` and `goccy/go-json`.

Its own logger is a discard handler, so nothing it says reaches migr's stderr.

### B. A stream reads one table and hands over a transaction only once its commit is read

`Capture` opens a pool of its own from the DSN for metadata, plus a replication connection from the
same parsed DSN, TLS and unix sockets carried over.

**How events become batches:**
- Row events are held until the XID that commits them, or the statement that commits itself. So a
  batch never ends inside a transaction, and a recorded position never names one.
- Events outside any transaction (a rotation, a format description, the previous-GTIDs list) move the
  file and offset and nothing else. A heartbeat moves nothing, since it is not in the log.
- The copy's own inserts into the shadow arrive in the log too, and are dropped by their table map.

**Next's end rule**, which ADR 0089's `catchUp` already depended on, is now written on
`capture.Stream.Next`: *an empty batch means the stream has read to where the log ended as the call
began.*
- Each call first reads where the log ends (`SHOW MASTER STATUS`, or `SHOW BINARY LOG STATUS` from
  8.2) and the server's clock.
- It then reads until it has passed that point, or until 1000 changes, extended to the next boundary.
- On a GTID server, "passed" is containment of the server's executed set. On any other server it is
  the file and offset of the last boundary.
- `Behind` is the server's clock minus the last commit's timestamp, so there is no skew between two
  clocks.

**What a row becomes:**
- an update that moved the primary key becomes a delete of the old key, then an insert;
- every other insert or update becomes the whole row, generated columns left out;
- a delete becomes the key alone.

**What stops the stream**, with the statement named:
- an `ALTER`, `RENAME`, `DROP` or `TRUNCATE` of the table;
- a row whose width is not the table's.

### C. A position is `gtid:<set>` or `file:<name>:<offset>`, as the server dictates

The set survives a failover to a server whose binary log files are named and numbered differently,
which a file and offset does not.

A recorded position of the other kind from the server now in place is refused, naming both. That
happens when gtid_mode was changed between two runs. **Abandoning** is the way on, since translating
between the two forms is not something a client can do correctly.

A position the server has **purged** (error 1236) is refused naming `binlog_expire_logs_seconds` and
`migr resolve --rolled-back`.

### D. MySQL holds no server-side resource, so retention is what a detached hold depends on

A binlog reader registers as a replica only while it is connected. `Close` and `Drop` both close the
reader's connections, and an abandoned migration leaves no registered replica behind.

What PostgreSQL's slot is to an outage (a disk filling), purging is to a resume (a migration that
cannot carry on). So the capture fact now carries a **`Retention`** sentence, read from
`binlog_expire_logs_seconds`, and `migr plan` prints it beside the shadow.

The reader's `server_id` is derived from the capture's name in the high half of the range. It is moved
past `@@server_id` and every id `SHOW REPLICAS` lists, since a reader reusing a real replica's id
**disconnects that replica**.

### E. The copy is `INSERT … SELECT … FOR SHARE ON DUPLICATE KEY UPDATE k = k`, not `INSERT IGNORE`

ADR 0089 calls the copy "insert-ignore", which describes what happens to a row already present.
`INSERT IGNORE` itself does more: it downgrades a truncation, an out-of-range value and a NULL into a
`NOT NULL` column to warnings. A narrowing type change would meet those silently. `ON DUPLICATE KEY
UPDATE k = k` keeps the row a replay wrote and leaves every other error an error.

**`FOR SHARE`** waits for a writer whose transaction is already readable in the log but not yet
committed in InnoDB. Without it this interleaving loses a delete:
1. the delete's event is replayed, against nothing, before the commit is visible;
2. a copy chunk then reads the row from a snapshot that predates the commit;
3. the copy inserts the row, and no later event removes it.

Under REPEATABLE READ, the default, MySQL's `INSERT … SELECT` already takes shared next-key locks on
the rows it reads, so the clause changes nothing there: the mutation sweep removed it and every test
still passed. It is written out so that the copy waits the same way in a session running READ
COMMITTED, where that statement reads a snapshot and takes no locks at all.

Each chunk is keyed by the nested comparisons `chunk.go` measured in M12-T3, and paced by the engine's
`Pacer`.

### F. A replay is `REPLACE` by row and `DELETE` by key, bound by the source column's class

`REPLACE` also removes a stale row that a secondary unique key collides with. The later event that made
the collision transient puts that row back.

Values are rendered from the decoded event into the text the server answers with:
- an unsigned integer is un-signed;
- an enum is its label and a set its members, never an index or a bitmask;
- a timestamp is in UTC, and the replay sets the session's `time_zone` to `+00:00` around its
  statements;
- a float is **the exact double its float32 is**, because a float widened to a double converts the
  binary value, and the shortest text of the float32 converts to a different double;
- text is bound through `CONVERT(? USING <its charset>)` so its bytes are read as they were written;
- binary and blob values are bytes;
- a bit value is a number, since a string assigned to a `BIT` column is its bytes.

A column whose type is none of these is refused by `Admits`.

### G. The cutover is LOCK TABLES, the catch-up, AUTO_INCREMENT, RENAME and UNLOCK, bounded by safety.lock_timeout

1. `LOCK TABLES` the table, the shadow, and `<history>_shadows`, which the catch-up records its
   position in. Its wait is the session's `lock_wait_timeout`, so a timeout is
   `driver.ErrStatementLockTimeout`, and `shadowTransact` backs off and retries.
2. The catch-up. If it overruns `safety.lock_timeout`, the lock is released and the same sentinel is
   returned, so writers wait at most that long per attempt. This is gh-ost's cut-over lock timeout,
   expressed as the invariant #12 bound migr already has.
3. Copy `AUTO_INCREMENT` from `SHOW CREATE TABLE` to the shadow.
4. `RENAME TABLE t TO retired, shadow TO t`.
5. `UNLOCK TABLES`, deferred on a context that cannot be canceled.

**The shape this records:**
- `LOCK TABLES` ends the transaction the engine opened around the cutover;
- a `START TRANSACTION` under it would release the locks, so nothing under them begins one;
- each catch-up write commits on its own, and the applied row is written after the unlock.

The crash window between the swap and its record is the one `cutOverAlready` already reads back from
the catalog.

### H. Three changes outside the driver

1. **`Shadower.CreateShadow(ctx, s)` takes a context.** An `alter_column` is `ErrNeedsSession` on MySQL
   (ADR 0058's redefinition reads the column), so the shadow's change is expanded from **the
   original's** catalog row and rendered onto the shadow. The two are identical before the change.
2. **New `Shadower.Admits(ctx, s)`**, which the engine calls after reading the key and before the
   stream opens. MySQL refuses:
   - MariaDB, and MySQL before 8.0.13;
   - a foreign key on the table or into it;
   - triggers;
   - user-named checks;
   - a column type it cannot replay.

   A preview calls it for the **first queued migration only**: a later one's table may be one a
   migration ahead of it creates. That was measured in the end-to-end test, where `migr plan` marked a
   correct migration "refused" for exactly that reason. A later one is judged on everything that reads
   no table, and the plan says its table is read when the run reaches it. That is the `~` rule (ADR
   0069).
3. **The copy waits for a trailing stream.** While the last read's `Behind` exceeds
   `safety.replica_lag_threshold` (`engine.Options.CaptureLag`), the copy reads the log again instead of
   copying more. Each read is reported as a held chunk, so a copy cannot outrun the replay into a
   cutover that never catches up.

`Shadows` refuses, on this engine:
- `add_unique`, since a copy keeping the first of each duplicate would drop the rest silently;
- `rename_column`, since columns are matched by name;
- `drop_check`, since LIKE renames checks;
- both foreign-key operations.

### I. The acceptance test, and what it measured

`internal/driver/mysql/shadow_live_integration_test.go` runs on both servers:
- **Setup:** 20,000 seeded rows. The migration changes `n` from `int32` to `int64` and widens `label`.
- **The load:** eight writers, each owning its keys. They insert, update, delete and move keys
  continuously from before the copy until after the cutover. Each transaction writes one row of the
  table and the same change to an unchanged control table.
- **The confirmation:** a second session confirms the cutover once the copy has been held for 1.5 s.
- **The result:**
  - **zero failed writes**;
  - equal `COUNT(*)` and `BIT_XOR(CRC32(…))` over every column;
  - `n` is `bigint`;
  - the retired original is kept, and `engine.Retire` drops it;
  - the next generated id is past one the original had already handed out and deleted;
  - every recorded position is `file:` on mysql80 and `gtid:` on mysql80gtid.

The same suite covers:
- **resume:** a run stopped mid-copy or mid-hold, carried on from its recorded position to the same
  equality;
- **abandon:** abandoning at copying and at syncing, with no copy, no registered reader and no state
  left;
- **an `ALTER`** under a held copy, which stops the run naming the statement;
- **a purged position**, refused naming the retention setting;
- **a narrowing change** whose rows do not fit, where the copy fails with error 1265 rather than
  truncating, leaving the table untouched;
- **a catch-up that overruns** `safety.lock_timeout`, which returns the lock-wait sentinel, leaves the
  table writable and swaps nothing;
- **`Admits`**, refusing a table in a foreign key either way, one with a trigger, one with a named
  check and one with a `point` column, and admitting one whose only check is generated and whose
  generated column is left out.

The migr session runs those cases at `time_zone = '+05:00'` with a `timestamp(6)` column under load,
so a replay that did not read timestamps as UTC would shift them and fail the checksum.

The first acceptance run found a test bug, not a migration bug. The writers stamped `NOW(6)` once per
statement, so the two tables differed by microseconds.

## Rejected

- **A hand-written replication client.** Zero dependencies, at the price of the handshake, both
  authentication plugins, TLS, the dump commands and a decoder for every type, written and kept correct
  here. The decoder is where tools of this class find their bugs.
- **Triggers.** Rejected by ADR 0014 for putting work on every production write. Nothing here
  reconsiders that.
- **gh-ost's two-session sentinel cutover.** A sentinel table and a blocked RENAME on a second session
  exist because RENAME under LOCK TABLES was refused before 8.0.13. On 8.0.13 and later one session
  does it atomically, measured above, and earlier releases are refused.
- **Re-reading the original by key** instead of replaying the logged row. It needs no value rendering,
  but it needs a locking read on the production table for every change, to wait out the
  readable-before-visible window. That adds shared locks on hot rows to the application's path, which
  is the cost the approach exists to avoid.
- **`INSERT IGNORE` for the copy**, for E's reason.
- **Requiring `binlog_row_metadata=FULL`** so events carry column names and enum labels. It is a server
  setting, and `information_schema` answers the same questions once per stream.
- **Supporting MariaDB here.** Its GTIDs are `domain-server-seq` and its RENAME-under-lock behaviour
  is unmeasured. It is refused by name and handed to a task of its own.

## Consequences

- MySQL 8.0.13+ performs shadow-table migration. MariaDB and older MySQL are refused before anything
  is created, naming why.
- `go.mod` carries go-mysql and its transitive modules, and `go mod tidy` keeps them indirect.
- `test/initdb/grant-replication.sql` gives compose's `migr` user the two replication grants.
  `migr_plain` stays without them, as the refusal case.
- A detached hold can be carried on only while the server keeps the log. `migr plan` says how long that
  is.
- The rollback of a cut-over shadow migration is still the ordinary inverse (ADR 0089), now a MySQL
  `ALTER` on the swapped-in table.
