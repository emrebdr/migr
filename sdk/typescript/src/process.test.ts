import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import type { ChildProcessWithoutNullStreams } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

// The three rules only a real process has, which in-memory streams cannot see.
//
// docs/protocol.md §2.1 calls a corrupted stdout the commonest way a new SDK fails, and it fails as
// a parse error three messages later rather than where the print happened -- so the guard is worth a
// spawned child. §9 owes two more: end of file on stdin ends the process, because the engine that
// held the other end of that pipe is gone; and after answering shutdown it exits 0.
//
// The child imports the built ESM entry rather than the sources beside it, so what is driven here is
// what ships. It does not resolve @migr/sdk by name: Node's self-reference does not cross a
// node_modules boundary, and a runner written outside the package has no other way in.

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");

/** runnerFor is a runtime written the way M7-T5's binary will be, whose migration prints twice.
 *
 * Both prints are inside the migration, which is where an author's are and which is after the guard
 * is installed. §2.1 asks for the redirect "before the first message", so a print from module scope
 * -- before run is even called -- still reaches stdout. That hole is Go's too, and it fails loudly:
 * the engine reports a line it could not frame, naming it.
 */
function runnerFor(entry: string): string {
  return `import { Registry, run } from ${JSON.stringify(entry)};

const code = await run({
  load: async () => {
    const registry = new Registry();

    registry.add("20260727T153000_add_users.ts", {
      default: {
        up: async (s) => {
          console.log("a console line must not reach stdout");
          process.stdout.write("a direct write must not reach stdout\\n");

          await s.apply({ op: "raw", driver: "postgres", sql: "SELECT 1" });
        },
      },
    });

    return registry;
  },
});

process.exit(code);
`;
}

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

/** started writes a runtime that imports what was built and spawns it over real pipes. */
function started(): Child & { remove(): void } {
  const written = mkdtempSync(join(tmpdir(), "migr-process-"));
  const runner = join(written, "runtime.mjs");

  writeFileSync(runner, runnerFor(pathToFileURL(join(root, "dist", "esm", "index.js")).href));

  const child = spawn(process.execPath, [runner], { stdio: ["pipe", "pipe", "pipe"] });

  return { ...read(child), remove: () => rmSync(written, { recursive: true, force: true }) };
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

/** HANDSHAKE is the first line on the channel, which no other request may precede. */
const HANDSHAKE =
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersions":[1],' +
  '"irVersion":1,"client":{"name":"migr","version":"0.1.0"},"migrations":' +
  '{"dir":"/srv/app/migrations","language":"typescript"},"database":' +
  '{"name":"default","driver":"postgres"}}}';

test("the handshake is answered on stdout, and shutdown is answered before it exits 0", async () => {
  const child = started();

  try {
    child.send(HANDSHAKE);

    assert.match(
      await child.take(),
      /^\{"jsonrpc":"2\.0","id":1,"result":\{"protocolVersion":1,/,
    );

    child.send('{"jsonrpc":"2.0","id":2,"method":"shutdown","params":{}}');

    assert.equal(await child.take(), '{"jsonrpc":"2.0","id":2,"result":{}}');
    assert.equal(await child.done, 0);
  } finally {
    child.remove();
  }
});

test("what a migration prints goes to stderr, and stdout carries protocol and nothing else", async () => {
  const child = started();

  try {
    child.send(HANDSHAKE);
    await child.take();

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
