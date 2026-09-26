import assert from "node:assert/strict";
import { test } from "node:test";

import { MigrationError } from "./error.ts";
import { fromModule, Registry } from "./load.ts";
import type { Schema } from "./schema.ts";
import { newSchema } from "./schema.ts";

// Every module here is written the way a .js migration arrives: a plain object, checked by nothing.
// That is the point of the file -- migration.ts states these rules in types, and a migration that
// was never compiled meets only the ones stated here.

/** The filename these modules are read under, which the grammar accepts. */
const FILE = "20260727T153000_add_accounts.ts";

/** Reads a module the loader is expected to refuse, returning what it refused it with. */
function refusal(namespace: unknown, file = FILE): MigrationError {
  try {
    fromModule(file, namespace);
  } catch (thrown) {
    assert.ok(thrown instanceof MigrationError, `refused with ${String(thrown)}`);

    return thrown;
  }

  assert.fail(`${file} was read as a migration`);
}

/** Builds the window a migration acts through, which M7-T4 replaces with the real one. */
function schema(): Schema {
  return newSchema(
    {
      migration: "20260727T153000",
      direction: "up",
      mode: "apply",
      signal: new AbortController().signal,
    },
    {
      call: () => Promise.reject(new Error("this window answers no callback")),
      notify: () => undefined,
    },
  );
}

test("a module default-exporting a migration is read as one", () => {
  const migration = { up() {}, down() {}, dependencies: ["20260726T090000"] };
  const loaded = fromModule(FILE, { default: migration });

  assert.deepEqual(loaded, {
    version: "20260727T153000",
    name: "add_accounts",
    file: FILE,
    migration,
  });
});

test("a module exporting the parts one by one is read as the same migration", () => {
  const up = () => {};
  const down = () => {};
  const loaded = fromModule(FILE, { up, down, dependencies: ["20260726T090000"] });

  assert.equal(loaded.version, "20260727T153000");
  assert.equal(loaded.migration.up, up);
  assert.equal(loaded.migration.down, down);
  assert.deepEqual(loaded.migration.dependencies, ["20260726T090000"]);
});

test("a module using both styles is refused rather than one of them being preferred", () => {
  const refused = refusal({ default: { up() {} }, up() {}, down() {} });

  assert.equal(refused.code, "ambiguous_module");
  assert.match(refused.message, /up and down/);
});

test("a module exporting no migration is refused naming both styles", () => {
  const refused = refusal({ helper() {} });

  assert.equal(refused.code, "no_migration");
  assert.match(refused.message, /default-export one, or export up/);
});

test("a default-exported class is refused with the export that would have worked", () => {
  class AddAccounts {
    up() {}
  }

  const refused = refusal({ default: AddAccounts });

  assert.equal(refused.code, "no_migration");
  assert.match(refused.message, /export default new AddAccounts\(\)/);
});

test("a default-exported function that is not a class is refused as itself", () => {
  const refused = refusal({ default: () => {} });

  assert.equal(refused.code, "no_migration");
  assert.match(refused.message, /a migration is an object with an up/);
});

test("a default export that is not an object at all is refused", () => {
  for (const value of [1, "up", true, null]) {
    assert.equal(refusal({ default: value }).code, "no_migration");
  }
});

test("something that is not a module is refused before its exports are looked for", () => {
  assert.equal(refusal("20260727T153000_add_accounts.ts").code, "no_migration");
  assert.equal(refusal(null).code, "no_migration");
});

test("up is the one member a migration must have, and it must be callable", () => {
  assert.match(refusal({ down() {} }).message, /exports no up/);
  assert.match(refusal({ default: { down() {} } }).message, /exports no up/);
  assert.match(
    refusal({ up: "CREATE TABLE accounts" }).message,
    /up as a string, and up is a function/,
  );
});

test("an optional member present as something uncallable is refused naming it", () => {
  assert.match(refusal({ up() {}, down: "DROP TABLE accounts" }).message, /down as a string/);
  assert.match(refusal({ up() {}, validate: 1 }).message, /validate as a number/);
});

test("dependencies are declared, and a computation of them is not a declaration", () => {
  const computed = refusal({
    up() {},
    dependencies() {
      return ["20260726T090000"];
    },
  });

  assert.equal(computed.code, "bad_member");
  assert.match(computed.message, /never one it computes/);

  assert.equal(refusal({ up() {}, dependencies: [20260726090000] }).code, "bad_member");
  assert.equal(refusal({ up() {}, dependencies: "20260726T090000" }).code, "bad_member");
});

test("a migration that both authors a down and declares it has none is refused", () => {
  const refused = refusal({ up() {}, down() {}, irreversible: "the rows are gone" });

  assert.equal(refused.code, "both_directions");
  assert.match(refused.message, /one or the other/);
});

test("a declaration of irreversibility carries the reason a rollback halts with", () => {
  assert.equal(refusal({ up() {}, irreversible: "" }).code, "no_reason");
  assert.equal(refusal({ up() {}, irreversible: "   " }).code, "no_reason");
  assert.equal(refusal({ up() {}, irreversible: true }).code, "bad_member");

  const loaded = fromModule(FILE, { up() {}, irreversible: "the rows are gone" });

  assert.equal(loaded.migration.irreversible, "the rows are gone");
});

test("a filename the grammar refuses is refused before its exports are read", () => {
  assert.equal(refusal({ up() {} }, "add_accounts.ts").code, "malformed_name");
});

test("a migration written as a class instance keeps the object its methods were written on", async () => {
  class AddAccounts {
    seen = "";

    async up(s: Schema) {
      this.seen = s.migration;
    }
  }

  const instance = new AddAccounts();
  const loaded = fromModule(FILE, { default: instance });

  await loaded.migration.up(schema());

  assert.equal(instance.seen, "20260727T153000");
});

test("a registry returns what it read in version order", () => {
  const registry = new Registry();

  registry.add("20260727T153000_add_accounts.ts", { up() {} });
  registry.add("20260726T090000_add_users.ts", { up() {} });

  assert.deepEqual(
    registry.all().map((entry) => entry.version),
    ["20260726T090000", "20260727T153000"],
  );

  assert.equal(registry.lookup("20260726T090000")?.name, "add_users");
  assert.equal(registry.lookup("20260101T000000"), undefined);
});

test("two files cannot claim one version, and the refusal names both", () => {
  const registry = new Registry();

  registry.add("20260727T153000_add_accounts.ts", { up() {} });
  registry.add("20260727T153000_add_users.js", { up() {} });

  assert.throws(() => registry.all(), {
    message: /add_accounts\.ts and 20260727T153000_add_users\.js both register/,
  });
});

test("a registry reports every problem it found rather than the first", () => {
  const registry = new Registry();

  registry.add("add_accounts.ts", { up() {} });
  registry.add("20260727T153000_add_users.ts", { down() {} });
  registry.add("20260728T153000_add_teams.ts", { up() {} });

  try {
    registry.all();
    assert.fail("a registry with two unreadable modules reported neither");
  } catch (thrown) {
    assert.ok(thrown instanceof AggregateError);
    assert.equal(thrown.errors.length, 2);
    assert.match(thrown.message, /is not <YYYYMMDDThhmmss>/);
    assert.match(thrown.message, /exports no up/);
  }
});

test("one problem is reported as itself rather than wrapped in a list of one", () => {
  const registry = new Registry();

  registry.add("20260727T153000_add_users.ts", { down() {} });

  assert.throws(() => registry.all(), MigrationError);
});
