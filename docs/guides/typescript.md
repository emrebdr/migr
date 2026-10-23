---
title: TypeScript
---

# Writing migrations in TypeScript

A TypeScript migration is a module that describes the change through a schema builder from
[`@migr/sdk`](https://www.npmjs.com/package/@migr/sdk). migr compiles what it describes for whichever
database it runs against, and derives the down from it unless you write one. The operations it emits
are byte for byte the ones the same migration written in Go would emit.

## Set up

```console
$ npm install @migr/sdk
$ npm install --save-dev @migr/cli       # or install migr any other way
```

```yaml
# migr.yaml, in the project root
version: 1
migrations:
  dir: ./migrations
  language: typescript
databases:
  default:
    driver: postgres
    dsn: ${DATABASE_URL}
```

```console
$ npx migr create create_accounts
created migrations/20261023T120000_create_accounts.ts
```

**How migr runs it:** it starts Node 24 or later, which loads each migration and compiles it with
esbuild on the way in. `enum` and other non-erasable syntax work, but nothing is typechecked. Run
`tsc` for that. A migration can be `.ts`, `.js` or `.mjs`, in either module format.

## Describing a change

```ts
import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s
      .createTable("accounts")
      .column("id", t.int64())
      .column("email", t.string(255))
      .column("created_at", t.timestamptz()).default({ function: "current_timestamp" })
      .primaryKey("id")
      .apply();
  },
});
```

- **`s.createTable(name)`** builds a new table, with its primary key stated inline.
- **`s.table(name)`** changes one that exists, and **`s.view(name)`** a view.

Two conventions are built into the API:
- **a column is `NOT NULL` unless the chain says `.null()`;**
- **nothing is emitted until `.apply()`.** A chain built and never applied fails the migration, and
  so does an `apply()` that's never awaited.

Types are logical, and each driver maps them to native ones: `t.string(n)`, `t.text()`,
`t.bool()`, `t.int16()`, `t.int32()`, `t.int64()`, `t.float32()`, `t.float64()`,
`t.decimal(p, s)`, `t.uuid()`, `t.json()`, `t.timestamp()`, `t.timestamptz()`, `t.date()`,
`t.time()`, `t.binary()`, `t.enum(...values)` and `t.array(of)`.

## The down

There are three ways a migration can be undone, and it uses exactly one of them:

- **Derived.** Write no `down`, and migr inverts what `up` emitted. `migr validate` warns
  (`missing-down`) because the inverse is computed when the rollback runs.
- **Authored.** Write a `down`. A drop records what it removes with `.was(...)`, so the drop can
  itself be reversed:

  ```ts
  export default defineMigration({
    async up(s) {
      await s
        .table("accounts")
        .addColumn("nickname", t.string(64)).null()
        .addIndex("idx_accounts_nickname", "nickname")
        .apply();
    },
    async down(s) {
      await s
        .table("accounts")
        .dropIndex("idx_accounts_nickname").was({ columns: ["nickname"] })
        .dropColumn("nickname").was({ name: "nickname", type: t.string(64), nullable: true })
        .apply();
    },
  });
  ```

- **Declared irreversible**, with `irreversible: "the reason"` in place of a `down`. A rollback
  halts before its first statement and prints the reason. Writing both doesn't compile.

## Asking the database

```ts
export default defineMigration({
  async up(s) {
    const { rows } = await s.query("SELECT count(*) FROM accounts");

    const tier = s.table("accounts").addColumn("tier", t.string(16));
    if (rows[0]?.[0] !== "0") {
      tier.default({ literal: "free" });
    }

    await tier.apply();
  },
  irreversible: "the tiers written after it ran are not recorded anywhere a rollback could restore them from",
});
```

- **Every value is a string or `null`**, never a number: a 64-bit integer doesn't survive a JSON
  number in JavaScript.
- **A read runs against the database as it is now.** So `migr plan` over a queue where an earlier
  pending migration creates the table fails on the read. Apply up to that point first
  (`migr up --to <version>`).
- **The placeholder is the engine's own:** `$1` on PostgreSQL, `?` on MySQL and SQLite.
  `(await s.info()).driver` says which.

`s.exec(sql)` sends a statement the builder can't express. It runs outside the migration's
transaction, `migr plan` records it instead of running it, and a migration that calls it must write
its down.

## Data migrations and phased changes

`.backfill(...)`, `.copyInto(...)` and `.transform(...)` write rows, in chunks when the migration
holds nothing else. `replaceColumn` changes a column through four migrations, each exporting one
phase:

```ts
import { replaceColumn, t } from "@migr/sdk";

export default replaceColumn("users", "email", t.string(255)).with(t.string(320)).expand();
```

The phases are `expand`, `backfill`, `swap` and `contract`, applied a deploy apart. The
[zero-downtime guide](../zero-downtime.md#_6-changing-a-column-across-deploys) explains the sequence
and `--release`.

## Large tables

`online: SHADOW_TABLE` performs the migration through a shadow copy of its table, which you cut over
with `migr cutover` once it's in sync:

```ts
import { columns, defineMigration, SHADOW_TABLE } from "@migr/sdk";

export default defineMigration({
  online: SHADOW_TABLE,
  async up(s) {
    await s.table("people").addIndex("idx_people_region", "region").apply();
  },
  async down(s) {
    await s.table("people").dropIndex("idx_people_region").was(columns("region")).apply();
  },
});
```

## Testing

`@migr/sdk/testing` has three helpers:
- **`capture`** reports what a half emits;
- **`reversal`** reports how a migration is undone;
- **`project`** builds a throwaway project and drives the real `migr` binary against it.

**Tests don't go in the migrations directory**, which accepts only migrations:

```ts
// test/migrations.test.ts, run with `node --test test/migrations.test.ts`
import assert from "node:assert/strict";
import { test } from "node:test";

import { t } from "@migr/sdk";
import { answer, capture, reversal } from "@migr/sdk/testing";

import addNickname from "../migrations/20261023T120100_add_accounts_nickname.ts";
import addTier from "../migrations/20261023T120200_add_accounts_tier.ts";

const accounts = { reads: { "SELECT count(*) FROM accounts": answer(["count"], ["3"]) } };

test("add_accounts_nickname adds a column and its index", async () => {
  assert.deepEqual((await capture(addNickname)).kinds(), ["add_column", "add_index"]);
});

test("add_accounts_tier defaults the column where accounts already exist", async () => {
  (await capture(addTier, accounts)).is({
    op: "add_column",
    table: "accounts",
    column: { name: "tier", type: t.string(16), nullable: false, default: { literal: "free" } },
  });
});

test("add_accounts_tier cannot be undone", async () => {
  assert.equal((await reversal(addTier, accounts)).undo, "declared");
});
```

A read the stage doesn't answer is refused rather than answered empty, so a test never takes a branch
by accident.

The package's [README](../../sdk/typescript/README.md) is the full reference: every builder call,
both module formats, how a migration is loaded, and what each helper returns.

## In a pipeline

`ghcr.io/emrebdr/migr:<version>-node` runs a TypeScript project with nothing else installed. The
project's own `node_modules`, with `@migr/sdk` in it, comes along in the mount. The
[zero-downtime guide's CI job](../zero-downtime.md#a-github-actions-job) uses it.

## Example project

[`examples/ts-mysql`](../../examples/ts-mysql/README.md) is a TypeScript project on MySQL with an
enum, an authored down, a chunked data migration, and tests using `@migr/sdk/testing`.
