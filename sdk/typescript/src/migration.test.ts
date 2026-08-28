import assert from "node:assert/strict";
import { test } from "node:test";

import { authored, check, declared, defineMigration, dependencies } from "./migration.ts";
import type { Schema } from "./schema.ts";
import { newSchema } from "./schema.ts";

// Half of what this file asserts is checked by tsc rather than by node: a @ts-expect-error line
// fails the build when the error it expects stops happening, which is what `npm run lint` runs.
// The type assertions live beside the runtime ones because most of them are the same claim twice --
// load.ts states each rule again for the .js migrations no compiler sees.

/** Builds the window a migration acts through, which M7-T4 replaces with the real one. */
function schema(): Schema {
  return newSchema(
    {
      migration: "20260727T153000",
      direction: "up",
      mode: "apply",
      signal: new AbortController().signal,
    },
    async () => undefined,
  );
}

test("a migration's parameters are inferred with nothing annotated", async () => {
  let seen = "";

  const m = defineMigration({
    async up(s) {
      seen = s.migration;
    },
  });

  await m.up(schema());

  assert.equal(seen, "20260727T153000");
});

test("defineMigration returns the migration it was given, and nothing else", () => {
  const written = { up() {} };

  assert.equal(defineMigration(written), written);
});

test("a migration cannot both author a down and declare it has none", () => {
  // @ts-expect-error a migration authors a down or declares it has none, not both
  const m = defineMigration({
    up() {},
    down() {},
    irreversible: "the rows are gone",
  });

  assert.ok(m);
});

test("a migration must have an up", () => {
  // @ts-expect-error up is the one member a migration must have
  const m = defineMigration({
    down() {},
  });

  assert.ok(m);
});

test("dependencies is a declaration the type will not let be a computation", () => {
  const m = defineMigration({
    up() {},
    // @ts-expect-error ADR 0018: dependencies are declared, never computed
    dependencies() {
      return ["20260727T153000"];
    },
  });

  assert.ok(m);
});

test("up is handed the schema and nothing else", () => {
  const m = defineMigration({
    // @ts-expect-error up takes the window it acts through
    up(version: string) {
      void version;
    },
  });

  assert.ok(m);
});

test("a migration that writes a down is the one whose down is run", () => {
  const down = () => {};
  const m = defineMigration({ up() {}, down });

  assert.equal(authored(m), down);
  assert.equal(declared(m), undefined);
});

test("a migration that declares it has no undo carries the reason a rollback halts with", () => {
  const m = defineMigration({
    up() {},
    irreversible: "the rows are gone; restore from a backup",
  });

  assert.equal(authored(m), undefined);
  assert.equal(declared(m), "the rows are gone; restore from a backup");
});

test("a migration with neither is the one the engine derives an inverse for", () => {
  const m = defineMigration({ up() {} });

  assert.equal(authored(m), undefined);
  assert.equal(declared(m), undefined);
});

test("a migration declaring no dependencies declares an empty list, not nothing", () => {
  assert.deepEqual(dependencies(defineMigration({ up() {} })), []);
  assert.deepEqual(
    dependencies(defineMigration({ up() {}, dependencies: ["20260727T153000"] })),
    ["20260727T153000"],
  );
});

test("a migration with no check of its own passes", async () => {
  await check(defineMigration({ up() {} }));
});

test("a migration that considers itself invalid says so by failing", async () => {
  const m = defineMigration({
    up() {},
    validate() {
      throw new Error("this migration needs the accounts table");
    },
  });

  await assert.rejects(check(m), /needs the accounts table/);
});
