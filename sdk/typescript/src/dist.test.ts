import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readdirSync, readFileSync, statSync } from "node:fs";
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
  assert.equal(typeof loaded.defineMigration, "function");
});

test("the CJS build loads and reports what the sources do", () => {
  const loaded = require(join(root, "dist", "cjs", "index.js"));

  assert.equal(loaded.PROTOCOL_VERSION, PROTOCOL_VERSION);
  assert.equal(loaded.VERSION, VERSION);
  assert.equal(typeof loaded.defineMigration, "function");
});

test("the contract a migration is written against is what both formats ship", async () => {
  const esm = await import(join(root, "dist", "esm", "index.js"));
  const cjs = require(join(root, "dist", "cjs", "index.js")) as Record<string, unknown>;

  for (const exported of [
    "defineMigration",
    "fromModule",
    "parseName",
    "Registry",
    "newSchema",
    "encode",
    "literal",
    "call",
  ]) {
    assert.equal(typeof esm[exported], "function", `dist/esm ships no ${exported}`);
    assert.equal(typeof cjs[exported], "function", `dist/cjs ships no ${exported}`);
  }

  const written = esm["defineMigration"]({ up() {} });
  const loaded = esm["fromModule"]("20260727T153000_add_users.ts", { default: written });

  assert.equal(loaded.version, "20260727T153000");
});

test("the schema builder is what both formats ship, and it emits what the sources do", async () => {
  const esm = await import(join(root, "dist", "esm", "index.js"));
  const cjs = require(join(root, "dist", "cjs", "index.js")) as Record<string, unknown>;

  for (const module of [esm, cjs]) {
    const { newSchema, t } = module as {
      newSchema: (
        call: unknown,
        host: {
          call: (method: string, params: unknown) => Promise<unknown>;
          notify: () => void;
        },
      ) => {
        createTable: (name: string) => {
          column: (
            name: string,
            type: string,
          ) => {
            primaryKey: (...columns: string[]) => { apply: () => Promise<void> };
          };
        };
      };
      t: { uuid: () => string };
    };

    const emitted: unknown[] = [];
    const schema = newSchema(
      {
        migration: "20260727T153000",
        direction: "up",
        mode: "apply",
        signal: new AbortController().signal,
      },
      {
        call: async (method, params) => {
          assert.equal(method, "host.schema.apply");

          const { operations } = params as { operations: unknown[] };

          emitted.push(...operations);

          return { recorded: operations.length };
        },
        notify: () => undefined,
      },
    );

    await schema.createTable("users").column("id", t.uuid()).primaryKey("id").apply();

    assert.equal(
      JSON.stringify(emitted[0]),
      '{"op":"create_table","table":"users","columns":' +
        '[{"name":"id","type":"uuid","nullable":false}],"primary_key":["id"]}',
    );
  }
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

test("the executable the bin field names was built, and it loads the bundle beside it", () => {
  const named = manifest()["bin"] as unknown as Record<string, string>;
  const path = named["migr-runtime-node"];

  assert.ok(path, "package.json installs no migr-runtime-node");

  const source = shipped(path);

  assert.match(source, /^#!\/usr\/bin\/env node\n/, "the executable opens with no shebang");
  assert.match(
    source,
    /from "\.\/index\.js"/,
    "the executable does not load the bundle beside it",
  );
  assert.ok(
    statSync(join(root, path)).mode & 0o111,
    "the executable is not executable, so npm links a file nothing can run",
  );
});

test("every shipped declaration points at the JavaScript beside it, never at a source", () => {
  let checked = 0;

  for (const format of ["esm", "cjs"]) {
    for (const file of declarationsUnder(join(root, "dist", format))) {
      const declared = readFileSync(file, "utf8");

      assert.doesNotMatch(
        declared,
        /(from\s*|import\s*\(\s*)['"]\.\.?\/[^'"]*\.ts['"]/,
        `${file} imports a .ts source, which a consumer cannot resolve`,
      );

      checked += 1;
    }
  }

  assert.ok(checked > 2, "the walk found almost nothing, so it is not walking the tree");
});

/** declarationsUnder walks one directory tree, since a nested source makes a nested declaration. */
function declarationsUnder(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);

    if (entry.isDirectory()) {
      return declarationsUnder(path);
    }

    return entry.name.endsWith(".d.ts") ? [path] : [];
  });
}

test("the rewrite reaches module specifiers and not the strings beside them", () => {
  const declared = shipped("dist/esm/name.d.ts");

  assert.match(
    declared,
    /readonly \[".ts", ".js", ".mjs"\]/,
    "the extensions this SDK loads were rewritten along with the imports",
  );
});

test("the declarations exclude the tests, which are not part of what ships", () => {
  assert.throws(
    () => readFileSync(join(root, "dist", "esm", "version.test.d.ts"), "utf8"),
    "a test's declarations were published",
  );
});
