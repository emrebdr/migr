import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";

import { LANGUAGE } from "../index.ts";

import { BINARY_VAR, CACHE_DIR, project, render, table } from "./index.ts";
import type { Project } from "./index.ts";

// How this suite gets a migr to drive, and why it does not skip when it has none.
//
// MIGR_BIN first, then this repository's bin/migr, then a build of this working tree. A migr that
// happened to be on PATH is deliberately not a rung here: it could be an old release, and a suite
// that measured it would be measuring something nobody chose. The last rung being a build rather than
// a skip is what keeps these cases inside `npm test`, which runs before `make build` in CI.

/** here is this directory, from which the repository root is four levels up. */
const here = import.meta.dirname;

/** repository is this working tree's root, which holds the engine these cases are about. */
const repository = join(here, "..", "..", "..", "..");

/** packaged is this package's root, which a staged project links as @migr/sdk. */
const packaged = join(here, "..", "..");

/** built is the engine this suite drives, resolved once and removed by the last case that ran. */
let built: string | undefined;

/** engine reports the migr every case here drives, building this repository's when none is named. */
function engine(): string {
  if (built !== undefined) {
    return built;
  }

  const named = process.env[BINARY_VAR];

  if (named !== undefined && named !== "") {
    built = named;

    return built;
  }

  const release = join(repository, "bin", "migr");

  if (existsSync(release)) {
    built = release;

    return built;
  }

  const into = join(mkdtempSync(join(tmpdir(), "migr-engine-")), "migr");

  execFileSync("go", ["build", "-o", into, "./cmd/migr"], { cwd: repository, stdio: "pipe" });

  built = into;

  return built;
}

/** theMigrations are the two this suite stages: one authoring a down, one leaving it to the engine. */
const theMigrations: { name: string; body: string }[] = [
  {
    name: "20260101T000000_add_accounts.ts",
    body: `import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  up: (s) =>
    s
      .createTable("accounts")
      .column("id", t.int64())
      .column("email", t.string(255))
      .primaryKey("id")
      .apply(),
  down: (s) =>
    s
      .table("accounts")
      .drop()
      .was({
        columns: [
          { name: "id", type: t.int64(), nullable: false },
          { name: "email", type: t.string(255), nullable: false },
        ],
        primary_key: ["id"],
      })
      .apply(),
});
`,
  },
  {
    name: "20260101T000100_add_tier.ts",
    body: `import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  up: (s) => s.table("accounts").addColumn("tier", t.int32()).null().apply(),
});
`,
  },
];

/** dropsWithoutRecording drops an index naming nothing it was, which nothing can invert. */
const dropsWithoutRecording = {
  name: "20260101T000200_drop_index.ts",
  body: `import { defineMigration } from "@migr/sdk";

export default defineMigration({
  up: (s) => s.table("accounts").dropIndex("idx_accounts_email").apply(),
});
`,
};

/** stage writes a project whose migrations directory holds the files given, and returns that directory. */
function stage(...files: { name: string; body: string }[]): string {
  const root = mkdtempSync(join(tmpdir(), "migr-project-"));

  writeFileSync(join(root, "package.json"), `{"name":"staged","type":"module"}\n`);

  const scope = join(root, "node_modules", "@migr");

  mkdirSync(scope, { recursive: true });
  symlinkSync(packaged, join(scope, "sdk"));

  const dir = join(root, "migrations");

  mkdirSync(dir, { recursive: true });

  for (const file of files) {
    writeFileSync(join(dir, file.name), file.body);
  }

  return dir;
}

/** staged stages a project and hands back both it and the cleanup every case owes it (ADR 0045). */
function staged(...files: { name: string; body: string }[]): {
  p: Project;
  dir: string;
  remove: () => void;
} {
  const dir = stage(...(files.length > 0 ? files : theMigrations));
  const p = project({ dir, binary: engine() });

  return {
    p,
    dir,
    remove: () => {
      p.remove();
      rmSync(join(dir, ".."), { recursive: true, force: true });
    },
  };
}

test("a staged project applies and rolls back through the real binary", () => {
  const { p, remove } = staged();

  try {
    const applied = p.up();

    assert.equal(applied.runs.length, 2);
    assert.equal(applied.runs[0]?.file, theMigrations[0]?.name);
    assert.ok((applied.batch ?? 0) > 0);

    assert.deepEqual(p.status().counts.applied, 2);

    assert.equal(p.down().runs.length, 2);
    assert.equal(p.status().counts.applied, 0);
    assert.equal(p.status().counts.pending, 2);
  } finally {
    remove();
  }
});

test("a round trip says the rollback left nothing behind", () => {
  const { p, remove } = staged();

  try {
    p.roundTrip();
  } finally {
    remove();
  }
});

test("the config is written inside the project because the runtime works from it", () => {
  const { p, dir, remove } = staged();

  try {
    assert.equal(
      dirname(dirname(p.config)),
      join(dirname(dir), CACHE_DIR),
      "docs/protocol.md §9 makes the config's directory the runtime's working directory, and the " +
        "Node adapter resolves @migr/sdk from there",
    );

    const body = readFileSync(p.config, "utf8");

    assert.ok(body.includes(`dir: ${JSON.stringify(dir)}`), body);
    assert.equal(readFileSync(join(dirname(dir), CACHE_DIR, ".gitignore"), "utf8"), "*\n");
  } finally {
    remove();
  }

  assert.equal(existsSync(p.config), false, "the config outlived the project that staged it");
});

