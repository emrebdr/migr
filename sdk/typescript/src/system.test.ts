import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import type { TestContext } from "node:test";

import { systemOf } from "./system.ts";

// Node's rule, read here because Node will not answer it for TypeScript.
//
// A load hook is handed format null for every .ts file outside a "type": "module" package, and
// calling nextLoad to find out throws on the enum esbuild is there to compile. So this is the one
// place the rule lives, and both the hook and the loader read the same answer from it.

/** written lays out one temporary tree of files and returns where it starts. */
function written(t: TestContext, files: Record<string, string>): string {
  const at = mkdtempSync(join(tmpdir(), "migr-system-"));

  t.after(() => rmSync(at, { recursive: true, force: true }));

  for (const [path, contents] of Object.entries(files)) {
    const full = join(at, path);

    mkdirSync(dirname(full), { recursive: true });
    writeFileSync(full, contents);
  }

  return at;
}

test("an extension that states its own system is believed over the package around it", (t) => {
  const at = written(t, {
    "package.json": '{"type":"commonjs"}',
    "a.mjs": "",
    "a.mts": "",
    "a.cjs": "",
    "a.cts": "",
  });

  assert.equal(systemOf(join(at, "a.mjs")), "module");
  assert.equal(systemOf(join(at, "a.mts")), "module");
  assert.equal(systemOf(join(at, "a.cjs")), "commonjs");
  assert.equal(systemOf(join(at, "a.cts")), "commonjs");
});

test("a .ts or a .js takes the system the nearest package.json declares", (t) => {
  const at = written(t, {
    "package.json": '{"type":"module"}',
    "a.ts": "",
    "a.js": "",
    "tools/package.json": '{"name":"tools"}',
    "tools/b.ts": "",
    "tools/b.js": "",
  });

  assert.equal(systemOf(join(at, "a.ts")), "module");
  assert.equal(systemOf(join(at, "a.js")), "module");

  assert.equal(systemOf(join(at, "tools", "b.ts")), "commonjs");
  assert.equal(systemOf(join(at, "tools", "b.js")), "commonjs");
});

test("a package.json declaring no type at all is CommonJS, which is Node's own default", (t) => {
  const at = written(t, { "package.json": "{}", "a.ts": "" });

  assert.equal(systemOf(join(at, "a.ts")), "commonjs");
});

test("a file with no package.json above it at all is CommonJS, which is Node's own default", () => {
  assert.equal(systemOf(join("/", "migr-no-such-project-here", "a.ts")), "commonjs");
});

test("a package.json that is not JSON is refused naming it, rather than assumed to be either", (t) => {
  const at = written(t, { "package.json": "{ not json", "a.ts": "" });

  assert.throws(() => systemOf(join(at, "a.ts")), {
    message: new RegExp(`package\\.json is not JSON`),
  });
});
