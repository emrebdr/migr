# 0095. MariaDB keeps a shadow table current from its binary log, and swaps it in through a rename queued behind a write lock

## Status

Accepted

## Date

2026-10-22

## Context

M13-T17's done-when:
- M13-T10's acceptance test passes on a MariaDB 11.4 with `log_bin` on and `binlog_format = ROW`;
- its position is recorded as a MariaDB GTID (`domain-server-seq`) where the server has them;
- the cutover's RENAME under `LOCK TABLES` is measured on that server before it is relied on.

[ADR 0091](0091-mysql-binlog-shadow.md) refused MariaDB by name, in `Admits` and in the stream, because
neither its GTIDs nor its cutover locking had been measured. This ADR lifts that refusal from 11.4.
ADR 0091 is otherwise unchanged and still describes MySQL.

Compose's `mariadb114` runs with `log_bin` off. Two suites depend on that:
- ADR 0064's uuid-default case, refused by one server's declaration and performed by the other's;
- the capture fact's refusal, read from a server that lacks the setting.

## Measured first

On a new compose service, `mariadb114binlog` (11.4.13, `--log-bin --binlog-format=ROW --server-id=4`):

| Question | Answer |
|---|---|
| `SHOW MASTER STATUS` / `SHOW BINLOG STATUS` | Four columns, with no executed-GTID column. `@@gtid_binlog_pos` is `0-4-10` once the init scripts have run. `@@gtid_mode` does not exist. |
| `RENAME TABLE t TO r, s TO t` under `LOCK TABLES t WRITE, s WRITE, h WRITE` | **Error 1192**: active locked tables |
| `ALTER TABLE t RENAME TO r; ALTER TABLE s RENAME TO t` under the same lock | Accepted, but **every writer queued behind the lock fails with 1146**, on three runs of four writers each. A writer arriving between the renames succeeds. |
| gh-ost's form: A locks `t`, `s` and a sentry at `r`; B queues `RENAME`; A drops the sentry and unlocks | **Every queued writer, before and after B was queued, landed in the original**, which B then renamed away. B was waiting on the sentry, which sorts before `t`, so nothing exclusive was pending on `t`. |
| A locks `t` alone; `s` and `r` are free; B queues `RENAME`; A unlocks | **Every writer, before and after B, landed in the new table**, on three runs. The retired original got none. |
| `SHOW CREATE TABLE x` under `lock_wait_timeout = 0` while B holds `x` | 1205. Where B is only waiting for `x` behind another reader, it succeeds. For an absent `r` that B holds, it is also 1205; nobody holding it gives 1146. |
| `INSERT … SELECT … FOR SHARE ON DUPLICATE KEY UPDATE` | **Error 1064.** `LOCK IN SHARE MODE` is accepted, and waited 1.7 s for an uncommitted delete without copying the row. |
| `CREATE TABLE s LIKE t` | Every check's name is kept: user-named, `CONSTRAINT_n`, and the json column's `json_valid` check named after the column. AUTO_INCREMENT is not carried. |
| `SHOW SLAVE HOSTS` as the compose user | Error 1227: needs `REPLICATION MASTER ADMIN` |
| A reader started at a GTID whose log was purged | Error 1236, *Could not find GTID state requested by slave*. But `PURGE BINARY LOGS TO` keeps a file the binlog checkpoint still needs, with Note 1375, so a purge has to be repeated until the file is gone. |

## Decision Drivers

- **No queued writer fails, and none lands in the table being retired.** A write to the retired
  original after the catch-up is a lost write.
- MySQL's path stays as ADR 0091 measured it.
- The stream and the shadow stay one driver: MariaDB is a variant of `mysql`, not a fourth driver.

## Considered Options

### A. The cutover
- **A1: three connections beside the session** ✔:
  1. a **holder** takes `LOCK TABLES t WRITE` alone;
  2. the session catches up, carries AUTO_INCREMENT across, and COMMITs. That ends the engine's
     transaction, as `LOCK TABLES` does on MySQL, so the session holds no lock on `s`;
  3. a **renamer** sends `RENAME TABLE t TO r, s TO t`;
  4. a **prober** waits, within `safety.lock_timeout`, for PROCESSLIST to show the renamer waiting on a
     metadata lock, and for `SHOW CREATE TABLE` under `lock_wait_timeout = 0` to time out on each of `r`
     and `s` that sorts before `t`. Then the renamer holds those names, and its exclusive request on `t`
     is what is pending;
  5. the holder unlocks.

  A renamer not seen queued in time is killed, and the cutover fails as `ErrStatementLockTimeout`
  with nothing swapped, for the engine to retry. This happens, for example, when a dump's transaction
  holds a lock on the shadow.
