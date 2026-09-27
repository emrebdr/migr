import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { setSourceMapsSupport } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import type { TestContext } from "node:test";
import { fileURLToPath } from "node:url";

import { fromDirectory } from "./directory.ts";
import { missing } from "./hook.ts";
import type { Loaded } from "./load.ts";
import { newSchema } from "./schema.ts";
import type { Host } from "./schema.ts";

// The done-when, made mechanical: .ts, .js, and .mjs migrations all load, and TypeScript is
// transpiled with no build step of the author's own.
//
// Every TypeScript fixture here carries an enum, which is not a flourish. Node's own type stripping
// refuses one -- ERR_UNSUPPORTED_TYPESCRIPT_SYNTAX -- so a fixture that used only erasable syntax
// would pass with the transpiler switched off entirely, and the enum's value reaching the emitted IR
// is what proves esbuild compiled rather than merely stripped.
//
// The rest is the directory: flat, dotfiles exempt, and a file the grammar refuses failing loudly
// rather than vanishing. A migration written, committed, and quietly ignored is the mistake this
// strictness is spent on.

const sdk = join(dirname(fileURLToPath(import.meta.url)), "..");

/** project writes one temporary project and returns its migrations directory. */
function project(t: TestContext, type: string, files: Record<string, string>): string {
  const root = mkdtempSync(join(tmpdir(), "migr-directory-"));

  t.after(() => rmSync(root, { recursive: true, force: true }));

  writeFileSync(
    join(root, "package.json"),
    `{"name":"fixture","type":${JSON.stringify(type)}}\n`,
  );
  mkdirSync(join(root, "node_modules", "@migr"), { recursive: true });
  symlinkSync(sdk, join(root, "node_modules", "@migr", "sdk"));
  mkdirSync(join(root, "migrations"), { recursive: true });

  for (const [path, contents] of Object.entries(files)) {
    const full = join(root, path);

    mkdirSync(dirname(full), { recursive: true });
    writeFileSync(full, contents);
  }

  return join(root, "migrations");
}

/** emitted runs one loaded migration's up through a recording host and returns what it emitted. */
async function emitted(entry: Loaded): Promise<unknown[]> {
  const operations: unknown[] = [];
  const host: Host = {
    call: async <R>(method: string, params: unknown): Promise<R> => {
      assert.equal(method, "host.schema.apply");

      const sent = (params as { operations: unknown[] }).operations;

      operations.push(...sent);

      return { recorded: sent.length } as R;
    },
    notify: () => undefined,
  };

  const window = newSchema(
    {
      migration: entry.version,
      direction: "up",
      mode: "apply",
      signal: new AbortController().signal,
    },
    host,
  );

  await entry.migration.up(window);
  await window.seal();

  return operations;
}

/** refused loads a directory expected to hold problems, returning each in the order reported. */
async function refused(dir: string): Promise<string[]> {
  const registry = await fromDirectory(dir);

  try {
    registry.all();
  } catch (thrown) {
    return thrown instanceof AggregateError
      ? (thrown.errors as Error[]).map((problem) => problem.message)
      : [(thrown as Error).message];
  }

  return assert.fail("the directory was read with no problem reported");
}

/** TABLE is the operation every fixture emits, whose width comes from an enum in the TypeScript. */
const TABLE =
  '{"op":"create_table","table":"accounts",' +
  '"columns":[{"name":"email","type":"string(255)","nullable":false}]}';

test("a .ts migration loads, and an enum in it proves esbuild compiled rather than Node stripped", async (t) => {
  const dir = project(t, "module", {
    "migrations/20260727T153000_add_accounts.ts": `import { defineMigration, t } from "@migr/sdk";

enum Width {
  Email = 255,
}

export default defineMigration({
  async up(s) {
    await s.createTable("accounts").column("email", t.string(Width.Email)).apply();
  },
});
`,
  });

  const [entry] = (await fromDirectory(dir)).all();

  assert.ok(entry, "the .ts migration was not loaded");
  assert.equal(entry.version, "20260727T153000");
  assert.equal(entry.file, "20260727T153000_add_accounts.ts");
  assert.equal(JSON.stringify((await emitted(entry))[0]), TABLE);
});

test("an .mjs migration loads, exporting its parts one by one and importing nothing", async (t) => {
  const dir = project(t, "commonjs", {
    "migrations/20260727T153000_add_accounts.mjs": `export async function up(s) {
  await s.createTable("accounts").column("email", "string(255)").apply();
}

export const dependencies = ["20260101T000000"];
`,
  });

  const [entry] = (await fromDirectory(dir)).all();

  assert.ok(entry, "the .mjs migration was not loaded");
  assert.deepEqual(entry.migration.dependencies, ["20260101T000000"]);
  assert.equal(JSON.stringify((await emitted(entry))[0]), TABLE);
});

