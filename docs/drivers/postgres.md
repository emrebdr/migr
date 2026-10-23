---
title: PostgreSQL
---

# PostgreSQL

`driver: postgres`, tested against PostgreSQL 16 and 17. The table below is what both declare,
since they declare the same costs. Older servers connect, and every verdict is resolved against the
version `Open` detects: PostgreSQL 10, for example, rewrote the table for an `ADD COLUMN … DEFAULT`
that 11 and later record as metadata.

```yaml
databases:
  default:
    driver: postgres
    dsn: ${DATABASE_URL}        # postgres://user:password@host:5432/db?sslmode=verify-full
    schema: public              # the one namespace migr reads and changes
```

## How a migration runs

- **One transaction per migration.** PostgreSQL's DDL is transactional, so a migration that fails
  leaves nothing behind. A lock wait that outlasts `safety.lock_timeout` rolls the whole migration
  back and reruns it after a backoff, so no lock is held while it waits.
- **The migration lock is a session advisory lock.** It dies with the session that took it, so a
  crashed run never wedges the database. A wait that times out names the session holding it.
- **A canceled statement is stopped on the server**, through PostgreSQL's cancel request, before the
  call returns. The session survives, so the run can still record what happened.
- **A migration's own reads** run in a read-only transaction, so the server refuses a write hidden
  in one. Values come back as the server's text. The placeholder is `$1`.

## Rewrites: safer forms of the same change

A session performs these operations as a sequence that blocks no write, and `migr plan` marks each
one `↳ rewritten`. Each has a switch under `safety.rewrites`, and `safety.auto_rewrite: false` turns
them all off.

| Operation | Performed as | Switch |
|---|---|---|
| `add_check`, `add_foreign_key` | `ADD … NOT VALID`, then `VALIDATE CONSTRAINT` outside any transaction | `not_valid` |
| `alter_column … NOT NULL`, no retype, 12+ | a validated `IS NOT NULL` check, then `SET NOT NULL`, then the check dropped | `not_null_check` |
| `add_index`, `drop_index` | `CREATE INDEX CONCURRENTLY`, `DROP INDEX CONCURRENTLY` | `concurrent_index` |
| `add_unique` | a concurrent unique build, then `ADD CONSTRAINT … UNIQUE USING INDEX` | `concurrent_index` |
| `add_primary_key`, 12+ | validated `IS NOT NULL` checks first, so attaching the key scans nothing | `concurrent_index` |

- **An operation on a table the same migration creates is never rewritten.** Nothing can be waiting
  on that table, and splitting the migration would only cost atomicity.
- **Each `VALIDATE` and concurrent statement runs outside any transaction.** It is bounded by
  `safety.concurrent_timeout` (unset: no bound), not the 30 s `statement_timeout`.
- **A failed concurrent build leaves an INVALID index, and the next run cleans it up.** It drops the
  leftover and retries, or rebuilds it with `REINDEX INDEX CONCURRENTLY` on a resume, and reports
  `repaired: …`. `migr status`, `migr plan` and `migr validate --connect` list any INVALID index left
  with both remedies.

## Shadow tables

A migration declaring `online: shadow_table` is performed through a copy of the table, kept current
by **logical decoding**: a replication slot and a publication of the one table, both named for the
migration. The copy is swapped in by rename after you confirm it (see the
[zero-downtime guide](../zero-downtime.md#_5-the-cutover)).

**The server must permit capture**, and `migr plan` names every missing prerequisite at once:
- `wal_level = logical`;
- a role with the `REPLICATION` attribute, or superuser;
- a free replication slot.

**A table is refused** before anything is created if it has:
- foreign keys, in either direction;
- dependent views or rules;
- triggers or row-level security;
- partitions or inheritance;
- a `REPLICA IDENTITY` other than `DEFAULT` or `FULL`.

**No slot outlives its migration.** The cutover's transaction drops it, and an orphan, a slot whose
migration cut over or never recorded a state, is dropped by the next `migr up`. `migr status`,
`migr cutover` and `migr plan` name every slot holding WAL, with the WAL it retains.

## Replica lag

A chunked data migration waits while any standby trails by more than
`safety.replica_lag_threshold` (5 s). PostgreSQL reports its standbys' lag on the primary
(`pg_stat_replication`), so no replica DSNs are configured; `databases.<name>.replicas` is refused
for this driver.

## What it costs: capabilities and lock impact

*Declared cost* is the driver's verdict for the operation. *Cost as migr runs it* is what a session
pays after the rewrites above. The four drops that delete storage are `long_exclusive` because the
files are unlinked before the lock is released.

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
