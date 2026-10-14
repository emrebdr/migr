import assert from "node:assert/strict";
import { PassThrough } from "node:stream";
import { test } from "node:test";

import { Registry } from "./load.ts";
import type { Migration } from "./migration.ts";
import type { Schema } from "./schema.ts";
import { EXIT_FAILED, EXIT_OK, run } from "./session.ts";
import { t } from "./type.ts";
import type { Message } from "./wire/message.ts";
import { decode, encode, notification, request, response } from "./wire/message.ts";

// The session over a pair of streams, which is everything about it except the process.
//
// One read loop is what keeps $/cancel readable while work is running, and the two tables are what
// keep an id unique within its own direction. Both are asserted here rather than reasoned about.

/** FILE is the filename these migrations are read under, which the grammar accepts. */
const FILE = "20260727T153000_add_users.ts";

/** VERSION is the version that filename carries, which every message names it by. */
const VERSION = "20260727T153000";

/** HANDSHAKE is what the engine offers, which this SDK answers with the one version it speaks. */
const HANDSHAKE = {
  protocolVersions: [1],
  irVersion: 1,
  client: { name: "migr", version: "0.1.0" },
  migrations: { dir: "/srv/app/migrations", language: "typescript" },
  database: { name: "default", driver: "postgres" },
};

/** Session is one running session, and the two ends a test drives it from. */
interface Session {
  /** Sends one message to the runtime, as the engine would. */
  send(m: Message): void;
  /** Waits for the next message the runtime writes. */
  take(): Promise<Message>;
  /** Closes the runtime's stdin, which §9 makes the end of its session. */
  close(): void;
  /** The exit status the session settles on. */
  done: Promise<number>;
}

/** opened starts one session over a pair of streams, with the migrations it can run. */
function opened(...loaded: [string, unknown][]): Session {
  const input = new PassThrough();
  const held: Message[] = [];
  const waiting: ((m: Message) => void)[] = [];

  const done = run({
    input,
    load: async () => {
      const registry = new Registry();

      for (const [file, namespace] of loaded) {
        registry.add(file, namespace);
      }

      return registry;
    },
    send: async (line) => {
      const m = decode(line);
      const next = waiting.shift();

      if (next === undefined) {
        held.push(m);

        return;
      }

      next(m);
    },
  });

  return {
    send: (m) => {
      input.write(`${encode(m)}\n`);
    },
    take: () => {
      const first = held.shift();

      return first === undefined
        ? new Promise<Message>((resolve) => waiting.push(resolve))
        : Promise.resolve(first);
    },
    close: () => {
      input.end();
    },
    done,
  };
}

/** handshaken opens a session and completes the handshake, which every method needs first. */
async function handshaken(...loaded: [string, unknown][]): Promise<Session> {
  const session = opened(...loaded);

  session.send(request(1, "initialize", HANDSHAKE));

  const answer = await session.take();

  assert.equal(answer.error, undefined, `the handshake failed: ${JSON.stringify(answer.error)}`);

  return session;
}

/** ended shuts a session down and reports the status it settled on. */
async function ended(session: Session): Promise<number> {
  session.send(request(99, "shutdown", {}));
  await session.take();

  return session.done;
}

/** Refused is what one failed answer says: its code, its slug, its prose, and its data. */
interface Refused {
  code: number;
  slug: string;
  message: string;
  data: Record<string, unknown>;
}

/** failureOf reads the error one answer carries, failing the test when it carries none. */
function failureOf(answer: Message): Refused {
  assert.ok(answer.error, `the answer carries no error: ${JSON.stringify(answer)}`);

  const data = answer.error.data as Record<string, unknown>;

  return {
    code: answer.error.code,
    slug: String(data["slug"]),
    message: answer.error.message,
    data,
  };
}

/** creating is a migration that emits one operation and nothing else. */
const creating: Migration = {
  async up(s: Schema) {
    await s.createTable("users").column("id", t.uuid()).primaryKey("id").apply();
  },
};

