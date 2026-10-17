import assert from "node:assert/strict";
import { test } from "node:test";

import { encode } from "./operation.ts";
import type { Host } from "./schema.ts";
import { newSchema, operationSince } from "./schema.ts";
import { IR_VERSION } from "./version.ts";

// raw's no_transaction is irVersion 3's one addition (ADR 0084): a field, not a kind, so the
// version an operation needs is read from the operation rather than from its kind alone.

/** window opens a migration's window over a sink, against an engine stating one irVersion. */
function window(irVersion: number): { schema: ReturnType<typeof newSchema>; sent: unknown[] } {
  const sent: unknown[] = [];
  const host: Host = {
    call: async <R>(_method: string, params: unknown): Promise<R> => {
      const { operations } = params as { operations: unknown[] };

      sent.push(...operations);

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
      irVersion,
    },
    host,
  );

  return { schema, sent };
}

test("noTransaction marks the statement just added and no other", async () => {
  const { schema, sent } = window(IR_VERSION);

  await schema
    .raw("postgres", "CREATE TABLE t (c int)")
    .raw("postgres", "CREATE INDEX CONCURRENTLY i ON t (c)")
    .noTransaction()
    .raw("postgres", "ANALYZE t")
    .apply();

  assert.deepEqual(
    sent.map((document) => JSON.stringify(document)),
    [
      '{"op":"raw","driver":"postgres","sql":"CREATE TABLE t (c int)"}',
      '{"op":"raw","driver":"postgres","sql":"CREATE INDEX CONCURRENTLY i ON t (c)","no_transaction":true}',
      '{"op":"raw","driver":"postgres","sql":"ANALYZE t"}',
    ],
  );
});

test("only a raw carrying the flag needs irVersion 3", () => {
  assert.equal(operationSince({ op: "raw", driver: "postgres", sql: "SELECT 1" }), 1);
  assert.equal(
    operationSince({ op: "raw", driver: "postgres", sql: "VACUUM", no_transaction: true }),
    3,
  );
  assert.ok(
    IR_VERSION >= 3,
    "this SDK emits no_transaction, so it must say it emits irVersion 3",
  );
  assert.equal(
    encode({ op: "raw", driver: "postgres", sql: "SELECT 1", no_transaction: false }),
    '{"op":"raw","driver":"postgres","sql":"SELECT 1"}',
  );
});

test("no_transaction against an engine reading irVersion 2 is refused before it is sent", async () => {
  const { schema, sent } = window(2);

  await assert.rejects(
    schema.raw("postgres", "CREATE INDEX CONCURRENTLY i ON t (c)").noTransaction().apply(),
    /raw carrying no_transaction, which needs irVersion 3, and the engine stated 2/,
  );
  assert.deepEqual(sent, []);
});
