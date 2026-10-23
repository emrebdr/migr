---
title: MySQL and MariaDB
---

# MySQL and MariaDB

`driver: mysql` serves both, tested against **MySQL 8.0** and **MariaDB 11.4**. The driver reads which
variant it is connected to at `Open`, and a few answers differ between them, so each has a table of
its own below.

```yaml
databases:
  default:
    driver: mysql
    dsn: ${DATABASE_URL}          # user:password@tcp(host:3306)/db
    replicas: [${REPLICA_DSN}]    # optional: where replica lag is read, by host:port
```

## How a migration runs

- **DDL commits as it runs.** Neither server can roll a schema change back. A migration is therefore
  a sequence of committed statements. One that fails part way is recorded **dirty**, naming the
  statements that survived, and every later run is refused until it's settled:
  - `migr resolve --resume` carries it on from the statement that failed;
  - `--applied` or `--rolled-back` records what you did by hand.
- **A lock wait is retried from the statement that waited.** The statements before it already
  committed, and running them again would fail.
- **The migration lock is `GET_LOCK`**, a named lock that dies with its session. A crashed run frees
  it as soon as the server reaps the connection.
- **A canceled statement is stopped with `KILL QUERY`** from a second connection, before the call
  returns, so a queued `ALTER` doesn't keep waiting for its whole `lock_wait_timeout`.
- **A migration's own reads** are read-only twice over: a read-only transaction, and
  `transaction_read_only` pinned around it. DDL commits implicitly and would escape the first alone.
  The placeholder is `?`.

## Changing a column is a redefinition

MySQL has no statement that changes one attribute of a column. A type or nullability change is a
`MODIFY COLUMN`, which drops every attribute it doesn't restate. So `alter_column` is **emulated**:
the driver reads the column as it stands and restates everything else. It refuses rather than guesses
where a redefinition would silently lose something. `alter_column` that only drops a default compiles
without reading the table.

## Costs that depend on the table

Which algorithm a server picks can depend on the table itself. A `COMPRESSED` or `FULLTEXT`-indexed
table is rebuilt for an `add_column` or `drop_column` that is otherwise instant, and so is a MySQL
table at 64 instant row versions. Those operations are declared at their worst, `table_rewrite`, and
`migr plan` reads the first queued migration's table to confirm the cheaper cost, marked
*(read from the table)*. Without the `PROCESS` grant, MySQL 8.0.29 and later can't report row versions,
so the cost stays at its worst. migr never asks for that grant.

## Shadow tables