test("the handshake answers with the one version this SDK speaks, and what implements it", async () => {
  const session = opened();

  session.send(request(1, "initialize", HANDSHAKE));

  const answer = await session.take();
  const result = answer.result as Record<string, Record<string, string> | number>;

  assert.equal(answer.id, 1);
  assert.equal(result["protocolVersion"], 1);
  assert.equal((result["runtime"] as Record<string, string>)["name"], "typescript");
  assert.equal((result["sdk"] as Record<string, string>)["name"], "@migr/sdk");
  assert.equal(await ended(session), EXIT_OK);
});

test("a protocol version this SDK does not speak is refused, listing what it does", async () => {
  const session = opened();

  session.send(request(1, "initialize", { ...HANDSHAKE, protocolVersions: [2, 3] }));

  const refused = failureOf(await session.take());

  assert.equal(refused.code, -32001);
  assert.equal(refused.slug, "unsupported_protocol");
  assert.deepEqual(refused.data["supported"], [1]);

  session.close();
  assert.equal(await session.done, EXIT_OK);
});

test("any IR version an engine states is accepted, and only none at all is refused", async () => {
  for (const stated of [1, 2, 3]) {
    const session = opened();

    session.send(request(1, "initialize", { ...HANDSHAKE, irVersion: stated }));

    const answer = await session.take();

    assert.equal(answer.error, undefined, `an engine stating irVersion ${stated} was refused`);
    assert.equal(await ended(session), EXIT_OK);
  }

  const session = opened();

  session.send(request(1, "initialize", { ...HANDSHAKE, irVersion: 0 }));

  assert.equal(failureOf(await session.take()).slug, "unsupported_ir");

  session.close();
  await session.done;
});

test("a data operation against an engine that reads only v1 is refused before it is sent", async () => {
  const backfilling: Migration = {
    async up(s) {
      await s.table("users").backfill("plan", t.string(16), { literal: "free" }).apply();
    },
  };

  const session = await handshaken([FILE, { default: backfilling }]);

  session.send(request(2, "migration.execute", { id: VERSION, direction: "up", mode: "apply" }));

  const answer = await session.take();

  assert.equal(answer.method, undefined, "the operation reached the host before it was refused");

  const refused = failureOf(answer);

  assert.equal(refused.slug, "unsupported_ir");

  for (const want of ["data_backfill", "irVersion 2", "stated 1"]) {
    assert.ok(
      refused.message.includes(want),
      `the failure does not name ${want}: ${refused.message}`,
    );
  }

  assert.equal(await ended(session), EXIT_OK);
});

test("a request before initialize, and a second initialize, are both refused", async () => {
  const session = opened();

  session.send(request(1, "migration.discover", {}));
  assert.equal(failureOf(await session.take()).slug, "session_state");

  session.send(request(2, "initialize", HANDSHAKE));
  await session.take();

  session.send(request(3, "initialize", HANDSHAKE));
  assert.equal(failureOf(await session.take()).slug, "session_state");

  assert.equal(await ended(session), EXIT_OK);
});

test("a method this protocol version does not define is refused and the session continues", async () => {
  const session = await handshaken();

  session.send(request(2, "migration.rewind", {}));

  const refused = failureOf(await session.take());

  assert.equal(refused.code, -32601);
  assert.equal(refused.slug, "method_not_found");
  assert.equal(await ended(session), EXIT_OK);
});

test("discovery reports what was loaded, with the versions each migration declares", async () => {
  const session = await handshaken([
    FILE,
    { default: { ...creating, dependencies: ["20260726T090000"] } },
  ]);

  session.send(request(2, "migration.discover", {}));

  const result = (await session.take()).result as { migrations: Record<string, unknown>[] };

  assert.deepEqual(result.migrations, [
    { version: VERSION, file: FILE, dependencies: ["20260726T090000"] },
  ]);

  assert.equal(await ended(session), EXIT_OK);
});

