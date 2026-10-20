# 0090. Whether a server permits log-based capture is read from it at Open, and an unread answer permits nothing

## Status

Accepted

## Date

2026-10-20

## Context

M13-T16 asks for three things:
- `capability.Facts` carries whether the connected server permits log-based change capture;
- each driver declares that from the server rather than from documentation;
- a shadow-table migration against a server that cannot capture is refused before anything is
  created, naming the setting or privilege that is missing.

It was folded into M13-T9, which depends on it, by agreement. `internal/capability/doc.go` had
reserved the field since M2-T2, as a struct field rather than an interface method so that no driver
reopens for it.

## Measured first

On compose, as the `migr` user and as an unprivileged one:

| Server | Settings | `migr` holds | Unprivileged |
|---|---|---|---|
| PostgreSQL 16 | `wal_level = replica`, 10 slots, none used | superuser | neither superuser nor `REPLICATION` |
| PostgreSQL 17 | `wal_level = logical` (`-c wal_level=logical`, added for this task) | superuser | — |
| MySQL 8.0.46 | `log_bin = 1`, `ROW`, `FULL` | `PROCESS` globally, nothing replication-related | with both grants: `REPLICATION SLAVE` and `REPLICATION CLIENT` in `information_schema.USER_PRIVILEGES` |
| MariaDB 11.4.13 | `log_bin = 0`, `binlog_format = MIXED`, `FULL` | — | with both grants: `REPLICATION SLAVE` and **`BINLOG MONITOR`**, the name MariaDB 10.5 gave `REPLICATION CLIENT` |

**`SHOW GRANTS` on MariaDB prints the password hash**, so the grants are read from
`USER_PRIVILEGES` instead, which lists one privilege per row and no credential.

## Decision Drivers

- Invariant #13's posture: what cannot be determined is reported pessimistically.
- Every missing prerequisite is named at once, so an operator fixes them in one pass.
- A setting that needs a restart is reported, never changed: `wal_level` and `log_bin` are restarts.

## Considered Options

### A. The shape
- **A1: `Facts.Capture` is `capability.Capture{Permitted bool; Missing []string}`** ✔:
  - each entry names a setting or privilege and what it needs;
  - `Set.Capture()` returns it;
  - `Reason()` joins the entries;
  - the **zero value permits nothing** and says the driver did not answer, so a driver that forgets the
    field refuses rather than permits.
- A2: a per-operation verdict. **Rejected**: `wal_level` describes the server, not an operation, which
  `internal/capability/doc.go` recorded.

### B. What each driver reads, once, at Open
- **PostgreSQL**, in one query:
  - `current_setting('wal_level')` must be `logical`;
  - the role must have `rolsuper OR rolreplication`;
  - `max_replication_slots` must exceed the slots in use.
- **MySQL and MariaDB:**
  - `@@global.log_bin`, already read for the uuid-default refusal, must be on;
  - `binlog_format` must be `ROW`;
  - `binlog_row_image` must be `FULL`, or an update arrives without the whole row an upsert needs;
  - `USER_PRIVILEGES` for the session's own grantee must hold `REPLICATION SLAVE`, and
    `REPLICATION CLIENT` or `BINLOG MONITOR`.
- **SQLite** never permits, missing "a replication log, which SQLite does not have".

### C. An answer that cannot be read
**C1: not permitted, naming what could not be read** ✔. Failing `Open` was rejected: a server that will
not say whether it replicates still runs every other migration.

## Decision Outcome

A1, B and C1. `engine.performShadow` reads `Set.Capture()` first, before anything is created.
`migr validate --connect` and `migr plan` read it for a migration declaring a shadow table.

Measured against compose:
- PostgreSQL 16 declares `wal_level = logical (it is replica, …)`;
- PostgreSQL 17 permits;
- a non-replicating role on 17 is refused naming the `REPLICATION` attribute alone;
- MySQL 8.0 refuses `migr` for the two privileges, and permits a user holding both;
- MariaDB names `log_bin`, `binlog_format` and both privileges;
- an unreadable answer is not permitted on both engines.

## Consequences

### Positive
- A managed database withholding replication is told so by name, before anything is created, rather
  than given a slower path (ADR 0014's Question A).

### Negative
- Two to three more round trips at `Open`, on every command that connects.
- A server whose grant arrives through a role rather than directly is read from `USER_PRIVILEGES` as
  the grantee it is, which a role-granted privilege may not appear under. That reads as missing, the
  pessimistic side, and a later measurement may widen the read.

### Neutral
- postgres17 in compose now runs `wal_level = logical`, which every other suite ignores.
