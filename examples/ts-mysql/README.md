# TypeScript + MySQL

Three TypeScript migrations against MySQL 8.0, ending in a data migration that runs in chunks, with
tests that need no database.

| Migration | What it shows |
|---|---|
| `20261101T110000_create_orders.ts` | a table with a `decimal` and a defaulted `timestamptz`; its down is derived |
| `20261101T110100_add_orders_status.ts` | an `enum` column, with an authored down recording what it drops |
| `20261101T110200_backfill_orders_status.ts` | a data-only migration, filling `status` where it is NULL, a chunk at a time |

## Run it

You need [migr](../../README.md#installing), Node 24 or later, and Docker for the database.

```console
$ npm install
$ docker compose up -d --wait
$ migr up
up — database "default", batch 1

  1  20261101T110000_create_orders.ts           1 statement  7.002ms
  2  20261101T110100_add_orders_status.ts       1 statement  13.839ms
  3  20261101T110200_backfill_orders_status.ts  1 chunk, 0 rows written  6.861ms

applied 3 migrations in 27.702ms
$ migr down --step 3
down — database "default", batch 2

  1  20261101T110200_backfill_orders_status.ts  0 statements  253µs
  2  20261101T110100_add_orders_status.ts       1 statement  14.856ms
  3  20261101T110000_create_orders.ts           1 statement  5.792ms

rolled back 3 migrations in 20.901ms
$ docker compose down -v
```

The DSN is go-sql-driver's form, `user:password@tcp(host:port)/database`. `migr.yaml` reads it from
`DATABASE_URL`, falling back to the compose database on port 53306.

## What to look at

- **The backfill runs in chunks.** A migration holding nothing but data operations runs each one
  `safety.chunk_rows` rows at a time (1,000 by default), each chunk its own short transaction, with a
  pause between them. A run that stops partway carries on from its last committed chunk at the next
  `migr up`. `migr plan` prints the chunk:

  ```text
  1  data_backfill  native  brief_exclusive (declared table_rewrite, one chunk at a time)
     chunks: 1000 rows at a time, pausing 100ms between them, ..., each committed on its own
  ```

- **The backfill's down emits nothing, on purpose.** Filling NULLs records nothing a rollback could
  restore, and the column itself is dropped by the migration before it. An authored down that emits
  no operations says "nothing to undo".
- **On MySQL, DDL commits as it goes.** A migration of two DDL statements that fails at the second
  leaves the first applied and the migration *dirty*. `migr status` shows it, and `migr resolve`
  settles it.
- **Why CI uses `--connect` rather than `--require-safe` here:** on MySQL, what an `add_column` costs
  depends on the table (a compressed or full-text-indexed table is rebuilt), so it is declared at its
  worst, `table_rewrite`. `migr validate` reads no table state, so `--require-safe` refuses every
  `add_column` (exit 5). `migr plan --safety` against a copy of production reads the first pending
  migration's table and reports its real cost.

## Tests

```console
$ npm test
```

[`test/migrations.test.ts`](test/migrations.test.ts) uses `@migr/sdk/testing`:
- `capture` and `captureDown` report what each half emits;
- `reversal` says how a migration is undone;
- `project` runs the real `migr` binary on a throwaway SQLite file: it validates the migrations and
  runs them up and down twice, which shows the rollback leaves nothing behind.

They need `migr` on `PATH` (or `MIGR_BIN`), and no database.

## CI

[`.github/workflows/migrations.yml`](.github/workflows/migrations.yml) runs on every pull request,
against a MySQL 8.0 service container. It runs the tests, `migr validate --connect` (what this server
refuses, and every reference a migration makes), and a round trip. Copy it into your own repository.
It installs migr from npm as `@migr/cli`, so it needs a published release.

## Using this as a starting point

This project links the TypeScript SDK from this repository, since it isn't published yet. In your
own copy, replace `"file:../../sdk/typescript"` in `package.json` with a version of `@migr/sdk`.
