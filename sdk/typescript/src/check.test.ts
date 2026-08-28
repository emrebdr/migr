import assert from "node:assert/strict";
import { test } from "node:test";

import { check } from "./check.ts";
import type { Operation } from "./operation.ts";
import { t } from "./type.ts";

// docs/ir.md §9.1, which is structure only: required members present and non-empty, lists non-empty
// and distinct where the operation requires it, paired lists the same length, closed sets honoured,
// either-or members not both set, a create_table's key naming its own columns, and a drop_column's
// recorded name matching the column being dropped.
//
// The host runs all of this again, so none of it is what makes a bad operation safe. It is here so
// that the failure names the call that built it, and so that two SDKs refuse the same operations --
// a rule enforced on one side only is a rule that has already diverged (ADR 0038 C).

/** refusal runs one check that should fail, returning what it failed with. */
function refusal(op: Operation): string {
  try {
    check(op);
  } catch (thrown) {
    return thrown instanceof Error ? thrown.message : String(thrown);
  }

  return assert.fail(`${op.op} was accepted`);
}

test("a required identifier is present and is not blank", () => {
  assert.match(refusal({ op: "raw", driver: "", sql: "SELECT 1" }), /driver is required/);
  assert.match(refusal({ op: "raw", driver: "postgres", sql: " " }), /sql is required/);
  assert.match(refusal({ op: "drop_view", view: "" }), /view is required/);
  assert.match(
    refusal({ op: "add_check", table: "t", name: "c", expression: "" }),
    /expression is required/,
  );
});

test("a list the operation requires is non-empty and names nothing twice", () => {
  assert.match(
    refusal({ op: "add_index", table: "t", name: "i", columns: [] }),
    /columns must list at least one column/,
  );
  assert.match(
    refusal({ op: "add_index", table: "t", name: "i", columns: ["a", ""] }),
    /columns\[1\] is empty/,
  );
  assert.match(
    refusal({ op: "add_unique", table: "t", name: "u", columns: ["a", "a"] }),
    /columns lists "a" more than once/,
  );
});

test("two lists that pair by position are the same width", () => {
  assert.match(
    refusal({
      op: "add_foreign_key",
      table: "posts",
      name: "fk",
      columns: ["a", "b"],
      references_table: "users",
      references_columns: ["id"],
    }),
    /columns has 2 entries but references_columns has 1/,
  );
});

test("a closed set is closed, so a fifth function or a sixth action is refused", () => {
  assert.match(
    refusal({
      op: "add_column",
      table: "t",
      column: { name: "c", type: t.text(), default: { function: "now" as never } },
    }),
    /unknown default function "now"/,
  );

  assert.match(
    refusal({
      op: "add_foreign_key",
      table: "posts",
      name: "fk",
      columns: ["a"],
      references_table: "users",
      references_columns: ["id"],
      on_delete: "delete" as never,
    }),
    /unknown on_delete action "delete"/,
  );
});

test("an either-or pair is not both set, in the change and in what it recorded", () => {
  assert.match(
    refusal({
      op: "add_column",
      table: "t",
      column: { name: "c", type: t.text(), default: { literal: "x", function: "uuid" } },
    }),
    /literal or function, not both/,
  );

  assert.match(
    refusal({ op: "add_column", table: "t", column: { name: "c", type: t.text(), default: {} } }),
    /either literal or function/,
  );

  assert.match(
    refusal({
      op: "alter_column",
      table: "t",
      column: "c",
      default: { literal: "x" },
      drop_default: true,
    }),
    /default and drop_default cannot both be set/,
  );

  assert.match(
    refusal({
      op: "alter_column",
      table: "t",
      column: "c",
      nullable: true,
      from: { default: { literal: "x" }, no_default: true },
    }),
    /from.default and from.no_default cannot both be set/,
  );
});

test("a new table's key names its own columns, and its columns are declared once", () => {
  assert.match(
    refusal({
      op: "create_table",
      table: "users",
      columns: [{ name: "id", type: t.uuid() }],
      primary_key: ["email"],
    }),
    /primary_key\[0\]: "email" is not a column of this table/,
  );

  assert.match(
    refusal({
      op: "create_table",
      table: "users",
      columns: [
        { name: "id", type: t.uuid() },
        { name: "id", type: t.text() },
      ],
    }),
    /column "id" is declared more than once/,
  );

  assert.match(
    refusal({ op: "create_table", table: "users", columns: [] }),
    /columns must list at least one column/,
  );
});

test("a drop_column's recorded column is the column being dropped", () => {
  assert.match(
    refusal({
      op: "drop_column",
      table: "users",
      column: "email",
      from: { name: "nickname", type: t.text() },
    }),
    /from.name "nickname" does not name the column being dropped \("email"\)/,
  );

  check({
    op: "drop_column",
    table: "users",
    column: "email",
    from: { name: "email", type: t.text() },
  });
});

test("a rename names two different things, both of them named", () => {
  assert.match(refusal({ op: "rename_table", from: "users", to: "users" }), /same from and to/);
  assert.match(refusal({ op: "rename_table", from: "", to: "people" }), /from is required/);
  assert.match(
    refusal({ op: "rename_column", table: "t", from: "a", to: "a" }),
    /column rename has the same from and to/,
  );
});

test("an alteration that asks for nothing is refused, since absent means leave it alone", () => {
  assert.match(refusal({ op: "alter_column", table: "t", column: "c" }), /requests no change/);

  check({ op: "alter_column", table: "t", column: "c", drop_default: true });
  check({ op: "alter_column", table: "t", column: "c", nullable: false });
});

test("what an operation recorded is held to the same rules as what it changes", () => {
  assert.match(
    refusal({ op: "drop_table", table: "users", from: { columns: [] } }),
    /from.columns must list at least one column/,
  );

  assert.match(
    refusal({ op: "drop_index", table: "t", name: "i", from: { columns: ["a", "a"] } }),
    /from.columns lists "a" more than once/,
  );

  assert.match(
    refusal({ op: "drop_view", view: "v", from: { query: "" } }),
    /from.query is required/,
  );

  assert.match(
    refusal({
      op: "drop_foreign_key",
      table: "posts",
      name: "fk",
      from: { columns: ["a"], references_table: "", references_columns: ["id"] },
    }),
    /from.references_table is required/,
  );
});

test("a column carries a name and a type, and the failure names the column", () => {
  assert.match(
    refusal({ op: "add_column", table: "t", column: { name: "", type: t.text() } }),
    /column name is required/,
  );

  assert.match(
    refusal({ op: "add_column", table: "t", column: { name: "c", type: "" as never } }),
    /column "c": type is required/,
  );

  assert.match(
    refusal({
      op: "create_table",
      table: "t",
      columns: [
        { name: "id", type: t.uuid() },
        { name: "c", type: "" as never },
      ],
    }),
    /columns\[1\]: column "c": type is required/,
  );
});
