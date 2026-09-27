import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import type { ChildProcessWithoutNullStreams } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

// The rules only a real process has, driven through the executable this package installs.
//
// docs/protocol.md §2.1 calls a corrupted stdout the commonest way a new SDK fails, and it fails as a
// parse error three messages later rather than where the print happened -- so the guard is worth a
// spawned child. §9 owes two more: end of file on stdin ends the process, because the engine that
// held the other end of that pipe is gone; and after answering shutdown it exits 0.
//
// What is spawned is dist/esm/migr-runtime-node.js itself, by path and not through node, so the
// shebang and the executable bit are driven too -- that file is what npm links into a project's
// node_modules/.bin, and nothing else here reads it. The project it is pointed at is a real one: a
// package.json, a node_modules/@migr/sdk, and migration files on disk.

const sdk = join(dirname(fileURLToPath(import.meta.url)), "..");

/** RUNTIME is the executable package.json's bin field names, which is what a project runs. */
const RUNTIME = join(sdk, "dist", "esm", "migr-runtime-node.js");

/** MIGRATIONS are the two files every project here holds, one of each format that matters.
 *
 * The .ts one prints twice from inside up, which is where an author's prints are and which is after
 * the guard is installed. §2.1 asks for the redirect "before the first message", so a print from
 * module scope -- before main is even called -- still reaches stdout. That hole is Go's too, and it
 * fails loudly: the engine reports a line it could not frame, naming it.
 *
 * It authors no down, so a rollback of it is §7.4's refusal rather than a failure. The .mjs one
 * authors one, and declares that it follows the first.
 */
const MIGRATIONS: Record<string, string> = {
  "migrations/20260727T153000_add_users.ts": `import { defineMigration, t } from "@migr/sdk";

enum Width {
  Email = 255,
}

export default defineMigration({
  async up(s) {
    console.log("a console line must not reach stdout");
    process.stdout.write("a direct write must not reach stdout\\n");

    await s.createTable("users").column("email", t.string(Width.Email)).apply();
  },
});
`,
  "migrations/20260727T153100_add_teams.mjs": `export const dependencies = ["20260727T153000"];

export async function up(s) {
  await s.createTable("teams").column("name", "string(80)").apply();
}

export async function down(s) {
  await s.table("teams").apply();
}
`,
};

/** Child is one spawned runtime, and the two streams a test reads it through. */
interface Child {
  /** Sends one line to the child's stdin, as the engine would. */
  send(line: string): void;
  /** Waits for the next line the child writes to stdout. */
  take(): Promise<string>;
  /** Closes the child's stdin, which §9 makes the end of its session. */
  close(): void;
  /** Everything the child has written to stderr, which is never parsed. */
  logged(): string;
  /** The status the child exits with. */
  done: Promise<number>;
}

/** Runtime is one spawned runtime and the project it was pointed at. */
interface Runtime extends Child {
  /** The project root, which §9 makes the working directory and which the handshake is built from. */
  root: string;
  /** The handshake naming this project's migrations directory, which arrives absolute. */
  handshake: string;
  /** Kills the child and removes the project, whatever the test made of either. */
  remove(): void;
}

/** started writes a project, spawns the runtime in it, and returns both over real pipes. */
function started(files: Record<string, string> = MIGRATIONS): Runtime {
  const root = mkdtempSync(join(tmpdir(), "migr-process-"));

  writeFileSync(join(root, "package.json"), '{"name":"fixture","type":"module"}\n');
  mkdirSync(join(root, "node_modules", "@migr"), { recursive: true });
  symlinkSync(sdk, join(root, "node_modules", "@migr", "sdk"));
  mkdirSync(join(root, "migrations"), { recursive: true });

  for (const [path, contents] of Object.entries(files)) {
    writeFileSync(join(root, path), contents);
  }

  const child = spawn(RUNTIME, { cwd: root, stdio: ["pipe", "pipe", "pipe"] });

  return {
    ...read(child),
    root,
    handshake: handshakeFor(join(root, "migrations")),
    remove: () => {
      child.kill();
      rmSync(root, { recursive: true, force: true });
    },
  };
}

