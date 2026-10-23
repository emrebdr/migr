# Mixed languages on SQLite

One migrations directory holding all three languages migr reads, run against a SQLite file, so there
is no server to start:

| File | Language | What it does |
|---|---|---|
| `20261101T090000_create_authors.sql` | SQL | creates `authors`, with an authored `-- migr:down` |
| `20261101T090100_create_books.go` | Go | creates `books` and its foreign key to `authors` |
| `20261101T090200_add_books_isbn.ts` | TypeScript | adds a unique `isbn`, declaring the Go migration as a dependency |

**Each file runs in the language its extension names.** `migrations.language: go` in `migr.yaml` only
decides what `migr create` writes.

## Run it

You need [migr](../../README.md#installing), Go 1.24 or later, and Node 24 or later.

```console
$ npm install
$ migr plan
$ migr up
up — database "default", batch 1

  1  20261101T090000_create_authors.sql  1 statement  152µs
  2  20261101T090100_create_books.go     8 statements  415µs
  3  20261101T090200_add_books_isbn.ts   2 statements  325µs

applied 3 migrations in 893µs
$ migr down --step 3
```

The database is `./app.db`, or whatever `DATABASE_URL` names.

## What to look at

- **`migr plan` marks the foreign key `emulated: table rebuild`.** SQLite's `ALTER TABLE` cannot add
  a foreign key, so the driver copies the table into one carrying the key. That's 8 statements where
  the migration describes 2 operations, and the plan says so before anything runs.
- **The TypeScript migration declares `dependencies: ["20261101T090100"]`**, a version written in
  another language. A dependency names a version, whoever loads it, and one graph is resolved over
  every runtime's migrations. Here it only states what the timestamps already order. A dependency
  pointing backwards in time is where it changes the order.
- **`migr status` prints version order and says so**, because it starts no runtime, and only a
  runtime can report a dependency. `migr plan` prints the order a run uses.
- **`migr validate` warns `missing-down` twice.** The Go and TypeScript migrations author no down, so
  migr derives one by inverting what `up` emits. The `.sql` migration has to author its down, since
  migr can't read raw SQL.
- **`allow_destructive: [raw]`** permits the `.sql` file: every statement in one is a raw operation,
  which migr treats as destructive because it can't tell.

## CI

[`.github/workflows/migrations.yml`](.github/workflows/migrations.yml) validates the migrations and
runs them up and back down against a throwaway SQLite file. Copy it into your own repository. It
installs migr from npm as `@migr/cli`, so it needs a published release.

## Using this as a starting point

This project links the SDKs from this repository, since they aren't published yet. In your own
copy:
- delete the `replace` line in `go.mod`, and run `go get github.com/emrebdr/migr/sdk/go`;
- replace `"file:../../sdk/typescript"` in `package.json` with a version of `@migr/sdk`.