test("executing a migration emits through the callback and reports what it emitted", async () => {
  const session = await handshaken([FILE, { default: creating }]);

  session.send(request(2, "migration.execute", { id: VERSION, direction: "up", mode: "apply" }));

  const callback = await session.take();

  assert.equal(callback.method, "host.schema.apply");
  assert.equal(callback.id, 1);

  const { operations } = callback.params as { operations: unknown[] };

  assert.equal(operations.length, 1);

  session.send(response(1, { recorded: 1 }));

  const answer = await session.take();

  assert.deepEqual(answer.result, { operations: 1 });
  assert.equal(answer.id, 2);
  assert.equal(await ended(session), EXIT_OK);
});

test("a migration reads before it decides, and every value comes back as text", async () => {
  const seen: (string | null)[][] = [];
  const reading: Migration = {
    async up(s) {
      const rows = await s.query("SELECT count(*) AS n FROM users WHERE email IS $1", "NULL");

      seen.push(...rows.rows);

      await s.apply({ op: "raw", driver: "postgres", sql: "ANALYZE users" });
    },
  };

  const session = await handshaken([FILE, { default: reading }]);

  session.send(request(2, "migration.execute", { id: VERSION, direction: "up", mode: "apply" }));

  const read = await session.take();

  assert.equal(read.method, "host.db.query");
  assert.deepEqual(read.params, {
    sql: "SELECT count(*) AS n FROM users WHERE email IS $1",
    args: ["NULL"],
  });

  session.send(response(1, { columns: ["n"], rows: [["4711"], [null]] }));

  assert.equal((await session.take()).method, "host.schema.apply");
  session.send(response(2, { recorded: 1 }));

  assert.deepEqual((await session.take()).result, { operations: 1 });
  assert.deepEqual(seen, [["4711"], [null]]);
  assert.equal(await ended(session), EXIT_OK);
});

test("several callbacks may be in flight, and each takes its own answer", async () => {
  const answers: string[] = [];
  const asking: Migration = {
    async up(s) {
      const [one, two] = await Promise.all([s.info(), s.exec("ANALYZE users")]);

      answers.push(one.driver, two.rowsAffected);

      await s.apply({ op: "raw", driver: "postgres", sql: "SELECT 1" });
    },
  };

  const session = await handshaken([FILE, { default: asking }]);

  session.send(request(2, "migration.execute", { id: VERSION, direction: "up", mode: "apply" }));

  const first = await session.take();
  const second = await session.take();

  assert.deepEqual([first.method, second.method], ["host.info", "host.db.exec"]);

  session.send(response(2, { rowsAffected: "7", executed: true }));
  session.send(
    response(1, {
      database: "default",
      driver: "postgres",
      server: "17.2",
      transactionalDDL: true,
      mode: "apply",
    }),
  );

  assert.equal((await session.take()).method, "host.schema.apply");
  session.send(response(3, { recorded: 1 }));

  await session.take();

  assert.deepEqual(answers, ["postgres", "7"]);
  assert.equal(await ended(session), EXIT_OK);
});

test("an error the host answers with reaches the migration as the slug it carries", async () => {
  let slug = "";
  const refused: Migration = {
    async up(s) {
      try {
        await s.query("DELETE FROM users");
      } catch (thrown) {
        slug = (thrown as { slug: string }).slug;
      }

      await s.apply({ op: "raw", driver: "postgres", sql: "SELECT 1" });
    },
  };

  const session = await handshaken([FILE, { default: refused }]);

  session.send(request(2, "migration.execute", { id: VERSION, direction: "up", mode: "apply" }));
  await session.take();

  session.send({
    jsonrpc: "2.0",
    id: 1,
    error: {
      code: -32020,
      message: "that statement could write",
      data: { slug: "not_read_only" },
    },
  });

  assert.equal((await session.take()).method, "host.schema.apply");
  session.send(response(2, { recorded: 1 }));

  await session.take();

  assert.equal(slug, "not_read_only");
  assert.equal(await ended(session), EXIT_OK);
});