test("a .js migration loads as an ES module where the project declares one", async (t) => {
  const dir = project(t, "module", {
    "migrations/20260727T153000_add_accounts.js": `export async function up(s) {
  await s.createTable("accounts").column("email", "string(255)").apply();
}
`,
  });

  const [entry] = (await fromDirectory(dir)).all();

  assert.ok(entry, "the ESM .js migration was not loaded");
  assert.equal(JSON.stringify((await emitted(entry))[0]), TABLE);
});

test("a .js migration loads as CommonJS where the project declares that, which most do", async (t) => {
  const dir = project(t, "commonjs", {
    "migrations/20260727T153000_add_accounts.js": `module.exports = {
  up: async (s) => {
    await s.createTable("accounts").column("email", "string(255)").apply();
  },
};
`,
  });

  const [entry] = (await fromDirectory(dir)).all();

  assert.ok(entry, "the CommonJS .js migration was not loaded");
  assert.equal(JSON.stringify((await emitted(entry))[0]), TABLE);
});

test("a CommonJS module's exports are unwrapped, since Node reaches them as both styles at once", async (t) => {
  const dir = project(t, "commonjs", {
    "migrations/20260727T153000_add_accounts.js": `module.exports = { up: async () => {} };
`,
  });

  t.diagnostic(
    "import() of a CommonJS file yields the lexer's named exports AND a default, which",
  );
  t.diagnostic("load.ts refuses as ambiguous_module -- so the loader hands it what it exported.");

  const loaded = await fromDirectory(dir);

  assert.equal(loaded.all().length, 1);
});

test("a .ts migration loads in a CommonJS project too, compiled to the system it belongs to", async (t) => {
  const dir = project(t, "commonjs", {
    "migrations/20260727T153000_add_accounts.ts": `enum Width {
  Email = 255,
}

module.exports = {
  up: async (s) => {
    await s.createTable("accounts").column("email", \`string(\${Width.Email})\`).apply();
  },
};
`,
  });

  t.diagnostic("Node reports no format at all for this file, and asking it throws on the enum.");
  t.diagnostic("So the system is read from the project, esbuild emits CommonJS, and what came");
  t.diagnostic("back through import() is unwrapped.");

  const [entry] = (await fromDirectory(dir)).all();

  assert.ok(entry, "the CommonJS .ts migration was not loaded");
  assert.equal(JSON.stringify((await emitted(entry))[0]), TABLE);
});

test("a migration may import a .mts helper, which Node cannot run either", async (t) => {
  const dir = project(t, "module", {
    "db/schema.mts": `enum Column {
  Email = 255,
}

export const width = Column.Email;
`,
    "migrations/20260727T153000_add_accounts.ts": `import { width } from "../db/schema.mjs";

export async function up(s) {
  await s.createTable("accounts").column("email", \`string(\${width})\`).apply();
}
`,
  });

  const [entry] = (await fromDirectory(dir)).all();

  assert.ok(entry, "the migration importing a .mts helper was not loaded");
  assert.equal(JSON.stringify((await emitted(entry))[0]), TABLE);
});

test("a TypeScript migration may import the project's own TypeScript, spelled as tsc requires", async (t) => {
  const dir = project(t, "module", {
    "db/schema.ts": `export const width = 255;
`,
    "migrations/20260727T153000_add_accounts.ts": `import { width } from "../db/schema.js";

export async function up(s) {
  await s.createTable("accounts").column("email", \`string(\${width})\`).apply();
}
`,
  });

  t.diagnostic(
    "tsc forbids ./x.ts unless allowImportingTsExtensions is on, so ./x.js is the only",
  );
  t.diagnostic("spelling a project can write -- and it is the one Node cannot resolve.");

  const [entry] = (await fromDirectory(dir)).all();

  assert.ok(entry, "the migration importing its project's own TypeScript was not loaded");
  assert.equal(JSON.stringify((await emitted(entry))[0]), TABLE);
});

test("a stack trace from inside a migration names the line its author wrote", async (t) => {
  const dir = project(t, "module", {
    "migrations/20260727T153000_add_accounts.ts": `enum Width {
  Email = 255,
}

export async function up() {
  void Width.Email;

  throw new Error("this migration raised");
}
`,
  });

  setSourceMapsSupport(true);
  t.after(() => setSourceMapsSupport(false));

  t.diagnostic("The enum above compiles to several lines, so the thrown line moves: a stack");
  t.diagnostic("naming line 8 is the inline source map being read rather than ignored.");

  const [entry] = (await fromDirectory(dir)).all();

  assert.ok(entry, "the migration was not loaded");

  await assert.rejects(
    async () => entry.migration.up({} as never),
    (thrown: Error) => {
      assert.match(String(thrown.stack), /20260727T153000_add_accounts\.ts:8:9/);

      return true;
    },
  );
});

