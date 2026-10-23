# Go + PostgreSQL

Three Go migrations against PostgreSQL 17, one for each way a migration can be undone, with tests
that need no database.

| Migration | Undone by |
|---|---|
| `20261101T100000_create_accounts.go` | **derived**: it writes no `Down`, so migr inverts what `Up` emits |
| `20261101T100100_add_accounts_nickname.go` | **authored**: its `Down` drops the index and column, recording each with `Was` |
| `20261101T100200_add_accounts_tier.go` | **nothing**: it reads the table to decide its default, and declares itself `Irreversible` |

## Run it

You need [migr](../../README.md#installing), Go 1.24 or later, and Docker for the database.

```console
$ docker compose up -d --wait
$ migr plan --safety --to 20261101T100100
```

```text
  1  20261101T100000_create_accounts.go
       1  create_table  native  lock_free
          CREATE TABLE "accounts" ( ... );

  2  20261101T100100_add_accounts_nickname.go
       ‖  runs in 2 parts, each committing on its own, split around the statements marked [no transaction]
       1  add_column  native  brief_exclusive
          ALTER TABLE "accounts" ADD COLUMN "nickname" varchar(64);
       2  add_index  native  lock_free (declared long_exclusive, rewritten)
          ↳ rewritten (safety.rewrites.concurrent_index): ... built CONCURRENTLY, outside any transaction, so writes go on while it builds
          [no transaction]
          CREATE INDEX CONCURRENTLY "idx_accounts_nickname" ON "accounts" ("nickname");
```

Every operation shows what it costs a live database. The index is declared `long_exclusive`, and
PostgreSQL builds it `CONCURRENTLY` instead, outside any transaction, so writes carry on.

**Why `--to`:** `add_accounts_tier` reads `accounts` to decide its default, and a read runs against
the database as it is now. On an empty database that table doesn't exist yet, so a plan reaching
that migration fails. A plan stops before it; `migr up` runs it once the table is there.

```console
$ migr up
up — database "default", batch 1

  1  20261101T100000_create_accounts.go        1 statement  575µs
  3  20261101T100100_add_accounts_nickname.go  2 statements, 1 operation rewritten  1.459ms
  4  20261101T100200_add_accounts_tier.go      1 statement  345µs

applied 3 migrations in 2.379ms
$ migr status
$ migr down
migr: 20261101T100200_add_accounts_tier.go: this migration declares that it cannot be undone: the tiers written after it ran are not recorded anywhere a rollback could restore them from
$ docker compose down -v
```

The rollback halts before its first statement, with the reason the migration gave. Nothing newer
than an irreversible migration can be rolled back past it, so to start over, drop the database.

## Tests

```console
$ go test ./...
```

`migrationtest/` uses the SDK's test helpers, from `github.com/emrebdr/migr/sdk/go/migrtest`:
- `migrtest.Up` and `migrtest.Down` capture what each half emits;
- `migrtest.Stage` answers `add_accounts_tier`'s read, so the test chooses the branch: with accounts,
  the column gets a default, and on an empty table it doesn't;
- `migrtest.Reversal` reports how each migration is undone;
- `migrtest.New` runs the real `migr` binary on a throwaway SQLite file: it validates, applies all
  three, and checks the rollback is refused.

They need `migr` on `PATH` (or `MIGR_BIN`), and no database. The tests live in a package of their
own because the migrations directory accepts only migrations.

## CI

[`.github/workflows/migrations.yml`](.github/workflows/migrations.yml) runs on every pull request,
against a PostgreSQL 17 service container:
- `go test ./...`;
- `migr validate --require-safe`, which compiles every migration against the server and fails on an
  operation above `safety.max_lock_impact` (`brief_exclusive`);
- `migr up`.

Copy it into your own repository. It installs migr from npm as `@migr/cli`, so it needs a published
release.

## Using this as a starting point

This project links the Go SDK from this repository, since it isn't published yet. In your own copy,
delete the `replace` line in `go.mod` and run `go get github.com/emrebdr/migr/sdk/go`.
