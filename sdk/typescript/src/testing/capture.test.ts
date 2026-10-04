import assert from "node:assert/strict";
import { test } from "node:test";

import { defineMigration, t as types } from "../index.ts";
import type { Migration, Rows } from "../index.ts";

import { answer, capture, captureDown, documents, reversal, UnstagedError } from "./index.ts";

/** THE_TABLE is what every migration here creates or changes. */
const THE_TABLE = "users";

/** THE_READ is what the branching migration asks, which a stage has to answer. */
const THE_READ = "SELECT count(*) FROM users";

/** THE_STATEMENT is what the mixing migration sends itself. */
const THE_STATEMENT = "UPDATE users SET tier = 1";

/** creates is a migration that creates a table and authors the down that drops it. */
const creates = defineMigration({
  up: (s) =>
    s
      .createTable(THE_TABLE)
      .column("id", types.int64())
      .column("email", types.string(255))
      .primaryKey("id")
      .apply(),
  down: (s) =>
    s
      .table(THE_TABLE)
      .drop()
      .was({
        columns: [
          { name: "id", type: types.int64(), nullable: false },
          { name: "email", type: types.string(255), nullable: false },
        ],
        primary_key: ["id"],
      })
      .apply(),
});

/** derives is a migration that authors no down at all, so the engine inverts its up. */
const derives = defineMigration({
  up: (s) => s.table(THE_TABLE).addColumn("tier", types.int32()).null().apply(),
});

/** oneWay is a migration whose author states it cannot be undone. */
const oneWay = defineMigration({
  up: (s) => s.table(THE_TABLE).dropColumn("legacy").apply(),
  irreversible: "the dropped values are not recoverable; restore from a backup",
});

/** branches reads the database and emits a different column for each answer. */
const branches = defineMigration({
  up: async (s) => {
    const rows = await s.query(THE_READ);
    const change = s.table(THE_TABLE).addColumn("tier", types.int32());

    await (rows.rows[0]?.[0] !== "0" ? change.null() : change).apply();
  },
});

/** mixes sends a statement of its own beside the operation it declares, and authors a down. */
const mixes = defineMigration({
  up: async (s) => {
    const outcome = await s.exec(THE_STATEMENT);

    s.log("info", "backfilled", {
      rows: outcome.rowsAffected,
      executed: String(outcome.executed),
    });

    await s.table(THE_TABLE).addIndex("idx_users_tier", "tier").apply();
  },
  down: (s) =>
    s
      .table(THE_TABLE)
      .dropIndex("idx_users_tier")
      .was({ columns: ["tier"] })
      .apply(),
});

/** derivesOverStatement sends a statement of its own and authors no down, which has no inverse. */
const derivesOverStatement = defineMigration({
  up: async (s) => {
    await s.exec(THE_STATEMENT);
    await s.table(THE_TABLE).addIndex("idx_users_tier", "tier").apply();
  },
});

/** asks branches on what the engine is, and on what it makes of an operation. */
const asks = defineMigration({
  up: async (s) => {
    const facts = await s.info();
    const op = { op: "add_index", table: THE_TABLE, name: "idx_users_email", columns: ["email"] };
    const verdict = await s.capability(op as never);

    s.log("debug", "asked", { driver: facts.driver, support: verdict.support });

    await s.apply(op as never);
  },
});

/** forgetful builds a change and returns without applying it, which emits nothing. */
const forgetful = defineMigration({
  up: (s) => {
    s.table(THE_TABLE).addColumn("tier", types.int32());
  },
});

/** swallows reads the database, ignores the refusal, and emits anyway. */
const swallows = defineMigration({
  up: async (s) => {
    await s.query(THE_READ).catch(() => undefined);
    await s.table(THE_TABLE).addColumn("tier", types.int32()).null().apply();
  },
});

/** counted is the one answer a staged read gives, since every case here reads the same statement. */
function counted(rows: string): Record<string, Rows> {
  return { [THE_READ]: answer(["count"], [rows]) };
}

test("a capture holds the documents the engine would have received", async () => {
  const captured = await capture(creates);

  captured.is({
    op: "create_table",
    table: THE_TABLE,
    columns: [
      { name: "id", type: types.int64(), nullable: false },
      { name: "email", type: types.string(255), nullable: false },
    ],
    primary_key: ["id"],
  });

  assert.deepEqual(captured.kinds(), ["create_table"]);
});

test("an authored down is captured the same way", async () => {
  const captured = await captureDown(creates);

  assert.deepEqual(captured.kinds(), ["drop_table"]);
});

test("both modes emit the same operations", async () => {
  const applying = await capture(creates, { mode: "apply" });
  const planning = await capture(creates, { mode: "plan" });

  assert.deepStrictEqual(
    applying.operations.map((d) => JSON.stringify(d)),
    planning.operations.map((d) => JSON.stringify(d)),
    "ADR 0033: the engine records operations in both modes, so a preview that emitted something " +
      "else would be a preview of a run that never happens",
  );
});

