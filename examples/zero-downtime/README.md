# Zero-downtime column change: expand and contract

`users.email varchar(255)` becomes `users.email_address varchar(320)` across five deploys, while an
app keeps inserting and reading users the whole time. [`walkthrough.sh`](walkthrough.sh) runs every
deploy against PostgreSQL 17 and fails if any app query fails.

## The change

One declaration, restated in each phase file, each registering one phase:

```go
migr.Register(migr.ReplaceColumn("users", "email", migr.String(255)).
	With(migr.String(320)).RenameTo("email_address").Expand())
```

| Deploy | Migration | The app |
|---|---|---|
| 0 | `create_users`: the table as it was before the change | **v0** writes and reads `email` |
| 1 | **expand**: adds `email_address`, nullable | **v1** writes both, reads `email` |
| 2 | **backfill**: copies `email` into `email_address`, in chunks | **v2** writes both, reads `email` |
| 3 | **swap**: `email` becomes nullable, `email_address` NOT NULL | **v3** writes both, reads `email_address` |
| 4 | the app first: **v4** writes and reads `email_address` only; then **contract** drops `email` | |

In every deploy but the last, migr runs while the previous app version is still serving, then the
new version rolls out. [`app/main.go`](app/main.go) is that app, one version per `-version`.

**Why the contract comes after the app:** v3 still writes `email`, so dropping it under v3 fails v3's
inserts. The walkthrough's own mutation check confirms it: moving the contract ahead of v4 makes v3
fail 153 of its 463 queries.

**Why `RenameTo`:** keeping the name would mean swap renames `email_next` to `email`, and the app
version serving at that moment would lose the column it writes. Ending under a new name means no
step renames anything.

## Run it

You need [migr](../../README.md#installing), Go 1.24 or later, and Docker.

```console
$ docker compose up -d --wait
$ ./walkthrough.sh
```

Abridged, from a real run:

```text
== the gate CI runs on every pull request
no problems found, 1 warning. every operation was checked against server 17.11.

== deploy 0: the app as it is, serving from users.email
  1  20261101T120000_create_users.go  1 statement  745µs

== deploy 1: expand
  1  20261101T120100_expand_user_email.go  1 statement  838µs

⏸ held: 20261101T120200_backfill_user_email.go waits for a later release, and nothing after it runs
either -- 20261101T120100_expand_user_email.go, its "expand" step, belongs to release "d1", which is
this one; this step runs under a later release, once the app "d1" deploys is serving everywhere.

== roll the app out: v1
app v0: 444 queries, 0 failed

== deploy 2: backfill
  1  20261101T120200_backfill_user_email.go  1 chunk, 375 rows written  7.017ms
...
== deploy 4: the app first, then contract
app v3: 564 queries, 0 failed
  1  20261101T120400_contract_user_email.go  1 statement  680µs

== done
app v4: 304 queries, 0 failed
every app version served through every migration with no failed query
```

To run it again, start from an empty database: `docker compose down -v && docker compose up -d --wait`.

## What to look at

- **`--release` is what keeps two phases out of one deploy.** Each `migr up --release dN` applies the
  next phase and **holds** at the one after it, with exit 0, because that phase's prior step belongs
  to this release. The next deploy runs it. Pass your build id, such as `$GITHUB_SHA`.
- **Deploy 0 uses `--to`.** Every phase file is in the directory from the start, and expand waits on
  no earlier step. In a real project the table predates the change and the phase files arrive later.
- **The backfill runs in chunks**, each its own short transaction. `migr plan --safety` shows it as
  `brief_exclusive (declared table_rewrite, one chunk at a time)`, so the lock gate passes it.
- **The swap's NOT NULL is rewritten** on PostgreSQL 12 and later: a `NOT VALID` check is validated
  without blocking writes, then `SET NOT NULL` uses it.
- **migr warns at the unsafe steps**, in `migr plan` too: the swap, the contract and undoing the
  expand each depend on which app version is serving.
- **Permit what the change destroys:** the backfill is a `data_transform` and the contract a
  `drop_column`, so `migr.yaml` lists both under `validation.allow_destructive`.

The [zero-downtime guide](../../docs/zero-downtime.md) covers the rest: lock timeouts, shadow tables
for large tables, and replica lag.

## CI

[`.github/workflows/migrations.yml`](.github/workflows/migrations.yml) has two jobs:
- on a pull request, `migr validate --require-safe` against an empty PostgreSQL 17, which fails on
  any operation above `brief_exclusive`;
- on a push to `main`, `migr up --release "$GITHUB_SHA"` against the database in
  `secrets.DATABASE_URL`, before the app deploys.

Copy it into your own repository. It installs migr from npm as `@migr/cli`, so it needs a published
release.

## Using this as a starting point

This project links the Go SDK from this repository, since it isn't published yet. In your own copy,
delete the `replace` line in `go.mod` and run `go get github.com/emrebdr/migr/sdk/go`.