test("everything wrong with a directory is reported at once, in name order and once each", async (t) => {
  const dir = project(t, "module", {
    "migrations/README.md": "these are the migrations\n",
    "migrations/20260727T153000-add-accounts.ts": "export async function up() {}\n",
    "migrations/20260727T153100_add_teams.ts":
      "throw new Error('this module cannot be loaded');\n",
    "migrations/20260727T153200_add_users.ts": "export async function up() {}\n",
    "migrations/20260727T153200_add_people.ts": "export async function up() {}\n",
    "migrations/archive/20260101T000000_old.ts": "export async function up() {}\n",
  });

  t.diagnostic("Five problems and exactly five: a filename the grammar refused is not then");
  t.diagnostic("imported as well, and the order is the directory's names rather than the");
  t.diagnostic("filesystem's, which on another filesystem is not the same thing.");

  const reported = await refused(dir);

  assert.equal(reported.length, 5, `reported ${reported.length}: ${reported.join(" | ")}`);
  assert.match(String(reported[0]), /20260727T153000-add-accounts\.ts is not/);
  assert.match(String(reported[1]), /20260727T153100_add_teams\.ts could not be loaded/);
  assert.match(String(reported[2]), /both register version 20260727T153200/);
  assert.match(String(reported[3]), /README\.md is not/);
  assert.match(String(reported[4]), /archive is a directory/);
});

test("an import of a file that is not there names the specifier its author wrote", async (t) => {
  const dir = project(t, "module", {
    "migrations/20260727T153000_add_accounts.ts": `import { nothing } from "./nowhere.js";

export async function up() {
  void nothing;
}
`,
  });

  t.diagnostic("The retry looked for nowhere.ts and did not find it either -- and what the");
  t.diagnostic("author reads is the specifier they wrote, not the one migr guessed.");

  const [reported] = await refused(dir);

  assert.match(String(reported), /could not be loaded/);
  assert.match(String(reported), /nowhere\.js/);
  assert.doesNotMatch(String(reported), /nowhere\.ts/);
});

test("a migration in another language is skipped, where a file that is not one is refused", async (t) => {
  const dir = project(t, "module", {
    "migrations/20260727T153000_add_accounts.ts": "export async function up() {}\n",
    "migrations/20260727T153100_add_teams.go": "package migrations\n",
    "migrations/20260727T153200_add_users.py": "def up(s):\n    pass\n",
    "migrations/README.md": "these are the migrations\n",
  });

  t.diagnostic(
    "One directory may hold two languages (ADR 0048), so a well-formed migration name in",
  );
  t.diagnostic(
    "an extension this runtime does not load belongs to another runtime. A README is not",
  );
  t.diagnostic(
    "that, and is still refused -- the engine routes by extension and holds what each",
  );
  t.diagnostic(
    "runtime reports against what it sent, so nothing is skipped by everything in silence.",
  );

  const reported = await refused(dir);

  assert.equal(reported.length, 1, `reported ${reported.length}: ${reported.join(" | ")}`);
  assert.match(String(reported[0]), /README\.md is not/);
});

test("a directory of another language's migrations alone loads nothing and reports nothing", async (t) => {
  const dir = project(t, "module", {
    "migrations/20260727T153000_add_accounts.go": "package migrations\n",
  });

  const loaded = await fromDirectory(dir);

  assert.deepEqual(loaded.all(), []);
});

test("a dotfile is skipped, which is the one exemption the grammar has", async (t) => {
  const dir = project(t, "module", {
    "migrations/.DS_Store": "",
    "migrations/.eslintrc.json": "{}\n",
    "migrations/20260727T153000_add_accounts.ts": "export async function up() {}\n",
  });

  const loaded = await fromDirectory(dir);

  assert.equal(loaded.all().length, 1);
});

test("an empty directory holds no migrations and is not a problem", async (t) => {
  const loaded = await fromDirectory(project(t, "module", {}));

  assert.deepEqual(loaded.all(), []);
});

test("a directory that cannot be read is reported with the path that could not be read", async () => {
  const missingDir = join(tmpdir(), "migr-directory-nothing-here");

  await assert.rejects(fromDirectory(missingDir), {
    message: new RegExp(`${missingDir} could not be read`),
  });
});

test("the transpiler is named when it cannot be loaded, along with what refused it", () => {
  const refusal = missing(new Error("Cannot find package 'esbuild'"));

  assert.match(refusal.message, /esbuild/);
  assert.match(refusal.message, /reinstall this package's dependencies/);
  assert.equal((refusal.cause as Error).message, "Cannot find package 'esbuild'");
});
