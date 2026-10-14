import assert from "node:assert/strict";
import { test } from "node:test";

import { call, literal } from "./column.ts";
import { columns } from "./builder.ts";
import type { Kind } from "./kind.ts";
import { kinds } from "./kind.ts";
import type { Operation } from "./operation.ts";
import { encode } from "./operation.ts";
import type { Host, Schema, Window } from "./schema.ts";
import { newSchema } from "./schema.ts";
import { t } from "./type.ts";

// The done-when of the Go builder this one mirrors, stated mechanically: a chain and the operation
// written out by hand emit the same bytes. Record<Kind, Built> is what makes the coverage total --
// a twenty-first operation fails to compile here on the day it is added.

/** Built is one operation expressed twice: as a builder chain, and constructed by hand. */
interface Built {
  /** The chain a migration would write, ending in the apply that emits it. */
  chain: (s: Schema) => Promise<void>;
  /** The same operation written out, which Schema.apply takes directly. */
  want: Operation;
}

/** opened returns a window over a host that keeps the documents it was sent. */
function opened(emitted: unknown[]): Window {
  const host: Host = {
    call: async <R>(_method: string, params: unknown): Promise<R> => {
      const { operations } = params as { operations: unknown[] };

      emitted.push(...operations);

      return { recorded: operations.length } as R;
    },
    notify: () => undefined,
  };

  return newSchema(
    {
      migration: "20260727T153000",
      direction: "up",
      mode: "apply",
      signal: new AbortController().signal,
    },
    host,
  );
}

/** sent renders one document the host was handed, which is the bytes the operations array carries. */
function sent(document: unknown): string {
  return JSON.stringify(document);
}

