---
title: SQL
---

# Writing migrations in SQL

A `.sql` migration is engine SQL, sent as written. It's the quickest way to start, and it's the right
choice for a change no builder call expresses. What it gives up is everything migr learns by
*reading* a change:
- the migration runs only on the engine it was written for;
- migr can't derive its down, so you write one or declare there is none;
- migr can't tell what it costs a live database, so its lock cost is `unknown`.

```yaml
migrations:
  dir: ./migrations
  language: sql
validation:
  allow_destructive: [raw]     # migr init writes this: every .sql statement is a raw operation
```

## The file

```console
$ migr create add_users
created migrations/20261023T171330_add_users.sql
```

```sql
-- migr:up
CREATE TABLE users (
  id    INTEGER PRIMARY KEY,
  email TEXT NOT NULL UNIQUE
);

-- migr:down
DROP TABLE users;
```

Four markers structure a file, each alone on its line:

| Marker | Means |
|---|---|
| `-- migr:up` | what follows runs when the migration is applied |
| `-- migr:down` | what follows runs when it is rolled back |
| `-- migr:irreversible: <reason>` | in place of a down: a rollback halts before its first statement and prints the reason |
| `-- migr:no-transaction` | directly above one statement, which then runs outside any transaction |

**Statements run in order, in one transaction** wherever the engine has transactional DDL, which is
PostgreSQL and SQLite. On MySQL and MariaDB each statement commits as it runs.

**A statement that can't run in a transaction** gets the marker directly above it, and the
migration then runs as parts: the statements before it in one transaction, the marked statement on
its own, and the rest in another. `migr plan` says how many parts:

```sql
-- migr:up
-- migr:no-transaction
CREATE INDEX CONCURRENTLY idx_users_email ON users (email);

-- migr:down
-- migr:no-transaction
DROP INDEX CONCURRENTLY idx_users_email;
```

**A migration with no inverse says so, and why:**

```sql
-- migr:up
CREATE TABLE legacy_archive AS SELECT * FROM users;

-- migr:irreversible: the archive is a one-time export, and rolling it back would discard it
```

```console
$ migr down
migr: 20261023T171500_drop_legacy.sql: this migration declares that it cannot be undone: the archive is a one-time export, and rolling it back would discard it
```

A marker inside a quoted body isn't one, and a marker that declares nothing is refused, naming its
line.

## What migr can still do for SQL

- **Drift detection.** An applied file that changes is refused (exit 4). Only line endings are
  normalized, so the check survives a checkout on another OS.
- **Dirty-state recovery.** A migration that stops part way is recorded with the statement it
  stopped at, and `migr resolve` settles it.
- **Locking, timeouts and retries.** Every statement runs under `safety.lock_timeout` and
  `safety.statement_timeout`, and a lock wait is retried with backoff.
- **`migr plan`** prints every statement, and marks each one that runs outside a transaction.

## What it can't

- **No derived down.** Write `-- migr:down`, or declare the migration irreversible.
- **No lock-cost gate.** Every statement is `raw`, and a raw statement's cost is `unknown`, so
  `migr validate --require-safe` fails a `.sql` project unless `safety.max_lock_impact` is
  `table_rewrite`, which allows everything. Review `migr plan` instead, or write the migrations whose
  cost matters in [Go](go.md) or [TypeScript](typescript.md). One directory can hold both.
- **No portability.** The SQL is the engine's. Moving to another database means rewriting it.
- **No `migr diff` over a `.sql` project.** Engine SQL has no readable effect, so migr can't
  project the schema the migrations describe.

## Example project

[`examples/mixed-sqlite`](../../examples/mixed-sqlite/README.md) keeps a `.sql` migration beside Go
and TypeScript ones in one directory, on SQLite.
