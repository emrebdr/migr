# Zero-downtime migrations

This guide is for anyone running migr against a database that is serving traffic. It covers:

- what each engine can change without making clients wait, and what it cannot;
- how to change a column across several deploys;
- how to gate a pull request on lock cost;
- how a shadow-table migration is held and cut over.

Every claim here comes from a measurement or from the code. The per-engine tables in
[section 2](#2-what-each-operation-costs) are generated from each driver's own declaration, and a
test fails when they disagree, so a table you read here is the verdict `migr plan` prints.

## 1. What migr guarantees, and under which settings

### Lock cost

Every operation is declared at one of four lock costs, and `raw` at a fifth:

| Cost | What a client sees |
|---|---|
| `lock_free` | Nothing. Reads and writes proceed. |
| `brief_exclusive` | An exclusive lock for a metadata-only moment. Clients queued behind it wait for that moment, **and for as long as the statement waits to get the lock** (see below). |
| `long_exclusive` | An exclusive lock held for work that grows with the table. |
| `table_rewrite` | Every row is written to new storage, with clients blocked for the duration. |
| `unknown` | `raw` only: migr cannot know what a statement does, and treats it as the worst. |

The cost names how long the lock is held, not how strong it is. "Instant" DDL — PostgreSQL's stored
default, MySQL's `ALGORITHM=INSTANT` — is `brief_exclusive`, never `lock_free`. It still asks for an
exclusive lock and still waits for it.

A verdict is a **safe upper bound**. Where the cost depends on the table, the operation is declared
at its worst, and `migr plan` reads the table to lower it where it can (see the MySQL notes). An
operation on a table that the same migration creates costs `lock_free`, because nothing can be
waiting on a table that did not exist.

### Lock timeouts and retries

No DDL statement waits for a lock without a bound (CLAUDE.md, invariant #12):

- each statement runs under `safety.lock_timeout`, default 3 s. This is PostgreSQL's `lock_timeout`,
  MySQL's `lock_wait_timeout`, and SQLite's `busy_timeout`;
- a statement that gives up on its lock is retried with backoff: `safety.lock_retries` times (5),
  starting at `safety.lock_backoff` (500 ms), doubling to 30 s, with half of each delay random. Each
  retry is a warning, and the last one exits **3**;
- on PostgreSQL, the whole migration's transaction is retried. Where DDL commits as it goes (MySQL,
  MariaDB), the retry starts at the statement that waited. A statement that runs outside any
  transaction, such as `CREATE INDEX CONCURRENTLY`, is never retried; the run stops dirty instead.

### `brief_exclusive` is brief only under a short lock timeout

A statement waiting for its lock holds every client queued behind it for as long as it waits. The
zero-downtime suite measured this: under the default 3 s `lock_timeout`, a PostgreSQL 16 rename
queued behind autovacuum held readers for 1.0 s.

So **set `safety.lock_timeout` below the longest wait your clients can tolerate.** Two floors apply:

- on **PostgreSQL**, keep it at or above 1 s. Autovacuum yields to a waiting statement only after
  `deadlock_timeout` (1 s). Measured at 750 ms, every attempt of a concurrent build and a `VALIDATE`
  gave up before autovacuum was asked to yield;
- on **MySQL and MariaDB**, `lock_wait_timeout` takes whole seconds, so 1 s is the minimum, and a
  brief statement there may hold a client for up to that second by design.

### How this is proven

`make test-zerodowntime` runs every operation, up and down, through the same engine `migr up` runs,
against every compose server and SQLite. It works a 1M-row table with eight readers and eight
writers, and fails any operation declared `lock_free` or `brief_exclusive` that makes a client wait
past the budget (1.5 s). The run that set these numbers
([ADR 0096](adr/0096-zero-downtime-suite.md)) measured:

- the worst of 106 kept promises: 206 ms, PostgreSQL's concurrent `add_index`;
- every shadow-table cutover: 21–74 ms, with no committed write lost;
- honest rewrites: clients waited 1.4–41 s, which is why they are declared rewrites.

What the suite holds is migr with `lock_timeout` at 1.2 s (1 s on MySQL), not the shipped 3 s
default. That is the setting this guide recommends for a busy database.

## 2. What each operation costs

Each table lists one row per operation, plus a row per payload that changes the cost. The columns:

- **Declared cost** is the driver's verdict.
- **Cost as migr runs it** is what the run actually costs once a safer rewrite is applied (PostgreSQL
  only), naming the `safety.rewrites.*` key that switches it off.
- **Zero-downtime** is *yes* when that cost is at most `brief_exclusive`, and *where the table
  allows* when the declaration names a cheaper cost that `migr plan` confirms by reading the table.

The cost of a data operation (`data_copy`, `data_backfill`, `data_transform`) is declared as one
statement. In a data-only migration it runs in chunks of `safety.chunk_rows`, each its own short
transaction — measured, clients waited 20–40 ms — so its row is an over-report there. A data
operation beside a schema change in one migration runs as the one statement its row describes.

### PostgreSQL 16 and 17

<!-- migr:lock-table postgres -->
| Operation | Support | Declared cost | Cost as migr runs it | Zero-downtime |
|---|---|---|---|---|
| create_table | native | `lock_free` | `lock_free` | yes |
| drop_table | native | `long_exclusive` | `long_exclusive` | **no** |
| rename_table | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_column, nullable, no default | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_column, constant default | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_column, default evaluated per row (uuid) | native | `table_rewrite` | `table_rewrite` | **no** |
| drop_column | native | `brief_exclusive` | `brief_exclusive` | yes |
| rename_column | native | `brief_exclusive` | `brief_exclusive` | yes |
| alter_column, change type | native | `table_rewrite` | `table_rewrite` | **no** |
| alter_column, set NOT NULL | native | `long_exclusive` | `brief_exclusive`, rewritten (`safety.rewrites.not_null_check`) | yes |
| alter_column, drop NOT NULL | native | `brief_exclusive` | `brief_exclusive` | yes |
| alter_column, set default | native | `brief_exclusive` | `brief_exclusive` | yes |
| alter_column, drop default | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_index | native | `long_exclusive` | `lock_free`, rewritten (`safety.rewrites.concurrent_index`) | yes |
| drop_index | native | `long_exclusive` | `lock_free`, rewritten (`safety.rewrites.concurrent_index`) | yes |
| add_foreign_key | native | `long_exclusive` | `brief_exclusive`, rewritten (`safety.rewrites.not_valid`) | yes |
| drop_foreign_key | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_unique | native | `long_exclusive` | `brief_exclusive`, rewritten (`safety.rewrites.concurrent_index`) | yes |
| drop_unique | native | `long_exclusive` | `long_exclusive` | **no** |
| add_check | native | `long_exclusive` | `brief_exclusive`, rewritten (`safety.rewrites.not_valid`) | yes |
| drop_check | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_primary_key | native | `long_exclusive` | `brief_exclusive`, rewritten (`safety.rewrites.concurrent_index`) | yes |
| drop_primary_key | native | `long_exclusive` | `long_exclusive` | **no** |
| create_view | native | `lock_free` | `lock_free` | yes |
| create_view, or replace | native | `brief_exclusive` | `brief_exclusive` | yes |
| drop_view | native | `brief_exclusive` | `brief_exclusive` | yes |
| raw | native | `unknown` | `unknown` | **no** |
| data_copy | native | `long_exclusive` | `long_exclusive` | **no** |
| data_backfill | native | `table_rewrite` | `table_rewrite` | **no** |
| data_transform | native | `table_rewrite` | `table_rewrite` | **no** |
<!-- /migr:lock-table -->

- **The rewrites.** `add_index` and `drop_index` run `CONCURRENTLY`. `add_unique` and
  `add_primary_key` build their index concurrently, then attach it. `add_check` and `add_foreign_key`
  run as `NOT VALID` and then `VALIDATE`. `alter_column … NOT NULL` runs behind a validated
  `IS NOT NULL` check, on 12 and later. `safety.auto_rewrite: false` switches all of them off, and
  each one has its own key under `safety.rewrites`. Measured over 15M rows, a plain `ADD CHECK`
  blocked 34 of 35 writes for its 9.5 s scan, where the rewritten pair blocked none.
- **The concurrent statements are bounded by `safety.concurrent_timeout`** (unset: no bound) rather
  than the 30 s `statement_timeout`, since a large build takes longer and blocks no write. A failed
  build leaves an INVALID index; the next run repairs it, and `migr status` lists any that remain.
- An operation on a table the same migration created is never rewritten: nothing waits on it.
- `drop_table`, `drop_index`, `drop_unique` and `drop_primary_key` are `long_exclusive` because the
  server deletes the files before it releases the lock. The hold grew about ten-fold from kilobytes
  to hundreds of megabytes.
- PostgreSQL 10 and earlier rewrite the table for any default at all. migr reads the version and
  declares accordingly.

### MySQL 8.0

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

- **The table decides `add_column` and `drop_column`.** MySQL performs them instantly unless the
  table is `ROW_FORMAT=COMPRESSED`, carries a `FULLTEXT` index, or has reached 64 instant row
  versions, and it silently rebuilds the table when it is. `migr plan` reads the first queued
  migration's table and prints `(read from the table)` where it lowered the cost. **Reading the row
  versions needs `PROCESS`**, which migr never asks for: without it, the cost stays `table_rewrite`.
- `alter_column` other than dropping a default is a full column redefinition (`MODIFY COLUMN`),
  which migr declares a rewrite for every type change, even where the server would widen a
  `varchar` in place. That is the safe upper bound: the verdict cannot see the column's current type.
- MySQL rewrites no operation into a safer sequence. Every table-sized change on a hot MySQL table is
  a job for a [shadow table](#4-shadow-table-migrations).
- The table shows a server writing a binary log (`log_bin` on, MySQL 8.0's default), which refuses a
  per-row default.

### MariaDB 11.4

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

- MariaDB has no instant row-version limit, so only `COMPRESSED` and `FULLTEXT` keep `add_column`
  and `drop_column` from being instant.
- The table shows a server with `log_bin` off, MariaDB's default. With a binary log on, a per-row
  default is refused as on MySQL.

### SQLite

<!-- migr:lock-table sqlite -->
| Operation | Support | Declared cost | Cost as migr runs it | Zero-downtime |
|---|---|---|---|---|
| create_table | native | `brief_exclusive` | `brief_exclusive` | yes |
| drop_table | native | `long_exclusive` | `long_exclusive` | **no** |
| rename_table | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_column, nullable, no default | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_column, constant default | native | `brief_exclusive` | `brief_exclusive` | yes |
| add_column, default evaluated per row (uuid) | unsupported | — | — | — |
| drop_column | native | `table_rewrite` | `table_rewrite` | **no** |
| rename_column | native | `brief_exclusive` | `brief_exclusive` | yes |
| alter_column, change type | emulated (table rebuild) | `table_rewrite` | `table_rewrite` | **no** |
| alter_column, set NOT NULL | emulated (table rebuild) | `table_rewrite` | `table_rewrite` | **no** |
| alter_column, drop NOT NULL | emulated (table rebuild) | `table_rewrite` | `table_rewrite` | **no** |
| alter_column, set default | emulated (table rebuild) | `table_rewrite` | `table_rewrite` | **no** |
| alter_column, drop default | emulated (table rebuild) | `table_rewrite` | `table_rewrite` | **no** |
| add_index | native | `long_exclusive` | `long_exclusive` | **no** |
| drop_index | native | `long_exclusive` | `long_exclusive` | **no** |
| add_foreign_key | emulated (table rebuild) | `table_rewrite` | `table_rewrite` | **no** |
| drop_foreign_key | emulated (table rebuild) | `table_rewrite` | `table_rewrite` | **no** |
| add_unique | native | `long_exclusive` | `long_exclusive` | **no** |
| drop_unique | native | `long_exclusive` | `long_exclusive` | **no** |
| add_check | emulated (table rebuild) | `table_rewrite` | `table_rewrite` | **no** |
| drop_check | emulated (table rebuild) | `table_rewrite` | `table_rewrite` | **no** |
| add_primary_key | emulated (table rebuild) | `table_rewrite` | `table_rewrite` | **no** |
| drop_primary_key | emulated (table rebuild) | `table_rewrite` | `table_rewrite` | **no** |
| create_view | native | `brief_exclusive` | `brief_exclusive` | yes |
| create_view, or replace | native | `brief_exclusive` | `brief_exclusive` | yes |
| drop_view | native | `brief_exclusive` | `brief_exclusive` | yes |
| raw | native | `unknown` | `unknown` | **no** |
| data_copy | native | `long_exclusive` | `long_exclusive` | **no** |
| data_backfill | native | `table_rewrite` | `table_rewrite` | **no** |
| data_transform | native | `table_rewrite` | `table_rewrite` | **no** |

Refused on this server:

- **add_column, default evaluated per row (uuid)**: column "nickname" defaults to uuid(), and SQLite links no UUID function: the extension that provides one is not compiled into this library. The hex(randomblob()) idiom would write a generator into the schema rather than name one, so this driver refuses instead -- generate the value in the application, or use string(36)
<!-- /migr:lock-table -->

- SQLite takes one write lock for the whole database file, so even `create_table` is
  `brief_exclusive`: it waits for, and blocks, every other writer.
- Most changes to an existing table are an emulated **table rebuild**: a new table, every row copied,
  and a rename, preserving its checks and foreign keys. `migr plan` marks each rebuild with `→`.
- Run SQLite in WAL mode where other connections read and write while a migration runs. In the
  default rollback journal, eight concurrent writers measured a 2.0 s noise floor of their own.

## 3. What cannot be made zero-downtime

Every row above marked **no** blocks clients for as long as the work takes, on that engine, however
migr is configured. Stated plainly:

**PostgreSQL**
- Changing a column's type rewrites the table under an exclusive lock.
- Adding a column whose default is evaluated per row (`uuid`) rewrites the table.
- `drop_unique` and `drop_primary_key` hold their lock while the index's storage is deleted.
- A data operation beside a schema change runs as one statement in the migration's transaction.

**MySQL 8.0 and MariaDB 11.4**
- Changing a column's type or nullability redefines the column, which rebuilds the table.
- `add_index` and `add_unique` hold their lock for the build; `add_foreign_key`, `add_check`,
  `add_primary_key` and `drop_primary_key` rebuild the table.
- **A NOT NULL change can starve on a hot table.** MySQL's online rebuild logs every write made while
  it runs and must apply that log before it finishes. Measured under sixteen clients on 1M rows: with
  no pause between queries it never finished, its log growing as fast as it applied it; with a 2 ms
  pause it ran 988 s and failed with error 1799 (`innodb_online_alter_log_max_size`). It keeps its
  `table_rewrite` promise, but a hot table may never get the change in place.

**SQLite**
- Every rebuild — any `alter_column`, any key, check or foreign-key change, `drop_column` — copies the
  whole table under the database's one write lock.

**Every engine**
- `raw` is `unknown`: migr cannot know what your SQL locks.
- `drop_table` holds its lock while the storage goes.

What to do instead, in order of preference:

1. **A shadow table** ([section 4](#4-shadow-table-migrations)), where your server and table permit
   one: the copy runs beside the live table and only the rename at cutover is brief.
2. **Expand and contract** ([section 6](#6-changing-a-column-across-deploys)): add a new column, fill
   it in chunks, move the app across, and drop the old one, each step cheap.
3. **A maintenance window**, for what neither can reach.

## 4. Shadow-table migrations

A migration that declares `online: shadow_table` (`migr.Online` in Go, `online` in TypeScript) does
not alter its table in place. migr:

1. creates an empty copy with the change applied;
2. opens a change stream on the server's replication log — PostgreSQL logical decoding, the MySQL or
   MariaDB binary log — **before** it copies a row;
3. copies the table in chunks, replaying the captured changes onto the copy;
4. holds the copy in sync until the cutover is confirmed ([section 5](#5-the-cutover));
5. swaps the copy in by rename, under a brief lock, after catching up on the log;
6. keeps the original under a derived name until a separate step drops it.

The step costs `brief_exclusive`: the rename at cutover. There are no triggers, so the copy puts no
synchronous work on production writes.

### Where it is unavailable

`migr plan` marks a shadow-table migration that cannot run with `×` and the reason, and so does
`migr validate --connect`. `migr up` refuses it before creating anything. It is refused:

**On a server:**

| Engine | Refused when |
|---|---|
| SQLite | Always. It has no replication log. |
| PostgreSQL | `wal_level` is not `logical`; the user lacks `REPLICATION` (or superuser); no replication slot is free. |
| MySQL | Before 8.0.13; `log_bin` is off; `binlog_format` is not `ROW`; `binlog_row_image` is not `FULL`; the user lacks `REPLICATION SLAVE` or `REPLICATION CLIENT`. |
| MariaDB | Before 11.4; `log_bin` is off; `binlog_format` is not `ROW`; `binlog_row_image` is not `FULL`; the user lacks `REPLICATION SLAVE`, `BINLOG MONITOR` or `REPLICATION MASTER ADMIN`. |

Every missing prerequisite is named at once. migr never sets one.

**On a table:**

| Engine | Refused when the table |
|---|---|
| PostgreSQL | takes part in a foreign key either way; has a dependent view or rule; has triggers or row-level security; is partitioned or inherited; has a `REPLICA IDENTITY` other than `DEFAULT` or `FULL`. |
| MySQL | takes part in a foreign key either way; has triggers; has a user-named check; has a column type the copy cannot render. |
| MariaDB | takes part in a foreign key either way; has triggers; has a column type the copy cannot render; is `SYSTEM VERSIONED`. A user-named check is fine: MariaDB's `LIKE` keeps every check's name. |

**For an operation.** A shadow carries `add_column`, `drop_column`, `alter_column`, `add_index`,
`drop_index`, `add_check`, `drop_unique`, and on PostgreSQL `drop_check`. It refuses:

- `add_unique`, since the copy would silently keep one of each duplicate;
- `rename_column`, since the copy matches columns by name;
- both foreign-key operations, since a key would follow the original to its retired name;
- `drop_check` on MySQL and MariaDB, since `CREATE TABLE … LIKE` gives the shadow's checks names of
  its own;
- anything that is not a change to one existing keyed table.

### What a shadow costs the server

- **PostgreSQL** keeps a replication slot for the migration, and the slot retains WAL until the
  migration finishes. An abandoned slot fills the disk. `migr status`, `migr plan` and `migr cutover`
  name every orphaned `migr_` slot with its retained WAL, and `migr up` drops this project's own.
- **MySQL and MariaDB** keep nothing for a reader. A run stopped for longer than
  `binlog_expire_logs_seconds` cannot resume, because its position has been purged. `migr plan`
  prints that retention.

## 5. The cutover

### Holding, and confirming

When the copy has caught up, `migr up` **holds** it in sync and waits. It logs a warning naming the
command that confirms it. The rename is the one irreversible moment, so it happens when a person
says so, not whenever the copy happens to finish.

```console
$ migr up
level=WARN msg="holding the shadow table in sync until its cutover is confirmed -- migr cutover --confirm 20261020T140100 confirms it; Ctrl-C detaches and keeps it, exiting 6, and the next migr up carries the hold on" ...
```

From anywhere else:

```console
$ migr cutover                       # what waits, its lag, and how old that reading is
$ migr cutover --confirm 20261020T140100
```

`migr cutover` takes no migration lock, since the holding run holds it. `--confirm` records a flag
the holder reads, and the holder then cuts over. It exits **2** for a version nothing carries, and
**1** for one that is not waiting.

### The ways out

| You | migr |
|---|---|
| press Ctrl-C, or the job is sent SIGTERM | **detaches**: keeps the copy and its capture, records the stage, and exits **6**. `migr status` shows the migration as `waiting`. |
| run `migr up` again | carries the hold on from the recorded log position. |
| run `migr resolve --rolled-back` | abandons it: releases the stream, drops the copy, and says what it drops first. |
| run `migr down` | refuses while a migration is waiting. |

After the cutover, the original stays under its retired name until a later step drops it, so a
mistake is recoverable by hand until then.

### What the lag figure means

`migr cutover` shows each waiting migration's **lag**: how far its change stream trailed the end of
the log at the holder's last read.

- It is **0** whenever a read reached the end of the log as that read began. That is the normal state
  of a healthy hold, even under heavy writes.
- It is positive when a read stopped short of the end, and then it is the age of the last change
  read. On **PostgreSQL**, that is the time since that change committed. On **MySQL and MariaDB** it is
  the same, in **whole seconds**, from the binary log's event timestamps.
- The holder records it at least every 5 s. A reading older than 30 s is shown as *no run is holding
  it*, never as current: the holder has died or detached, and the next `migr up` resumes it.

A growing lag means the server writes faster than the copy replays. The cutover replays whatever is
outstanding under its lock, so confirm while the lag is low.

### Unattended cutover

`migr up --unattended` (or `MIGR_UNATTENDED=1`) confirms nothing and cuts over at the first read
that is caught up: one that found nothing new, or one whose lag is at most **`safety.cutover_lag`**.
The run then says *cut over unattended, 0s behind the log*.

- `cutover_lag` defaults to 1 s, or half of `lock_timeout` where that is shorter, and must be shorter
  than `lock_timeout`, since the catch-up under the cutover's lock has to fit in that bound.
- Until this release, unattended waited for a read that found nothing new, which a table under
  sustained writes never produces ([ADR 0097](adr/0097-unattended-cutover-lag.md)).

**When CI should pass `--unattended`:** in a pipeline with nobody to confirm — staging, preview
environments, a deploy you have decided to trust to the lag. Leave it off for production deploys
where a person should own the rename: the job then exits 6 with the copy held, and someone runs
`migr cutover --confirm`.

## 6. Changing a column across deploys

Some changes cannot be made in one deploy without breaking the app that is running: renaming a
column, changing its type, tightening it to NOT NULL. **Expand and contract** splits one change into
four migrations, each safe on its own, and migr runs them **a deploy apart**.

```go
// Go: one file per phase, each registering one migration.
migr.Register(migr.ReplaceColumn("accounts", "email", migr.String(255)).With(migr.String(320)).Expand())
migr.Register(migr.ReplaceColumn("accounts", "email", migr.String(255)).With(migr.String(320)).Backfill())
migr.Register(migr.ReplaceColumn("accounts", "email", migr.String(255)).With(migr.String(320)).Swap())
migr.Register(migr.ReplaceColumn("accounts", "email", migr.String(255)).With(migr.String(320)).Contract())
```

```ts
// TypeScript: one file per phase, each exporting one migration.
export default replaceColumn("accounts", "email", t.string(255)).with(t.string(320)).swap();
```

Restate the whole declaration in every phase file. A declaration shared from another package could
change what an applied migration emits without changing its source checksum.

### The deploy sequence

| Deploy | Migration | The app |
|---|---|---|
| 1 | **expand**: adds the new column, nullable whatever it ends as | writes both columns, reads the old |
| 2 | **backfill**: fills the new column from the old, in chunks | writes both, reads the old |
| 3 | **swap**: the new column becomes the one the app reads; the old is relaxed to nullable | writes both, reads the new |
| 4 | **contract**: drops the old column | uses only the new |

Each phase declares itself a step of the change. migr runs a step only under a **later release**
than the one that applied the step before it, so two phases never land in one deploy.

### Naming the release

Pass the deploy's identity on every `migr up`, from your build id:

```bash
migr up --release "$GITHUB_SHA"          # or MIGR_RELEASE=$GITHUB_SHA
```

Each batch's release is recorded. A run that reaches a step whose prior step was applied by **this**
release, or is not applied at all, **stops there** and prints:

```text
⏸ held: 20260727T120016_swap_account_email.go waits for a later release, and nothing after it runs
either -- 20260727T120015_backfill_account_email.go, its "backfill" step, belongs to release
"3f9c2e1", which is this one; this step runs under a later release, once the app "3f9c2e1" deploys
is serving everywhere.
```

and exits **0**. Everything after it waits too. This is not a failure: the next deploy runs it. A
queue holding any step with no `--release` is refused before anything runs, exit **2**.
`migr plan` and `--dry-run` stop at the same step.

### What to permit, and the unsafe steps

- The backfill is a `data_transform` and the contract a `drop_column`, both destructive, so the
  project must permit them: `validation.allow_destructive: [data_transform, drop_column]`.
- Swap (both ways), contract, and undoing expand warn, even in `migr plan`, because the app must have
  moved before they run.
- A contract after a lossy `lower`, `upper` or `trim` is irreversible rather than approximately
  undone.
- **On MySQL and MariaDB the swap is two commits**, since DDL commits as it goes. Between its two
  renames the column the app reads is briefly absent. migr says so at the hold.

## 7. Gating CI on lock cost

### On every pull request

```bash
migr validate --require-safe
```

`--require-safe` compiles every migration's **up** half as a run would — the session's rewrites
included, a table the migration creates costing nothing — and reports every operation above
`safety.max_lock_impact` (default `brief_exclusive`). It implies `--connect`, so it needs a database
of the same engine and version as production: the verdicts are version-dependent. An empty one is
enough. It reads no table state, so MySQL's table-dependent costs stay at their worst.

A `.sql` project passes only at `table_rewrite`, because every `raw` statement is `unknown`.

### Before a deploy, against staging

```bash
migr plan --require-safe
```

This prints the plan with each operation's lock cost and exits **5** if any exceeds the ceiling. The
first queued migration's table-dependent costs are read from the table, so this is where MySQL's
`add_column` can be confirmed instant.

### Exit codes a pipeline should branch on

| Code | Meaning | What the pipeline should do |
|---|---|---|
| 0 | Success, including a `⏸ held` step | Continue. A held step runs on the next deploy. |
| 1 | The world is not ready: unreachable database, dirty run, missing migration | Fail; read the message. |
| 2 | The argument is wrong: validation problems, a refused destructive operation, a step queued with no `--release` | Fail; fix the migration or the config. Wins over 5. |
| 3 | Lock contention, every retry spent | Retry the job later, or lower the contention. |
| 4 | An applied migration's file changed | Fail. Never auto-repair. |
| 5 | `--require-safe` found an operation above the ceiling | Fail the gate, or raise `max_lock_impact` deliberately. |
| 6 | A shadow-table migration is held, waiting for its cutover | Not a failure: page whoever confirms, or rerun with `--unattended`. |

### A GitHub Actions job

```yaml
jobs:
  migrations:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:17
        env: { POSTGRES_PASSWORD: postgres }
        ports: ["5432:5432"]
        options: --health-cmd pg_isready --health-interval 2s --health-retries 30
    env:
      DATABASE_URL: postgres://postgres:postgres@localhost:5432/postgres
    steps:
      - uses: actions/checkout@v4
      - run: migr validate --require-safe

  deploy:
    needs: migrations
    runs-on: ubuntu-latest
    environment: production
    env:
      DATABASE_URL: ${{ secrets.DATABASE_URL }}
    steps:
      - uses: actions/checkout@v4
      - run: migr plan --require-safe --release "$GITHUB_SHA"
      - name: migr up
        run: |
          set +e
          migr up --release "$GITHUB_SHA"
          code=$?
          if [ "$code" -eq 6 ]; then
            echo "::warning::a shadow-table cutover is waiting: run migr cutover --confirm"
            exit 0
          fi
          exit "$code"
```

Destructive operations are refused by `migr up` as well as by `migr validate`, exit **2**, unless
the kind is named in `validation.allow_destructive` or the run passes `--allow-destructive`. Keep
the list in `migr.yaml`, where a review sees it.

## 8. Settings that bear on downtime

| Key | Default | Why |
|---|---|---|
| `safety.lock_timeout` | 3s | Each DDL statement's wait for its lock. Lower it to your latency budget; 1 s minimum on PostgreSQL and MySQL ([section 1](#brief_exclusive-is-brief-only-under-a-short-lock-timeout)). |
| `safety.lock_retries` | 5 | Retries after a lock wait gives up; 0 tries once. |
| `safety.lock_backoff` | 500ms | The first retry's delay, doubled up to 30 s, half of it random. |
| `safety.statement_timeout` | 30s | Each statement and chunk. Must exceed `lock_timeout`. |
| `safety.concurrent_timeout` | none | Statements that block no write — a concurrent build, a `VALIDATE` — instead of `statement_timeout`. |
| `safety.migration_timeout` | none | Each migration with its retries. Never bounds a shadow-table hold. |
| `safety.max_lock_impact` | `brief_exclusive` | The ceiling `--require-safe` gates on. |
| `safety.auto_rewrite`, `safety.rewrites.*` | on | PostgreSQL's safer sequences. |
| `safety.chunk_rows` | 1000 | Rows per chunk of a data-only migration; 0 runs each as one statement. |
| `safety.chunk_pause` | 100ms | Between chunks. Zero is refused: an unthrottled backfill is itself an outage. |
| `safety.replica_lag_threshold` | 5s | A chunk, and a shadow copy, waits while any replica (or the change stream) trails by more. |
| `safety.cutover_lag` | 1s, at most half of `lock_timeout` | How far a stream may trail and an unattended cutover still count it caught up. |

`migr config` prints every resolved value.