test("$/cancel answers the named request canceled, and the migration stops at its next call", async () => {
  let reached = false;
  const slow: Migration = {
    async up(s) {
      await s.info();

      reached = true;

      await s.apply({ op: "raw", driver: "postgres", sql: "SELECT 1" });
    },
  };

  const session = await handshaken([FILE, { default: slow }]);

  session.send(request(2, "migration.execute", { id: VERSION, direction: "up", mode: "apply" }));
  await session.take();

  session.send(notification("$/cancel", { id: 2 }));

  const answer = failureOf(await session.take());

  assert.equal(answer.code, -32030);
  assert.equal(answer.slug, "canceled");
  assert.equal(reached, false);
  assert.equal(await ended(session), EXIT_OK);
});

test("a migration that finishes after the cancel is still answered canceled, not answered done", async () => {
  const racing: Migration = {
    async up(s) {
      await s.apply({ op: "raw", driver: "postgres", sql: "SELECT 1" });

      await new Promise<void>((resolve) => {
        s.signal.addEventListener("abort", () => resolve(), { once: true });
      });
    },
  };

  const session = await handshaken([FILE, { default: racing }]);

  session.send(request(2, "migration.execute", { id: VERSION, direction: "up", mode: "apply" }));

  const emitting = await session.take();

  session.send(response(emitting.id as number, { recorded: 1 }));
  session.send(notification("$/cancel", { id: 2 }));

  const answer = failureOf(await session.take());

  assert.equal(answer.slug, "canceled");
  assert.equal(await ended(session), EXIT_OK);
});

test("a host that records fewer operations than it was sent fails the migration", async () => {
  const session = await handshaken([FILE, { default: creating }]);

  session.send(request(2, "migration.execute", { id: VERSION, direction: "up", mode: "apply" }));

  const emitting = await session.take();

  session.send(response(emitting.id as number, { recorded: 0 }));

  const answer = failureOf(await session.take());

  assert.equal(answer.slug, "migration_failed");
  assert.match(answer.message, /recorded 0 of the 1 operations sent/);
  assert.equal(await ended(session), EXIT_OK);
});

test("cancelling an id nothing is running is ignored, silently", async () => {
  const session = await handshaken([FILE, { default: creating }]);

  session.send(notification("$/cancel", { id: 77 }));
  session.send(request(2, "migration.discover", {}));

  assert.equal((await session.take()).id, 2);
  assert.equal(await ended(session), EXIT_OK);
});

test("an id naming no migration this runtime loaded is refused as that", async () => {
  const session = await handshaken([FILE, { default: creating }]);

  session.send(
    request(2, "migration.execute", { id: "20990101T000000", direction: "up", mode: "apply" }),
  );

  assert.equal(failureOf(await session.take()).slug, "unknown_migration");
  assert.equal(await ended(session), EXIT_OK);
});

test("a migration with no down of its own answers so, which is not a failure", async () => {
  const session = await handshaken([FILE, { default: creating }]);

  session.send(
    request(2, "migration.execute", { id: VERSION, direction: "down", mode: "apply" }),
  );

  const refused = failureOf(await session.take());

  assert.equal(refused.code, -32012);
  assert.equal(refused.slug, "no_down_authored");
  assert.match(refused.message, new RegExp(FILE));
  assert.equal(await ended(session), EXIT_OK);
});

test("a migration declaring itself one-way answers with its author's reason", async () => {
  const session = await handshaken([
    FILE,
    { default: { up: () => undefined, irreversible: "the rows are gone" } },
  ]);

  session.send(
    request(2, "migration.execute", { id: VERSION, direction: "down", mode: "apply" }),
  );

  const refused = failureOf(await session.take());

  assert.equal(refused.code, -32013);
  assert.equal(refused.data["reason"], "the rows are gone");
  assert.equal(await ended(session), EXIT_OK);
});