/** read collects one child's two streams and its exit status, line by line on stdout. */
function read(child: ChildProcessWithoutNullStreams): Child {
  const held: string[] = [];
  const waiting: ((line: string) => void)[] = [];
  let stdout = "";
  let stderr = "";

  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");

  child.stdout.on("data", (chunk: string) => {
    stdout += chunk;

    for (;;) {
      const at = stdout.indexOf("\n");

      if (at < 0) {
        return;
      }

      const line = stdout.slice(0, at);

      stdout = stdout.slice(at + 1);

      const next = waiting.shift();

      if (next === undefined) {
        held.push(line);
      } else {
        next(line);
      }
    }
  });

  child.stderr.on("data", (chunk: string) => {
    stderr += chunk;
  });

  return {
    send: (line) => {
      child.stdin.write(`${line}\n`);
    },
    take: () => {
      const first = held.shift();

      return first === undefined
        ? within(new Promise<string>((resolve) => waiting.push(resolve)), "a line on stdout")
        : Promise.resolve(first);
    },
    close: () => {
      child.stdin.end();
    },
    logged: () => stderr,
    done: within(
      new Promise<number>((resolve) => {
        child.on("exit", (code) => resolve(code ?? -1));
      }),
      "the child to exit",
    ),
  };
}

/** within bounds a wait, so a child that never answers fails the test rather than hanging it. */
function within<T>(waiting: Promise<T>, what: string): Promise<T> {
  return Promise.race([
    waiting,
    new Promise<T>((_, reject) => {
      const timer = setTimeout(() => reject(new Error(`waited 10s for ${what}`)), 10_000);

      void waiting.finally(() => clearTimeout(timer));
    }),
  ]);
}

/** handshakeFor is the first line on the channel, which no other request may precede. */
function handshakeFor(dir: string): string {
  return (
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersions":[1],' +
    `"irVersion":1,"client":{"name":"migr","version":"0.1.0"},"migrations":` +
    `{"dir":${JSON.stringify(dir)},"language":"typescript"},"database":` +
    '{"name":"default","driver":"postgres"}}}'
  );
}

/** opened sends the handshake and reads its answer, which every other exchange follows. */
async function opened(child: Runtime): Promise<string> {
  child.send(child.handshake);

  return child.take();
}