A migration declaring `online: shadow_table` is performed through a copy of the table, kept current
by reading the server's **binary log** in row format, and swapped in by rename after you confirm it
(see the [zero-downtime guide](../zero-downtime.md#_5-the-cutover)). The log position is recorded:
- as a GTID set where the server records GTIDs, and as a file and offset where it doesn't;
- on MariaDB, as its own `domain-server-sequence` GTID.

**The server must permit capture**, and `migr plan` names every missing prerequisite at once:
- `log_bin` on, `binlog_format = ROW`, `binlog_row_image = FULL`;
- the grants `REPLICATION SLAVE` and `REPLICATION CLIENT`. On MariaDB these are `BINLOG MONITOR`, plus
  `REPLICATION MASTER ADMIN`;
- MySQL 8.0.13 or later, or MariaDB 11.4 or later.

A stopped run reads on from its recorded position, as long as the server still keeps the log there.
`migr plan` prints `binlog_expire_logs_seconds`, because neither server holds the log for a reader
that isn't connected.

**A table is refused** if it has:
- foreign keys, in either direction;
- triggers;
- a column type the replay can't render;
- on MySQL, checks with names you gave them;
- on MariaDB, system versioning.

**These operations are refused through a shadow table:** `add_unique`, `rename_column`, `drop_check`,
and both foreign-key operations.

## Replica lag

A chunked data migration waits while any replica named in `databases.<name>.replicas` trails by more
than `safety.replica_lag_threshold` (5 s), read with `SHOW REPLICA STATUS`. A replica that can't be
read holds the migration too, rather than letting it run unpaced.

## What the catalog can't say

- **`timestamp` and `timestamptz`** are both stored as `datetime(6)`, so a column read back is a guess
  between them. `migr diff` refuses to act on a guess.
- **MariaDB's `json`** is a `longtext` with a `json_valid` check, which is how it is told apart from `text`.
- **On MariaDB, `RESTRICT` reads back as `no_action`**: the server reports `RESTRICT` for a clause
  that was never written.

## What it costs: capabilities and lock impact

### MySQL 8.0

As compose runs it, with the binary log on.

<!-- migr:lock-table mysql 8.0 -->
| Operation | Support | Declared cost | Cost as migr runs it | Zero-downtime |
|---|---|---|---|---|
| create_table | native | `lock_free` | `lock_free` | yes |
| drop_table | native | `long_exclusive` | `long_exclusive` | **no** |
| rename_table | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_column, nullable, no default | native | `table_rewrite`, or `brief_exclusive` where the table is not ROW_FORMAT=COMPRESSED and carries no FULLTEXT index, and has fewer than 64 instant row versions -- which only a session holding PROCESS can read, so without it this stays a rewrite | `table_rewrite` | where the table allows |
| add_column, constant default | native | `table_rewrite`, or `brief_exclusive` where the table is not ROW_FORMAT=COMPRESSED and carries no FULLTEXT index, and has fewer than 64 instant row versions -- which only a session holding PROCESS can read, so without it this stays a rewrite | `table_rewrite` | where the table allows |
| add_column, default evaluated per row (uuid) | unsupported | — | — | — |
| drop_column | native | `table_rewrite`, or `brief_exclusive` where the table is not ROW_FORMAT=COMPRESSED and carries no FULLTEXT index, and has fewer than 64 instant row versions -- which only a session holding PROCESS can read, so without it this stays a rewrite, and the column is in no index | `table_rewrite` | where the table allows |
| rename_column | native | `brief_exclusive` | `brief_exclusive` | yes |
| alter_column, change type | emulated (column redefinition) | `table_rewrite` | `table_rewrite` | **no** |
| alter_column, set NOT NULL | emulated (column redefinition) | `table_rewrite` | `table_rewrite` | **no** |
| alter_column, drop NOT NULL | emulated (column redefinition) | `table_rewrite` | `table_rewrite` | **no** |
| alter_column, set default | emulated (column redefinition) | `brief_exclusive` | `brief_exclusive` | yes |
| alter_column, drop default | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_index | native | `long_exclusive` | `long_exclusive` | **no** |
| drop_index | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_foreign_key | native | `table_rewrite` | `table_rewrite` | **no** |
| drop_foreign_key | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_unique | native | `long_exclusive` | `long_exclusive` | **no** |
| drop_unique | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_check | native | `table_rewrite` | `table_rewrite` | **no** |
| drop_check | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_primary_key | native | `table_rewrite` | `table_rewrite` | **no** |
| drop_primary_key | native | `table_rewrite` | `table_rewrite` | **no** |
| create_view | native | `lock_free` | `lock_free` | yes |
| create_view, or replace | native | `brief_exclusive` | `brief_exclusive` | yes |
| drop_view | native | `brief_exclusive` | `brief_exclusive` | yes |
| raw | native | `unknown` | `unknown` | **no** |
| data_copy | native | `long_exclusive` | `long_exclusive` | **no** |
| data_backfill | native | `table_rewrite` | `table_rewrite` | **no** |
| data_transform | native | `table_rewrite` | `table_rewrite` | **no** |

Refused on this server:

- **add_column, default evaluated per row (uuid)**: column "nickname" is added with a uuid default, which MySQL writes into every existing row and therefore refuses outright with error 1674 while it writes a binary log: the value would differ on a replica. This server has log_bin on. A new table may declare the same default, and so may a later alter_column, so adding the column without one and setting it afterwards is two operations that do work -- or generate the value in the application
<!-- /migr:lock-table -->

### MariaDB 11.4

As compose runs it, with the binary log off.

<!-- migr:lock-table mariadb 11.4 -->
| Operation | Support | Declared cost | Cost as migr runs it | Zero-downtime |
|---|---|---|---|---|
| create_table | native | `lock_free` | `lock_free` | yes |
| drop_table | native | `long_exclusive` | `long_exclusive` | **no** |
| rename_table | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_column, nullable, no default | native | `table_rewrite`, or `brief_exclusive` where the table is not ROW_FORMAT=COMPRESSED and carries no FULLTEXT index | `table_rewrite` | where the table allows |
| add_column, constant default | native | `table_rewrite`, or `brief_exclusive` where the table is not ROW_FORMAT=COMPRESSED and carries no FULLTEXT index | `table_rewrite` | where the table allows |
| add_column, default evaluated per row (uuid) | native | `table_rewrite` | `table_rewrite` | **no** |
| drop_column | native | `table_rewrite`, or `brief_exclusive` where the table is not ROW_FORMAT=COMPRESSED and carries no FULLTEXT index | `table_rewrite` | where the table allows |
| rename_column | native | `brief_exclusive` | `brief_exclusive` | yes |
| alter_column, change type | emulated (column redefinition) | `table_rewrite` | `table_rewrite` | **no** |
| alter_column, set NOT NULL | emulated (column redefinition) | `table_rewrite` | `table_rewrite` | **no** |
| alter_column, drop NOT NULL | emulated (column redefinition) | `table_rewrite` | `table_rewrite` | **no** |
| alter_column, set default | emulated (column redefinition) | `brief_exclusive` | `brief_exclusive` | yes |
| alter_column, drop default | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_index | native | `long_exclusive` | `long_exclusive` | **no** |
| drop_index | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_foreign_key | native | `table_rewrite` | `table_rewrite` | **no** |
| drop_foreign_key | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_unique | native | `long_exclusive` | `long_exclusive` | **no** |
| drop_unique | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_check | native | `table_rewrite` | `table_rewrite` | **no** |
| drop_check | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_primary_key | native | `table_rewrite` | `table_rewrite` | **no** |
| drop_primary_key | native | `table_rewrite` | `table_rewrite` | **no** |
| create_view | native | `lock_free` | `lock_free` | yes |
| create_view, or replace | native | `brief_exclusive` | `brief_exclusive` | yes |
| drop_view | native | `brief_exclusive` | `brief_exclusive` | yes |
| raw | native | `unknown` | `unknown` | **no** |
| data_copy | native | `long_exclusive` | `long_exclusive` | **no** |
| data_backfill | native | `table_rewrite` | `table_rewrite` | **no** |
| data_transform | native | `table_rewrite` | `table_rewrite` | **no** |
<!-- /migr:lock-table -->