test("a statement is held under plan and reported performed under apply", async () => {
  const held = await capture(mixes, { mode: "plan" });
  const performed = await capture(mixes, {
    mode: "apply",
    affected: { [THE_STATEMENT]: "41" },
  });

  assert.equal(held.statements[0]?.sql, THE_STATEMENT);
  assert.equal(performed.statements[0]?.sql, THE_STATEMENT);

  assert.equal(held.logs[0]?.fields["executed"], "false");
  assert.equal(held.logs[0]?.fields["rows"], "0");
  assert.equal(performed.logs[0]?.fields["executed"], "true");
  assert.equal(performed.logs[0]?.fields["rows"], "41");
});

test("the mode is apply unless the stage says otherwise", async () => {
  const captured = await capture(mixes);

  assert.equal(captured.logs[0]?.fields["executed"], "true");
});

test("a staged read is what the migration branches on", async () => {
  const empty = await capture(branches, { reads: counted("0") });
  const loaded = await capture(branches, { reads: counted("12") });

  assert.equal((empty.operations[0] as { column: { nullable: boolean } }).column.nullable, false);
  assert.equal((loaded.operations[0] as { column: { nullable: boolean } }).column.nullable, true);
  assert.equal(loaded.reads[0]?.sql, THE_READ);
});

test("an unstaged read is refused rather than answered", async () => {
  await assert.rejects(capture(branches), (thrown: Error) => {
    assert.equal(thrown.name, "UnstagedError");
    assert.match(thrown.message, /SELECT count\(\*\) FROM users/);

    return true;
  });
});

test("a migration that swallows a refused read still fails the capture", async () => {
  await assert.rejects(capture(swallows), (thrown: Error) => {
    assert.ok(thrown instanceof UnstagedError);

    return true;
  });
});

test("host.info and a capability are refused until they are staged", async () => {
  await assert.rejects(capture(asks), /host\.info/);
  await assert.rejects(
    capture(asks, {
      info: {
        database: "default",
        driver: "postgres",
        server: "17.2",
        transactionalDDL: true,
        mode: "apply",
      },
    }),
    /add_index/,
  );
});

test("a staged engine is what the migration is told about, except the mode", async () => {
  const captured = await capture(asks, {
    mode: "plan",
    info: {
      database: "default",
      driver: "postgres",
      server: "17.2",
      transactionalDDL: true,
      mode: "apply",
    },
    verdicts: { add_index: { support: "native", lock: "lock_free" } },
  });

  assert.equal(captured.logs[0]?.fields["driver"], "postgres");
  assert.equal(captured.logs[0]?.fields["support"], "native");
});

test("a chain built and never applied fails the capture", async () => {
  await assert.rejects(capture(forgetful), /never applied/);
});

test("a down is captured exactly when the migration authors one", async () => {
  const authored = await reversal(creates);

  assert.equal(authored.undo, "authored");
  assert.equal(authored.down.operations.length, 1);

  const derived = await reversal(derives);

  assert.equal(derived.undo, "derived");
  assert.equal(
    derived.down.operations.length,
    0,
    "ADR 0022 keeps every inverse in the engine, so there is nothing to capture here",
  );

  const declared = await reversal(oneWay);

  assert.equal(declared.undo, "declared");
  assert.equal(declared.reason, oneWay.irreversible);
});

test("a migration that authors no down cannot have one captured", () => {
  for (const [m, says] of [
    [oneWay, /not recoverable/],
    [derives, /reversal/],
  ] as [Migration, RegExp][]) {
    assert.throws(() => captureDown(m), says);
  }
});

test("a derived undo over a held statement is reported", async () => {
  await assert.rejects(reversal(derivesOverStatement), (thrown: Error) => {
    for (const phrase of ["authors no down", THE_STATEMENT, "no inverse to derive", "ADR 0036"]) {
      assert.ok(thrown.message.includes(phrase), `the finding does not say ${phrase}`);
    }

    return true;
  });
});

test("the same pattern with an authored down is not reported", async () => {
  const reversed = await reversal(mixes);

  assert.equal(reversed.up.statements.length, 1);
  assert.equal(reversed.down.operations.length, 1);
});

test("is compares the documents and not the shape", async () => {
  const captured = await capture(derives);

  assert.throws(() =>
    captured.is({
      op: "add_column",
      table: THE_TABLE,
      column: { name: "tier", type: types.int64(), nullable: true },
    }),
  );

  assert.deepEqual(
    captured.kinds(),
    documents({
      op: "add_column",
      table: THE_TABLE,
      column: { name: "tier", type: types.int64(), nullable: true },
    }).map((d) => d["op"]),
    "they are the same kind, which is what kinds compares and is does not",
  );
});

test("answer renders every value as the server's own text", () => {
  const rows = answer(["id", "email"], ["1", "a@example.com"], ["2", null]);

  assert.equal(rows.rows.length, 2);
  assert.equal(rows.rows[1]?.[1], null);
});