test("the database is thrown away with the project that made it", () => {
  const { p, remove } = staged();

  try {
    p.up();

    assert.ok(existsSync(p.dsn), `the database was not created at ${p.dsn}`);
  } finally {
    remove();
  }

  assert.equal(existsSync(p.dsn), false, "the database outlived the project that made it");
});

test("validate reports what the checks found and fails nothing", () => {
  const { p, remove } = staged();

  try {
    const report = p.validate();

    assert.deepEqual(report.problems, []);
    assert.equal(report.migrations, 2);
    assert.equal(report.connected, false);
    assert.deepEqual(
      (report.warnings ?? []).map((warning) => warning.rule),
      ["missing-down"],
    );
  } finally {
    remove();
  }
});

test("validate reports a migration nothing can undo", () => {
  const { p, remove } = staged(
    theMigrations[0] as { name: string; body: string },
    dropsWithoutRecording,
  );

  try {
    const report = p.validate();

    assert.ok(report.problems.length > 0, "a migration whose drop records nothing passed");
    assert.equal(report.problems[0]?.check, "reverse");
    assert.equal(report.problems[0]?.version, "20260101T000200");
  } finally {
    remove();
  }
});

test("a failing command is reported with the document it wrote", () => {
  const { p, remove } = staged();

  try {
    const failure = p.refused("down", "--to", "20990101T000000");

    assert.equal(failure.code, 1);
    assert.match(failure.error, /does not carry that version/);
  } finally {
    remove();
  }
});

test("output hands back the document for a command this helper does not type", () => {
  const { p, remove } = staged();

  try {
    p.up();

    const history = JSON.parse(p.output("history")) as {
      runs: { runtime: string }[];
    };

    assert.equal(history.runs.length, 2);
    assert.equal(history.runs[0]?.runtime, LANGUAGE);
  } finally {
    remove();
  }
});

test("the command is built with the config and --json ahead of it", () => {
  const dir = stage(...theMigrations);
  const p = project({ dir, binary: fakeEngine(`printf '%s' "$*"`) });

  try {
    const args = p.output("status").split(" ");

    assert.deepEqual(args.slice(0, 4), ["--config", p.config, "--json", "status"]);
  } finally {
    p.remove();
    rmSync(join(dir, ".."), { recursive: true, force: true });
  }
});

test("a blank answer is never a report", () => {
  const dir = stage(...theMigrations);
  const p = project({
    dir,
    binary: fakeEngine(`echo '{"code":2,"error":"README.md is not a migration"}' >&2; exit 2`),
  });

  try {
    assert.throws(() => p.validate(), /printed no document.*not a migration/s);
  } finally {
    p.remove();
    rmSync(join(dir, ".."), { recursive: true, force: true });
  }
});

test("an exit code is surfaced with what was on stderr", () => {
  const dir = stage(...theMigrations);
  const p = project({
    dir,
    binary: fakeEngine(`echo '{"code":4,"error":"the checksum moved"}' >&2; exit 4`),
  });

  try {
    const failure = p.refused("up");

    assert.equal(failure.code, 4);
    assert.match(failure.error, /checksum moved/);
  } finally {
    p.remove();
    rmSync(join(dir, ".."), { recursive: true, force: true });
  }
});

test("the rendered config names everything the engine needs", () => {
  const rendered = render(
    { dir: "/srv/app/migrations" },
    "/srv/app/migrations",
    "/tmp/app.db",
    "migr_case",
  );

  for (const want of [
    `dir: "/srv/app/migrations"`,
    `language: "${LANGUAGE}"`,
    `driver: "sqlite"`,
    `dsn: "/tmp/app.db"`,
    `table: "migr_case"`,
    "timeout: 5s",
  ]) {
    assert.ok(rendered.includes(want), `${rendered}\ndoes not hold ${want}`);
  }

  assert.equal(
    rendered.includes("validation:"),
    false,
    "a project permitting nothing wrote a block",
  );
});

test("a project permits destruction by naming the kinds", () => {
  const rendered = render(
    { dir: "/srv/app/migrations", destructive: ["raw", "drop_table"] },
    "/srv/app/migrations",
    "/tmp/app.db",
    "migr_case",
  );

  assert.ok(rendered.includes(`allow_destructive: ["raw", "drop_table"]`), rendered);
});

test("a config supplied whole is what is written", () => {
  const supplied = "version: 1\n# whatever the fields cannot describe\n";

  assert.equal(render({ dir: "/a", config: supplied }, "/a", "/b", "c"), supplied);
});

test("each project records its runs in a table of its own", () => {
  const first = table({ dir: "/a" }, "/a/.migr/test-abc123");
  const second = table({ dir: "/a" }, "/a/.migr/test-def456");

  assert.notEqual(
    first,
    second,
    "M7-T8's lesson: the database is the thing that remembers, so two projects sharing one " +
      "history table make the second read the first one's runs as its own",
  );

  assert.match(first, /^[a-z0-9_]+$/);
  assert.equal(table({ dir: "/a", history: "named" }, "/a/.migr/test-abc123"), "named");
});

/** fakeEngine writes a stand-in migr running one shell line, for a case about this helper, not migr. */
function fakeEngine(body: string): string {
  const path = join(mkdtempSync(join(tmpdir(), "migr-fake-")), "migr");

  writeFileSync(path, `#!/bin/sh\n${body}\n`, { mode: 0o700 });

  return path;
}
