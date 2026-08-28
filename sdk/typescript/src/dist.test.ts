import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { PROTOCOL_VERSION, VERSION } from "./version.ts";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");
const require = createRequire(import.meta.url);

/** Reads this package's manifest, whose exports map is what a consumer actually resolves. */
function manifest(): Record<string, never> {
  return JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
}

/** Reads one file the manifest points at, failing with the path rather than with ENOENT alone. */
function shipped(relative: string): string {
  const path = join(root, relative);

  try {
    return readFileSync(path, "utf8");
  } catch {
    assert.fail(`${relative} is named by package.json but was not built`);
  }
}

test("the ESM build loads and reports what the sources do", async () => {
  const loaded = await import(join(root, "dist", "esm", "index.js"));

  assert.equal(loaded.PROTOCOL_VERSION, PROTOCOL_VERSION);
  assert.equal(loaded.VERSION, VERSION);
});

test("the CJS build loads and reports what the sources do", () => {
  const loaded = require(join(root, "dist", "cjs", "index.js"));

  assert.equal(loaded.PROTOCOL_VERSION, PROTOCOL_VERSION);
  assert.equal(loaded.VERSION, VERSION);
});

test("both formats are built, and neither is the other", () => {
  const esm = shipped("dist/esm/index.js");
  const cjs = shipped("dist/cjs/index.js");

  assert.ok(esm.includes("export"), "the ESM build carries no export statement");
  assert.ok(cjs.includes("module.exports"), "the CJS build does not assign module.exports");
});

test("each format declares its own flavour, so a plain .d.ts is unambiguous", () => {
  assert.equal(JSON.parse(shipped("dist/esm/package.json")).type, "module");
  assert.equal(JSON.parse(shipped("dist/cjs/package.json")).type, "commonjs");
});

test("type declarations ship beside each format", () => {
  for (const format of ["esm", "cjs"]) {
    const types = shipped(`dist/${format}/index.d.ts`);

    assert.ok(types.trim().length > 0, `dist/${format}/index.d.ts is empty`);
    assert.ok(types.includes("PROTOCOL_VERSION"), `dist/${format}/index.d.ts declares nothing`);
  }
});

test("every file the exports map names was built", () => {
  const exported = manifest()["exports"] as unknown as Record<string, Record<string, unknown>>;
  const entry = exported["."] as Record<string, Record<string, string>>;

  for (const condition of ["import", "require"]) {
    const resolved = entry[condition];

    assert.ok(resolved, `package.json exports no "${condition}" condition`);

    for (const named of Object.values(resolved)) {
      shipped(named);
    }
  }
});

test("the declarations exclude the tests, which are not part of what ships", () => {
  assert.throws(
    () => readFileSync(join(root, "dist", "esm", "version.test.d.ts"), "utf8"),
    "a test's declarations were published",
  );
});