const chains: Record<Kind, Built> = {
  create_table: {
    chain: (s) =>
      s
        .createTable("users")
        .column("id", t.uuid())
        .column("email", t.string(255))
        .column("created_at", t.timestamptz())
        .null()
        .default(call("current_timestamp"))
        .primaryKey("id")
        .apply(),
    want: {
      op: "create_table",
      table: "users",
      columns: [
        { name: "id", type: t.uuid() },
        { name: "email", type: t.string(255) },
        {
          name: "created_at",
          type: t.timestamptz(),
          nullable: true,
          default: call("current_timestamp"),
        },
      ],
      primary_key: ["id"],
    },
  },
  drop_table: {
    chain: (s) =>
      s
        .table("users")
        .drop()
        .was({ columns: [{ name: "id", type: t.uuid() }], primary_key: ["id"] })
        .apply(),
    want: {
      op: "drop_table",
      table: "users",
      from: { columns: [{ name: "id", type: t.uuid() }], primary_key: ["id"] },
    },
  },
  rename_table: {
    chain: (s) => s.table("users").renameTo("people").apply(),
    want: { op: "rename_table", from: "users", to: "people" },
  },
  add_column: {
    chain: (s) => s.table("users").addColumn("email", t.string(255)).notNull().apply(),
    want: { op: "add_column", table: "users", column: { name: "email", type: t.string(255) } },
  },
  drop_column: {
    chain: (s) =>
      s
        .table("users")
        .dropColumn("email")
        .was({ name: "email", type: t.string(255) })
        .apply(),
    want: {
      op: "drop_column",
      table: "users",
      column: "email",
      from: { name: "email", type: t.string(255) },
    },
  },
  rename_column: {
    chain: (s) => s.table("users").renameColumn("email", "address").apply(),
    want: { op: "rename_column", table: "users", from: "email", to: "address" },
  },
  alter_column: {
    chain: (s) =>
      s.table("users").alterColumn("email").type(t.string(512)).wasType(t.string(255)).apply(),
    want: {
      op: "alter_column",
      table: "users",
      column: "email",
      type: t.string(512),
      from: { type: t.string(255) },
    },
  },
  add_index: {
    chain: (s) => s.table("users").addIndex("idx_users_email", "email").apply(),
    want: { op: "add_index", table: "users", name: "idx_users_email", columns: ["email"] },
  },
  drop_index: {
    chain: (s) => s.table("users").dropIndex("idx_users_email").was(columns("email")).apply(),
    want: {
      op: "drop_index",
      table: "users",
      name: "idx_users_email",
      from: { columns: ["email"] },
    },
  },
  add_foreign_key: {
    chain: (s) =>
      s
        .table("posts")
        .addForeignKey("fk_posts_author", ["author_id"], "users", ["id"])
        .onDelete("cascade")
        .apply(),
    want: {
      op: "add_foreign_key",
      table: "posts",
      name: "fk_posts_author",
      columns: ["author_id"],
      references_table: "users",
      references_columns: ["id"],
      on_delete: "cascade",
    },
  },
  drop_foreign_key: {
    chain: (s) =>
      s
        .table("posts")
        .dropForeignKey("fk_posts_author")
        .was({
          columns: ["author_id"],
          references_table: "users",
          references_columns: ["id"],
          on_delete: "cascade",
        })
        .apply(),
    want: {
      op: "drop_foreign_key",
      table: "posts",
      name: "fk_posts_author",
      from: {
        columns: ["author_id"],
        references_table: "users",
        references_columns: ["id"],
        on_delete: "cascade",
      },
    },
  },
  add_unique: {
    chain: (s) => s.table("users").addUnique("uq_users_email", "email").apply(),
    want: { op: "add_unique", table: "users", name: "uq_users_email", columns: ["email"] },
  },
  drop_unique: {
    chain: (s) => s.table("users").dropUnique("uq_users_email").was(columns("email")).apply(),
    want: {
      op: "drop_unique",
      table: "users",
      name: "uq_users_email",
      from: { columns: ["email"] },
    },
  },
  add_check: {
    chain: (s) => s.table("orders").addCheck("ck_orders_total", "total >= 0").apply(),
    want: {
      op: "add_check",
      table: "orders",
      name: "ck_orders_total",
      expression: "total >= 0",
    },
  },
  drop_check: {
    chain: (s) =>
      s.table("orders").dropCheck("ck_orders_total").was({ expression: "total >= 0" }).apply(),
    want: {
      op: "drop_check",
      table: "orders",
      name: "ck_orders_total",
      from: { expression: "total >= 0" },
    },
  },
  add_primary_key: {
    chain: (s) => s.table("users").addPrimaryKey("pk_users", "id").apply(),
    want: { op: "add_primary_key", table: "users", name: "pk_users", columns: ["id"] },
  },
  drop_primary_key: {
    chain: (s) => s.table("users").dropPrimaryKey("pk_users").was(columns("id")).apply(),
    want: {
      op: "drop_primary_key",
      table: "users",
      name: "pk_users",
      from: { columns: ["id"] },
    },
  },
  create_view: {
    chain: (s) => s.view("active_users").create("SELECT id FROM users WHERE active").apply(),
    want: {
      op: "create_view",
      view: "active_users",
      query: "SELECT id FROM users WHERE active",
    },
  },
  drop_view: {
    chain: (s) => s.view("active_users").drop().was("SELECT id FROM users WHERE active").apply(),
    want: {
      op: "drop_view",
      view: "active_users",
      from: { query: "SELECT id FROM users WHERE active" },
    },
  },
  raw: {
    chain: (s) => s.raw("postgres", "CREATE EXTENSION pgcrypto").apply(),
    want: { op: "raw", driver: "postgres", sql: "CREATE EXTENSION pgcrypto" },
  },
  data_copy: {
    chain: (s) =>
      s
        .table("users")
        .copyInto("users_new", { from: "id", to: "id" }, { from: "name", to: "full_name" })
        .apply(),
    want: {
      op: "data_copy",
      from: "users",
      to: "users_new",
      columns: [
        { from: "id", to: "id" },
        { from: "name", to: "full_name" },
      ],
    },
  },
  data_backfill: {
    chain: (s) => s.table("users").backfill("plan", t.string(16), literal("free")).apply(),
    want: {
      op: "data_backfill",
      table: "users",
      column: "plan",
      type: t.string(16),
      value: { literal: "free" },
    },
  },
  data_transform: {
    chain: (s) =>
      s.table("users").transform("email_key", t.string(255), "email", "lower").apply(),
    want: {
      op: "data_transform",
      table: "users",
      column: "email_key",
      type: t.string(255),
      from: "email",
      function: "lower",
    },
  },
};

/** emit runs one chain and returns the documents the window sent. */
async function emit(built: Built): Promise<unknown[]> {
  const emitted: unknown[] = [];

  await built.chain(opened(emitted));

  return emitted;
}

test("the builder covers the v1 operation set and nothing beyond it", () => {
  assert.deepEqual(Object.keys(chains).sort(), kinds().sort());
});

