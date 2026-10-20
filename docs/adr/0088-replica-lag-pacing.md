# 0088. A chunk waits while a replica trails, and a replica migr cannot read counts as trailing

## Status

Accepted

## Date

2026-10-20

## Context

M13-T8 asks that chunk pacing:
- adapt to measured replica lag (`pg_stat_replication`, MySQL `Seconds_Behind_Master`);
- pause above `safety.replica_lag_threshold`;
- resume automatically;
- report the throttling state in progress output.

What existed (ADR 0078 F):
- `engine.Pacer` is asked before every chunk;
- `Pace{Rows, Wait, Throttle}` is what it answers;
- `Throttle` is printed in the progress line;
- `engine.Steady` is the only pacer;
- `safety.replica_lag_threshold` was validated, defaulting to 5s, and read by nothing.

The two engines report lag in different places:
- **PostgreSQL** reports every standby's on the primary migr already talks to.
- **MySQL and MariaDB** report it only on the replica itself.

Compose had no replicas. It now has a PostgreSQL 16 streaming standby with
`recovery_min_apply_delay = 3s`, and a MySQL 8.0 replica with `SOURCE_DELAY = 3` that replicates
`migr_lag` alone, so no other suite can stop its SQL thread.

## Measured first

**PostgreSQL 16:**
- `replay_lag` **trails the writes it measures**. It describes WAL already replayed, so straight after
  a write it reports an old sample (0.016s), and it climbs about a second per second of continuous
  writing, up to the delay.
- **It keeps its last value after the standby has caught up.** With writes stopped and
  `pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn)` at 0, it read 0.6s for about 10s and then NULL.
  It read 2.8s with nothing outstanding before the first test.
- **A role without `pg_read_all_stats`** still sees the row and its `application_name`, but `state`,
  `replay_lsn` and `replay_lag` are NULL.

**MySQL 8.0** (`SHOW REPLICA STATUS` on the replica):
- `Seconds_Behind_Source` climbs from 1 to 5s under writes and falls to 0 about 3s after they stop.
- With the SQL thread stopped it is NULL, beside `Replica_SQL_Running: No`.
- A user without `REPLICATION CLIENT` gets error 1227.
- A server that is not a replica answers no row.

## Decision Drivers

- Throttling exists to protect the replicas, so the safe failure is waiting, not writing.
- A paused backfill must resume by itself, with nothing to reset.
- No credential reaches a log line.

## Considered Options

### A. Where lag is read
- **A1: PostgreSQL on the primary, MySQL on each configured replica** ✔ (by agreement).
  - `driver.LagReader.ReplicaLag(ctx) ([]driver.Lag, error)` is optional on the Conn, like
    `Assessor`. A `Lag` holds a replica name, how far behind it is, and an `Err` for unknown.
  - **PostgreSQL** reads `pg_stat_replication` and needs no config. The
    `databases.<name>.replicas` key is refused there, and on SQLite, which has no replicas.
  - **MySQL** opens each `databases.<name>.replicas` DSN lazily, on its own pool with a 5s bound, and
    names the replica by `host:port` alone.
- A2: replica DSNs for every engine. **Rejected**: PostgreSQL already knows every standby, and a list
  an operator keeps would miss the one they forgot.
- A3: a heartbeat table (pt-heartbeat style). **Rejected**: it still needs the replica connection on
  MySQL, and it writes to production for monitoring's sake.

### B. Reading PostgreSQL
- **No outstanding WAL means caught up, whatever `replay_lag` says**, because of the stale reading.
- With WAL outstanding, `replay_lag` is the answer. **Outstanding and unmeasured** is unknown.
- **`state` NULL** is unknown, naming `pg_read_all_stats`. A state other than `streaming` is unknown
  too, naming the state.
- **`replay_lag` is PostgreSQL's own measure, and it errs late.** A short backfill can finish before
  it passes the threshold, which the test had to be lengthened for. A longer one is held once it
  does. Estimating time from bytes was rejected: it is a guess presented as a measurement.

### C. Reading MySQL
- **The statement follows the replica's version:**
  - `SHOW REPLICA STATUS` from MySQL 8.0.22 and MariaDB 10.5.1, otherwise `SHOW SLAVE STATUS`;
  - columns are read under either spelling, since MariaDB keeps `Slave_*` and
    `Seconds_Behind_Master` under the new statement.
- **Unknown, each with its own reason:**
  - a stopped IO or SQL thread, naming both and the last error;
  - NULL lag;
  - no row;
  - error 1227, naming REPLICATION CLIENT;
  - an unreachable replica.

### D. What unknown means
- **D1: as if the replica were too far behind** ✔ (by agreement). The chunk waits, and the lag is
  read again on every poll. The wait is bounded only by `safety.migration_timeout` and Ctrl-C,
  through the same `Sleeper` context a lock backoff uses.
- D2: warn and pace steadily. **Rejected**: it gives a broken replica the full write rate.

### E. How the pacer holds
- **`Pace.Hold`** ✔ means wait, then ask again; it is not "wait, then run".
  - `engine.Lagged{Steady, Reader, Threshold}` reads the lag on every `Pace`.
  - It holds while any replica is unknown or above the threshold, naming the worst replica, with an
    unknown one first. Otherwise it answers what `Steady` would.
  - **The pacer keeps no state**, so the resume is simply the next reading under the threshold.
  - The engine reports each hold through `Progress` with `Waiting` set, and counts the holds and the
    time waited on `Run`.
- **The poll is `max(chunk_pause, 1s)`**, an internal constant. A config key was rejected: nothing
  measured asks for one.
- An extended `Wait` without `Hold` was rejected: it would run the chunk after one wait whatever the
  lag had become.

## Decision Outcome

A1, B, C, D1 and E.

Measured through the whole stack:
- **PostgreSQL 16:** a 200-row backfill in chunks of 25, 300ms apart, under a 1s threshold, was held 3
  times and finished. The same run under a minute's threshold was never held for lag.
- **MySQL 8.0:** the same backfill was held 5 times as the replica read 2s to 5s behind, then
  finished.
- **By hand on MySQL,** with the replica's SQL thread stopped: 4 holds reading "replication is
  stopped", then 6 more while it caught up after the restart, then completion.

## Consequences

### Positive
- A backfill no longer outruns its replicas: it waits for them, says which one and how far behind it
  is, and goes on by itself.
- `migr plan` says what holds a chunk back, `-v` logs every held poll, and `migr up` reports
  "held back N times for Ts by replica lag".

### Negative
- **A replica migr cannot read stalls a backfill until it can, or until `migration_timeout`.** That
  is by agreement, and stated.
- **MySQL needs `REPLICATION CLIENT` on each replica**, and DSNs for replicas the operator keeps in
  `migr.yaml`.
- `replay_lag`'s lateness means PostgreSQL throttles a burst only once it has gone on longer than the
  threshold.

### Neutral
- SQLite paces steadily, as before.
- Compose gains two services, and the integration CI job runs them.