test("the handshake is answered on stdout, and shutdown is answered before it exits 0", async () => {
  const child = started();

  try {
    assert.match(
      await opened(child),
      /^\{"jsonrpc":"2\.0","id":1,"result":\{"protocolVersion":1,/,
    );

    child.send('{"jsonrpc":"2.0","id":2,"method":"shutdown","params":{}}');

    assert.equal(await child.take(), '{"jsonrpc":"2.0","id":2,"result":{}}');
    assert.equal(await child.done, 0);
  } finally {
    child.remove();
  }
});

test("every migration in the directory is discovered, with what each one declares", async () => {
  const child = started();

  try {
    await opened(child);

    child.send('{"jsonrpc":"2.0","id":2,"method":"migration.discover","params":{}}');

    assert.equal(
      await child.take(),
      '{"jsonrpc":"2.0","id":2,"result":{"migrations":[' +
        '{"version":"20260727T153000","file":"20260727T153000_add_users.ts","dependencies":[]},' +
        '{"version":"20260727T153100","file":"20260727T153100_add_teams.mjs",' +
        '"dependencies":["20260727T153000"]}]}}',
    );
  } finally {
    child.remove();
  }
});

test("a TypeScript migration on disk is executed, and emits the IR the document specifies", async () => {
  const child = started();

  try {
    await opened(child);

    child.send(
      '{"jsonrpc":"2.0","id":2,"method":"migration.execute","params":' +
        '{"id":"20260727T153000","direction":"up","mode":"apply"}}',
    );

    assert.equal(
      await child.take(),
      '{"jsonrpc":"2.0","id":1,"method":"host.schema.apply","params":{"operations":' +
        '[{"op":"create_table","table":"users","columns":' +
        '[{"name":"email","type":"string(255)","nullable":false}]}]}}',
    );

    child.send('{"jsonrpc":"2.0","id":1,"result":{"recorded":1}}');

    assert.equal(await child.take(), '{"jsonrpc":"2.0","id":2,"result":{"operations":1}}');
  } finally {
    child.remove();
  }
});

test("a migration that authors no down refuses the rollback rather than emitting nothing", async () => {
  const child = started();

  try {
    await opened(child);

    child.send(
      '{"jsonrpc":"2.0","id":2,"method":"migration.execute","params":' +
        '{"id":"20260727T153000","direction":"down","mode":"apply"}}',
    );

    assert.equal(
      await child.take(),
      '{"jsonrpc":"2.0","id":2,"error":{"code":-32012,"message":' +
        '"20260727T153000_add_users.ts authors no down of its own",' +
        '"data":{"slug":"no_down_authored"}}}',
    );
  } finally {
    child.remove();
  }
});

test("what a migration prints goes to stderr, and stdout carries protocol and nothing else", async () => {
  const child = started();

  try {
    await opened(child);

    child.send(
      '{"jsonrpc":"2.0","id":2,"method":"migration.execute","params":' +
        '{"id":"20260727T153000","direction":"up","mode":"apply"}}',
    );

    assert.match(await child.take(), /^\{"jsonrpc":"2\.0","id":1,"method":"host\.schema\.apply"/);

    child.send('{"jsonrpc":"2.0","id":1,"result":{"recorded":1}}');

    assert.equal(await child.take(), '{"jsonrpc":"2.0","id":2,"result":{"operations":1}}');

    child.close();

    assert.equal(await child.done, 0);
    assert.match(child.logged(), /a console line must not reach stdout/);
    assert.match(child.logged(), /a direct write must not reach stdout/);
  } finally {
    child.remove();
  }
});

test("a directory nothing can be loaded from is reported at migration.discover", async () => {
  const child = started({ "migrations/README.md": "these are the migrations\n" });

  try {
    await opened(child);

    child.send('{"jsonrpc":"2.0","id":2,"method":"migration.discover","params":{}}');

    const answered = await child.take();

    assert.match(answered, /"code":-32603/);
    assert.match(answered, /README\.md is not/);
  } finally {
    child.remove();
  }
});

test("a migrations directory that is not there is reported at migration.discover", async () => {
  const child = started();

  try {
    child.send(handshakeFor(join(child.root, "not-the-migrations-directory")));
    await child.take();

    child.send('{"jsonrpc":"2.0","id":2,"method":"migration.discover","params":{}}');

    const answered = await child.take();

    assert.match(answered, /"code":-32603/);
    assert.match(answered, /not-the-migrations-directory could not be read/);
  } finally {
    child.remove();
  }
});

test("a stack trace inside a migration names the author's line, since source maps are enabled", async () => {
  const child = started({
    "migrations/20260727T153000_add_users.ts": `enum Width {
  Email = 255,
}

export async function up(s) {
  void Width.Email;

  s.log("error", String(new Error("where am I").stack).split("\\n")[1]);
}
`,
  });

  try {
    await opened(child);

    child.send(
      '{"jsonrpc":"2.0","id":2,"method":"migration.execute","params":' +
        '{"id":"20260727T153000","direction":"up","mode":"apply"}}',
    );

    const logged = await child.take();

    assert.match(logged, /"method":"host\.log"/);
    assert.match(logged, /20260727T153000_add_users\.ts:8/);
  } finally {
    child.remove();
  }
});

test("end of file on stdin ends the process, because the engine is gone", async () => {
  const child = started();

  try {
    child.close();

    assert.equal(await child.done, 0);
  } finally {
    child.remove();
  }
});

test("a line the child cannot frame is answered once with a null id, and it exits a failure", async () => {
  const child = started();

  try {
    child.send("not json");

    assert.match(await child.take(), /^\{"jsonrpc":"2\.0","id":null,"error":\{"code":-32700,/);
    assert.equal(await child.done, 1);
  } finally {
    child.remove();
  }
});