test("every operation is emitted identically by a chain and by a hand-written literal", async () => {
  for (const kind of kinds()) {
    const built = chains[kind];
    const emitted = await emit(built);
    const [only] = emitted;

    assert.equal(emitted.length, 1, `${kind}: one kind is one operation`);
    assert.ok(only, `${kind}: the chain emitted nothing`);
    assert.equal(
      built.want.op,
      kind,
      `${kind}: the hand-written operation is a ${built.want.op}`,
    );
    assert.equal(sent(only), encode(built.want), `${kind}: the chain and the literal differ`);
  }
});

test("an unqualified column is NOT NULL, in both places a column is declared", async () => {
  const added = await emit(chains["add_column"]);
  const [column] = added;

  assert.ok(column);
  assert.match(sent(column), /"nullable":false/);

  const emitted: unknown[] = [];
  const s = opened(emitted);

  await s.createTable("people").column("email", t.string(255)).apply();

  const [created] = emitted;

  assert.ok(created);
  assert.match(sent(created), /"nullable":false/);
});

test("a new table states its key inline where an existing one states it as an operation", async () => {
  const emitted: unknown[] = [];
  const s = opened(emitted);

  await s.createTable("users").column("id", t.uuid()).primaryKey("id").apply();
  await s.table("people").addPrimaryKey("pk_people", "id").apply();

  assert.deepEqual(
    emitted.map((document) => (document as { op: string }).op),
    ["create_table", "add_primary_key"],
  );

  const [created] = emitted;

  assert.ok(created);
  assert.match(sent(created), /"primary_key":\["id"\]/);
});

test("a rename moves the name every later change in the chain uses", async () => {
  const emitted: unknown[] = [];
  const s = opened(emitted);

  await s.table("users").renameTo("people").addIndex("idx_people_email", "email").apply();

  assert.deepEqual(emitted.map(sent), [
    encode({ op: "rename_table", from: "users", to: "people" }),
    encode({
      op: "add_index",
      table: "people",
      name: "idx_people_email",
      columns: ["email"],
    }),
  ]);
});

test("a change builder promotes its chain, so the modifiers do not end it", async () => {
  const emitted: unknown[] = [];
  const s = opened(emitted);
  const table = s.table("users");

  await table
    .addColumn("email", t.string(255))
    .null()
    .addColumn("nickname", t.text())
    .notNull()
    .apply();

  assert.equal(emitted.length, 2);
  assert.match(sent(emitted[0]), /"nullable":true/);
  assert.match(sent(emitted[1]), /"nullable":false/);
});

test("a view of a chain modifies its own change and not the one before it", async () => {
  const emitted: unknown[] = [];
  const s = opened(emitted);
  const table = s.table("users");

  const first = table.addColumn("email", t.string(255));
  table.addColumn("nickname", t.text());
  first.null();

  await table.apply();

  assert.match(sent(emitted[0]), /"nullable":true/);
  assert.match(sent(emitted[1]), /"nullable":false/);
});

test("a member the document omits when empty is omitted when a literal writes it empty", () => {
  assert.equal(
    encode({ op: "create_view", view: "v", query: "SELECT 1", or_replace: false }),
    encode({ op: "create_view", view: "v", query: "SELECT 1" }),
  );

  assert.equal(
    encode({ op: "add_primary_key", table: "users", name: "", columns: ["id"] }),
    encode({ op: "add_primary_key", table: "users", columns: ["id"] }),
  );

  assert.equal(
    encode({
      op: "create_table",
      table: "t",
      columns: [{ name: "id", type: t.uuid() }],
      primary_key: [],
    }),
    encode({ op: "create_table", table: "t", columns: [{ name: "id", type: t.uuid() }] }),
  );

  assert.equal(
    encode({
      op: "alter_column",
      table: "users",
      column: "email",
      drop_default: false,
      nullable: true,
    }),
    encode({ op: "alter_column", table: "users", column: "email", nullable: true }),
  );
});

test("alter_column's nullable is emitted even when false, since absent means leave it alone", () => {
  assert.match(
    encode({ op: "alter_column", table: "users", column: "email", nullable: false }),
    /"nullable":false/,
  );

  assert.doesNotMatch(
    encode({
      op: "alter_column",
      table: "users",
      column: "email",
      nullable: true,
      from: { nullable: false },
    }),
    /"from":\{\}/,
  );
});
