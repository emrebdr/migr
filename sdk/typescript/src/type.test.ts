import assert from "node:assert/strict";
import { test } from "node:test";

import { OperationError } from "./error.ts";
import { t, TYPE_KINDS } from "./type.ts";

// A logical type is its own canonical spelling (docs/ir.md §5.1), so these assertions are the
// spelling and nothing else. The compile-time half is checked by tsc rather than by node: an
// expect-error directive fails the build when the error it expects stops happening.
//
// Every compile-time rule keeps its runtime check, because a computed argument is invisible to the
// type system. `width(0)` below is how a test reaches the runtime rule the literal cannot.

/** width hides a number from the type system, as a migration computing one would. */
function width(n: number): number {
  return n;
}

test("each of the eighteen types spells itself the one way the document permits", () => {
  assert.deepEqual(
    [
      t.text(),
      t.bool(),
      t.int16(),
      t.int32(),
      t.int64(),
      t.float32(),
      t.float64(),
      t.uuid(),
      t.json(),
      t.timestamp(),
      t.timestamptz(),
      t.date(),
      t.time(),
      t.binary(),
      t.string(255),
      t.decimal(10, 2),
      t.enum("draft", "live"),
      t.array(t.text()),
    ],
    [
      "text",
      "bool",
      "int16",
      "int32",
      "int64",
      "float32",
      "float64",
      "uuid",
      "json",
      "timestamp",
      "timestamptz",
      "date",
      "time",
      "binary",
      "string(255)",
      "decimal(10,2)",
      "enum(draft,live)",
      "array(text)",
    ],
  );
});

test("t offers exactly the types the document names", () => {
  assert.deepEqual(Object.keys(t).sort(), [...TYPE_KINDS].sort());
});

test("a parameterized type prints tight, since a space would be a different document", () => {
  assert.equal(t.decimal(10, 2), "decimal(10,2)");
  assert.equal(t.enum("a", "b", "c"), "enum(a,b,c)");
});

test("an array's element is itself a full type, and the nesting follows from the grammar", () => {
  assert.equal(t.array(t.string(255)), "array(string(255))");
  assert.equal(t.array(t.enum("draft", "live")), "array(enum(draft,live))");
  assert.equal(t.array(t.array(t.int32())), "array(array(int32))");
});

test("enum order is part of the type, since PostgreSQL compares labels by declaration order", () => {
  assert.notEqual(t.enum("a", "b"), t.enum("b", "a"));
});

test("a computed length is still held to the rule the literal is held to", () => {
  assert.throws(() => t.string(width(0)), OperationError);
  assert.throws(() => t.string(width(0)), /at least 1/);
  assert.throws(() => t.string(width(-3)), /at least 1/);
  assert.throws(() => t.string(width(1.5)), /whole number/);
});

test("a decimal's scale may not exceed its precision, which is checked where it is written", () => {
  assert.throws(() => t.decimal(2, 5), /scale 5 exceeds precision 2/);
  assert.throws(() => t.decimal(width(0), 0), /precision must be at least 1/);
  assert.throws(() => t.decimal(10, width(-1)), /must not be negative/);
  assert.equal(t.decimal(10, 10), "decimal(10,10)");
});

test("an enum label is letters, digits, underscore and hyphen, and nothing else", () => {
  assert.throws(() => t.enum("draft state"), /other than letters/);
  assert.throws(() => t.enum("draft,live"), /other than letters/);
  assert.equal(t.enum("in_review", "not-live", "v2"), "enum(in_review,not-live,v2)");
});

test("array nesting is bounded so corrupt input errors rather than recursing away", () => {
  let deep = t.int32();

  for (let at = 0; at < 32; at += 1) {
    deep = t.array(deep);
  }

  assert.equal(deep.startsWith("array(array("), true);
  assert.throws(() => t.array(deep), /exceeds the limit of 32/);
});

test("a length the type system can read is refused before anything runs", () => {
  // @ts-expect-error a string length is a whole number of at least 1
  assert.throws(() => t.string(0));
  // @ts-expect-error a string length is a whole number of at least 1
  assert.throws(() => t.string(-3));
  // @ts-expect-error a string length is a whole number of at least 1
  assert.throws(() => t.string(1.5));
});

test("a decimal's parameters the type system can read are refused before anything runs", () => {
  // @ts-expect-error a decimal precision is a whole number of at least 1
  assert.throws(() => t.decimal(0, 0));
  // @ts-expect-error a decimal scale is a whole number of at least 0
  assert.throws(() => t.decimal(10, -1));
  // @ts-expect-error a decimal scale is a whole number of at least 0
  assert.throws(() => t.decimal(10, 1.5));
});

test("an enum with no labels, or with one twice, is refused before anything runs", () => {
  // @ts-expect-error an enum has at least one label
  assert.throws(() => t.enum());
  // @ts-expect-error enum labels must be distinct
  assert.throws(() => t.enum("draft", "draft"));
});