test("a migration's own failure is reported as its own, naming what it raised", async () => {
  const session = await handshaken([
    FILE,
    {
      default: {
        up: () => {
          throw new Error("the accounts table is not there");
        },
      },
    },
  ]);

  session.send(request(2, "migration.execute", { id: VERSION, direction: "up", mode: "apply" }));

  const refused = failureOf(await session.take());

  assert.equal(refused.slug, "migration_failed");
  assert.match(refused.message, /accounts table is not there/);
  assert.equal(await ended(session), EXIT_OK);
});

test("validation runs the migration's own check, and one that has none passes", async () => {
  const session = await handshaken(
    [
      FILE,
      {
        default: {
          up: () => undefined,
          validate: () => {
            throw new Error("this migration needs a column it has not got");
          },
        },
      },
    ],
    ["20260728T090000_add_teams.ts", { default: creating }],
  );

  session.send(request(2, "migration.validate", { id: VERSION }));

  const refused = failureOf(await session.take());

  assert.equal(refused.slug, "migration_failed");
  assert.match(refused.message, /needs a column/);
  assert.match(refused.message, new RegExp(FILE));

  session.send(request(3, "migration.validate", { id: "20260728T090000" }));
  assert.deepEqual((await session.take()).result, {});

  assert.equal(await ended(session), EXIT_OK);
});

test("parameters of the wrong shape are refused as that, and the session continues", async () => {
  const session = await handshaken([FILE, { default: creating }]);

  session.send(
    request(2, "migration.execute", { id: VERSION, direction: "sideways", mode: "apply" }),
  );
  assert.equal(failureOf(await session.take()).slug, "invalid_params");

  session.send(request(3, "migration.validate", {}));
  assert.equal(failureOf(await session.take()).slug, "invalid_params");

  session.send(request(4, "migration.validate", { id: "" }));
  assert.equal(failureOf(await session.take()).slug, "invalid_params");

  assert.equal(await ended(session), EXIT_OK);
});

test("a response nothing is waiting on is dropped, where a framing failure ends the session", async () => {
  const session = await handshaken([FILE, { default: creating }]);

  session.send(response(4711, { recorded: 0 }));
  session.send(request(2, "migration.discover", {}));

  assert.equal((await session.take()).id, 2);
  assert.equal(await ended(session), EXIT_OK);
});

test("end of file ends the process, because the engine that held the pipe is gone", async () => {
  const session = await handshaken([FILE, { default: creating }]);

  session.close();

  assert.equal(await session.done, EXIT_OK);
});

test("a malformed line writes the one null-id error and exits a failure", async () => {
  const input = new PassThrough();
  const written: string[] = [];
  const done = run({
    input,
    load: async () => new Registry(),
    send: async (line) => {
      written.push(line);
    },
  });

  input.write("not json\n");

  assert.equal(await done, EXIT_FAILED);
  assert.equal(written.length, 1);
  assert.match(written[0] as string, /^\{"jsonrpc":"2\.0","id":null,"error":\{"code":-32700,/);
});

test("a log line is a notification, which carries no id and is never answered", async () => {
  const logging: Migration = {
    async up(s) {
      s.log("info", "backfilling in chunks", { chunk: "5000" });

      await s.apply({ op: "raw", driver: "postgres", sql: "SELECT 1" });
    },
  };

  const session = await handshaken([FILE, { default: logging }]);

  session.send(request(2, "migration.execute", { id: VERSION, direction: "up", mode: "apply" }));

  const logged = await session.take();

  assert.equal("id" in logged, false);
  assert.deepEqual(logged.params, {
    level: "info",
    message: "backfilling in chunks",
    fields: { chunk: "5000" },
  });

  assert.equal((await session.take()).method, "host.schema.apply");
  session.send(response(1, { recorded: 1 }));

  await session.take();
  assert.equal(await ended(session), EXIT_OK);
});
