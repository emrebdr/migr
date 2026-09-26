import assert from "node:assert/strict";
import { test } from "node:test";

import { OperationError } from "./error.ts";
import type { Host, Window } from "./schema.ts";
import { newSchema } from "./schema.ts";
import { t } from "./type.ts";

// The window's two holes, where Go has one.
//
// A chain built and never applied emits nothing, and a migration that reports success having
// changed nothing is the failure docs/ir.md §3.4 names -- so the window remembers what it handed
// out. TypeScript adds the second: an apply that was sent and never awaited carries its failure
// into a promise nobody reads, so the window remembers those too.

/** opened returns a window over a sink, and the operations that sink was sent. */
function opened(refuse?: () => never): {
  schema: Window;
  emitted: unknown[];
} {
  const emitted: unknown[] = [];
  const host: Host = {
    call: async <R>(_method: string, params: unknown): Promise<R> => {
      if (refuse !== undefined) {
        refuse();
      }

      const { operations } = params as { operations: unknown[] };

      emitted.push(...operations);

      return { recorded: operations.length } as R;
    },
    notify: () => undefined,
  };

  const schema = newSchema(
    {
      migration: "20260727T153000",
      direction: "up",
      mode: "apply",
      signal: new AbortController().signal,
    },
    host,
  );

  return { schema, emitted };
}

test("the window reports the call it was opened for and nothing it was not", () => {
  const { schema } = opened();

  assert.equal(schema.migration, "20260727T153000");
  assert.equal(schema.direction, "up");
  assert.equal(schema.mode, "apply");
  assert.equal(schema.planning, false);
});

test("a chain built and never applied fails the migration, naming what was left", async () => {
  const { schema, emitted } = opened();

  schema.table("users").addColumn("email", t.string(255));
  schema.view("active_users").create("SELECT 1");

  await assert.rejects(schema.seal(), /changes were built and never applied/);
  assert.deepEqual(emitted, []);
});

test("the report counts what each chain was holding and names the object", async () => {
  const { schema } = opened();

  schema.table("users").addIndex("idx_a", "a").addIndex("idx_b", "b");

  await assert.rejects(schema.seal(), /2 to table "users"/);
});

test("a chain nothing was added to is not a false alarm", async () => {
  const { schema } = opened();

  schema.table("users");
  schema.view("active_users");

  assert.equal(await schema.seal(), 0);
});

test("an apply that was never awaited surfaces its failure when the call ends", async () => {
  const { schema } = opened(() => {
    throw new Error("the host refused it");
  });

  void schema.table("users").addIndex("idx_users_email", "email").apply();

  await assert.rejects(schema.seal(), /the host refused it/);
});

test("an apply that was never awaited still emits, which is why only its failure is held", async () => {
  const { schema, emitted } = opened();

  void schema.table("users").addIndex("idx_users_email", "email").apply();

  assert.equal(await schema.seal(), 1);
  assert.equal(emitted.length, 1);
});

test("applying twice does not emit twice, because apply empties the chain", async () => {
  const { schema, emitted } = opened();
  const table = schema.table("users");

  await table.addIndex("idx_users_email", "email").apply();
  await table.apply();

  assert.equal(emitted.length, 1);
  assert.equal(await schema.seal(), 1);
});

test("an operation the engine would refuse fails the apply, naming which one it is", async () => {
  const { schema, emitted } = opened();

  await assert.rejects(
    schema.apply(
      { op: "raw", driver: "postgres", sql: "SELECT 1" },
      { op: "add_index", table: "users", name: "", columns: ["email"] },
    ),
    /invalid operation 2 \(add_index\): name is required/,
  );

  assert.deepEqual(emitted, [], "an emit that fails its check sends nothing at all");
});

test("nothing is emitted for an empty apply, which is not a failure either", async () => {
  const { schema, emitted } = opened();

  await schema.apply();

  assert.deepEqual(emitted, []);
  assert.equal(await schema.seal(), 0);
});

test("a callback made after the call has ended is refused", async () => {
  const { schema } = opened();

  await schema.seal();

  await assert.rejects(
    schema.apply({ op: "raw", driver: "postgres", sql: "SELECT 1" }),
    OperationError,
  );
});

test("the count the window reports is what it emitted, which the result carries", async () => {
  const { schema } = opened();

  await schema.apply(
    { op: "raw", driver: "postgres", sql: "SELECT 1" },
    { op: "raw", driver: "postgres", sql: "SELECT 2" },
  );
  await schema.table("users").addIndex("idx_users_email", "email").apply();

  assert.equal(await schema.seal(), 3);
});
