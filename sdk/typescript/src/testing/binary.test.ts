import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { ambient, BINARY_VAR, found, NoBinaryError } from "./index.ts";

/** executable writes something this ladder will accept as a binary, and returns its path. */
function executable(name: string): string {
  const path = join(mkdtempSync(join(tmpdir(), "migr-ladder-")), name);

  writeFileSync(path, "#!/bin/sh\nexit 0\n", { mode: 0o700 });

  return path;
}

test("the variable is preferred to whatever is on PATH", () => {
  const named = executable("migr");

  assert.equal(
    found({ named, path: ["/usr/local/bin"] }),
    named,
    "whoever set MIGR_BIN meant it; preferring PATH would silently measure a different binary",
  );
});

test("PATH is the second rung", () => {
  const directory = mkdtempSync(join(tmpdir(), "migr-ladder-"));

  writeFileSync(join(directory, "migr"), "#!/bin/sh\nexit 0\n", { mode: 0o700 });

  assert.equal(found({ named: undefined, path: [directory] }), join(directory, "migr"));
});

test("neither rung is a failure naming both", () => {
  assert.throws(
    () => found({ named: undefined, path: ["/nowhere/at/all"] }),
    (thrown: Error) => {
      assert.ok(thrown instanceof NoBinaryError);

      for (const phrase of [BINARY_VAR, "PATH"]) {
        assert.ok(thrown.message.includes(phrase), `the refusal does not name ${phrase}`);
      }

      return true;
    },
  );
});

test("a variable naming something that is not an executable is a failure and not a fallback", () => {
  const directory = mkdtempSync(join(tmpdir(), "migr-ladder-"));
  const unrunnable = join(directory, "migr");

  writeFileSync(unrunnable, "not a program", { mode: 0o600 });

  for (const [named, says] of [
    ["", /set to nothing/],
    [join(directory, "absent"), /not there/],
    [directory, /not an executable/],
    [unrunnable, /not an executable/],
  ] as [string, RegExp][]) {
    assert.throws(() => found({ named, path: [directory] }), says);
  }
});

test("ambient reads the real environment without starting anything", () => {
  const before = process.env[BINARY_VAR];
  const named = executable("migr");

  process.env[BINARY_VAR] = named;

  try {
    assert.equal(ambient().named, named);
    assert.equal(found(ambient()), named);
  } finally {
    if (before === undefined) {
      delete process.env[BINARY_VAR];
    } else {
      process.env[BINARY_VAR] = before;
    }
  }
});