- A2: `ALTER TABLE … RENAME` under the lock, one session. **Rejected by measurement:** it fails every
  queued writer.
- A3: gh-ost's sentry cutover. **Rejected by measurement:** the renamer waits on the sentry, so writers
  are let in first and write to the original. A sentry name sorting after `t` cannot also be the
  retired name the rename moves `t` to.
- A4: a rename with no lock, then a catch-up of what landed in the original. **Rejected on merit:** a
  change replayed after the swap can overwrite a newer write to the same key.
- A5: keep MariaDB refused. **Rejected:** A1 was measured to work.

The order a RENAME takes its names in is the server's sorted order, which the prober reads as a byte
comparison of the names as the catalog stores them. A crash after the renamer is queued finishes the
swap once the holder's session ends. The catch-up was complete by then, and the swap's record is
recovered from the catalog, as ADR 0091's crash window already is.

### B. The position
- **B1: `gtid:<domain>-<server>-<seq>[,…]`, read with go-mysql's MariaDB parser and streamed in its
  MariaDB flavor** ✔. The end of the log is `@@gtid_binlog_pos`. A set the other variant recorded is
  refused, naming the abandon remedy.
- On a fresh start where the server has binlogged nothing yet, the position is `file:<name>:<offset>`.
  An empty `slave_connect_state` would stream from the oldest log the server keeps.
- B2: a prefix of its own (`mariadb:`). **Rejected:** the server is known whenever a position is read,
  and the shared prefix keeps `migr status` uniform.

### C. What else differs
- **The copy reads `LOCK IN SHARE MODE`.**
- **`namedChecksOf` refuses nothing on MariaDB**, since LIKE keeps the names. Refusing them would have
  refused every table with a json column.
- **`Admits` refuses a SYSTEM VERSIONED table:** a copy carries the current rows and none of the history.
- **A group opens with `MariadbGTIDEvent`.** A standalone group ends at its statement, which already
  commits it. Annotate, checkpoint and GTID-list events move the offset alone. MariaDB's compressed row
  events are read as rows.
- **The capture fact also requires `REPLICATION MASTER ADMIN` on MariaDB.** `SHOW SLAVE HOSTS` needs it,
  and a reader that could not list its replicas' ids could take one, which disconnects that replica.
  **Rejected:** listing only the server's own id when refused, since that trades a clear refusal for a
  rare outage. Compose grants it in `initdb-mariadb/`, because MySQL has no privilege of that name.
- **The retention, `binlog_expire_logs_seconds`, is read on MariaDB too.**
- **The floor is MariaDB 11.4,** the one release measured.

### D. Compose
- **D1: a new service, `mariadb114binlog`, on port 55412** ✔ (by agreement).
- D2: turn on `log_bin` in `mariadb114`. **Rejected:** it would end the only MariaDB answering the
  log-off cases above.

## Decision Outcome

A1, B1, C and D1.

**Acceptance, on `mariadb114binlog`, in M13-T10's own suite (`shadowServers` now holds it):**
- eight writers;
- an `int32` → `int64` change and a widened varchar;
- **no failed write**, and the table equal to a control by count and checksum;
- every recorded position `gtid:`;
- three consecutive runs.

The rest of that suite passes there too:
- a stop mid-copy and mid-hold, each carried on;
- abandoning at both stages;
- an ALTER under the hold, which stops it;
- a purged position, refused naming the retention;
- a catch-up overrunning the lock timeout, which swaps nothing.

**Of its own**, it holds:
- eight writers queued behind the cutover, all landing in the new table, with fresh ids and the
  check names kept;
- a rename held back by a reader of the shadow, stopped, and not run once the reader lets go;
- a domain-server-seq position;
- json and named checks admitted, and system versioning refused.

In the end-to-end suite, a real `migr up` holds on `mariadb114binlog` and cuts over once confirmed.

## Consequences

### Positive
- MariaDB 11.4 runs every shadow-table migration MySQL does, and a queued writer is never failed or
  misplaced.
- A rename that could be let through late is killed, never waited out.

### Negative
- **The cutover opens three connections beside the session** for its length, in a pool of its own.
- **The prober adds latency:** writers wait for the catch-up plus one PROCESSLIST round trip per 5 ms
  until the rename is seen queued.
- **A session holding a lock on the shadow or the retired name makes every cutover attempt fail**
  until it lets go. Each attempt is bounded by `safety.lock_timeout` and retried with backoff.
- **The catch-up's writes commit before the rename.** A rename that then fails leaves them applied
  ahead of the recorded position, which a retry replays harmlessly.
- MariaDB below 11.4 stays refused, since nothing older was measured.

### Neutral
- ADR 0091's MySQL path, its cutover and its tests are unchanged.
