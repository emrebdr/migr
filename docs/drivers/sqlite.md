---
title: SQLite
---

# SQLite

`driver: sqlite`. The engine is linked into the `migr` binary (pure Go, no C library), so the SQLite
version is a fact about the build rather than about your database, and every session migr opens has
it.

```yaml
databases:
  default:
    driver: sqlite
    dsn: ./app.db                 # a file path; an empty DSN is refused
```

An empty DSN is refused: SQLite reads one as a private temporary database deleted on exit, so every
migration would apply to a file nobody can find. `:memory:` is allowed, for tests.

## How a migration runs

- **One transaction per migration, begun `IMMEDIATE`.** SQLite's DDL is transactional. Taking the
  write lock at `BEGIN` waits for it under `busy_timeout`, where a deferred transaction in WAL mode
  could fail at its first write.
- **migr owns three pragmas, sets them on every session, and reads them back:**
  - `foreign_keys = ON`, so a migration that breaks referential integrity fails rather than leaving
    dangling rows;
  - `busy_timeout = safety.lock_timeout`, so every statement waits for the write lock, but never
    without a bound;
  - `query_only`, switched on for the length of each of a migration's own reads.

  A DSN that sets one of them is refused, naming it.
- **`journal_mode` is left as you set it.** WAL lets readers keep reading during a migration, but
  it's a durable change to your database file, so it's yours to make.
- **The migration lock is an `flock`** on a lock file beside the database. The kernel releases it if
  migr dies, so a crash never wedges the database. The placeholder is `?`.

## Emulated operations

SQLite can't alter a column or add or drop a constraint in place, so seven operations are performed
by a **table rebuild**: create the new shape, copy every row, drop the old table, rename. Each
rebuild is compiled against the table as it stands at that point in the run, so it preserves the
checks, foreign keys and indexes nothing in the operation mentions. `migr plan` marks each
emulated operation `→` and expands the first one exactly. `migr validate` reports them `unchecked`,
because it opens no database. A migration holding one can't be resumed part way.

## What it can't do

- **No shadow tables.** SQLite has no replication log to read, so `online: shadow_table` is refused
  before anything is created.
- **Every change takes the database's one write lock.** Even `create_table` is `brief_exclusive`,
  because a single lock covers the whole file. On a busy database, keep `safety.lock_timeout` short.

## What the catalog can't say

- **`int16`, `int32` and `int64`** all declare as `integer`, and **`float32`** as a real, so a column
  read back is a guess between them, and `migr diff` refuses to act on a guess.
- **A `decimal` reads back as a float** in a migration's own read, because SQLite stores it with REAL
  affinity.
- **An `enum`** is an unnamed `CHECK (c IN (…))`, which is how it is recognized again.

## What it costs: capabilities and lock impact

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
