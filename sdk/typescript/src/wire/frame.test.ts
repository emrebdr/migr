import assert from "node:assert/strict";
import { test } from "node:test";

import { INVALID_REQUEST, PARSE_ERROR, ProtocolError } from "./code.ts";
import { MAX_LINE, newReader, newWriter, refuse, render } from "./frame.ts";
import type { Message } from "./message.ts";
import { notification, request, response } from "./message.ts";

// docs/protocol.md §2.2, §2.3, and §2.6: the line is the frame, the frame is bounded, and a line
// that cannot be framed ends the session rather than being skipped.

/** bytes hands a reader one stream of chunks, which is what a pipe delivers. */
async function* bytes(...chunks: (string | Uint8Array)[]): AsyncIterable<Uint8Array> {
  for (const chunk of chunks) {
    yield typeof chunk === "string" ? Buffer.from(chunk, "utf8") : chunk;
  }
}

/** read takes every message one stream carries, which is what a session's loop does. */
async function read(...chunks: (string | Uint8Array)[]): Promise<Message[]> {
  const reader = newReader(bytes(...chunks));
  const found: Message[] = [];

  for (;;) {
    const m = await reader.read();

    if (m === undefined) {
      return found;
    }

    found.push(m);
  }
}

/** refusal reads a stream expected to end the session, returning what ended it. */
async function refusal(...chunks: (string | Uint8Array)[]): Promise<ProtocolError> {
  return refusalOf(bytes(...chunks));
}

/** refusalOf reads one source expected to end the session, returning what ended it. */
async function refusalOf(source: AsyncIterable<Uint8Array>): Promise<ProtocolError> {
  const reader = newReader(source);

  try {
    while ((await reader.read()) !== undefined);
  } catch (thrown) {
    assert.ok(thrown instanceof ProtocolError, `ended with ${String(thrown)}`);

    return thrown;
  }

  return assert.fail("the stream was read to the end");
}

test("the line is the frame, and one chunk may carry several or part of one", async () => {
  const one = '{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}';
  const two = '{"jsonrpc":"2.0","id":2,"method":"shutdown","params":{}}';

  assert.deepEqual(
    (await read(`${one}\n${two}\n`)).map((m) => m.id),
    [1, 2],
  );

  assert.deepEqual(
    (await read(one.slice(0, 10), one.slice(10), "\n", `${two}\n`)).map((m) => m.id),
    [1, 2],
  );
});

test("a carriage return is accepted on input and an empty line is not a message", async () => {
  const found = await read(
    '{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}\r\n',
    "\n",
    "\r\n",
    '{"jsonrpc":"2.0","id":2,"method":"shutdown","params":{}}\n',
  );

  assert.deepEqual(
    found.map((m) => m.id),
    [1, 2],
  );
});

test("a byte-order mark is a parse error, since no stream here ever carries one", async () => {
  const refused = await refusal(
    Buffer.concat([
      Buffer.of(0xef, 0xbb, 0xbf),
      Buffer.from('{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}\n'),
    ]),
  );

  assert.equal(refused.code, PARSE_ERROR);
  assert.match(refused.message, /byte-order mark/);
});

test("invalid UTF-8 is a parse error rather than a run of replacement characters", async () => {
  const refused = await refusal(
    Buffer.concat([Buffer.of(0x7b, 0xff, 0xfe, 0x7d), Buffer.of(0x0a)]),
  );

  assert.equal(refused.code, PARSE_ERROR);
  assert.match(refused.message, /not valid UTF-8/);
});

test("a line is refused at the budget, not after a newline that may never arrive", async () => {
  let offered = 0;

  const refused = await refusalOf(
    (async function* (): AsyncIterable<Uint8Array> {
      while (offered < 64) {
        offered += 1;

        yield Buffer.alloc(1 << 20, 0x20);
      }
    })(),
  );

  assert.equal(refused.code, PARSE_ERROR);
  assert.match(refused.message, new RegExp(`longer than the ${MAX_LINE >> 20} MiB`));
  assert.ok(
    offered <= (MAX_LINE >> 20) + 1,
    `the reader took ${offered} MiB before refusing, and the budget is ${MAX_LINE >> 20}`,
  );
});

test("a stream cut off part way through a line is a parse error", async () => {
  const refused = await refusal('{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}');

  assert.equal(refused.code, PARSE_ERROR);
  assert.match(refused.message, /ends without a newline/);
});

test("a stream ending in whitespace with no newline has simply ended", async () => {
  assert.deepEqual(
    (await read('{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}\n', " ")).map(
      (m) => m.id,
    ),
    [1],
  );
});

test("a line that is only whitespace is a parse error, where an empty one is skipped", async () => {
  const refused = await refusal("   \n");

  assert.equal(refused.code, PARSE_ERROR);
  assert.match(refused.message, /carries no message/);
});

test("a reader is spent once it meets a line it cannot frame", async () => {
  const reader = newReader(
    bytes("not json\n", '{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}\n'),
  );

  await assert.rejects(reader.read(), /not JSON/);
  await assert.rejects(reader.read(), /not JSON/);
});

test("a failure names the line it was found on, which is what a peer can act on", async () => {
  const refused = await refusal(
    '{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}\n',
    "[1,2]\n",
  );

  assert.equal(refused.code, INVALID_REQUEST);
  assert.match(refused.message, /^line 2: /);
});

test("the one null-id error is what a peer that sent something malformed is told", async () => {
  const refused = await refusal("not json\n");
  const written = refuse(refused);

  assert.ok(written);
  assert.equal(written.id, null);
  assert.match(render(written), /^\{"jsonrpc":"2\.0","id":null,"error":\{"code":-32700,/);
  assert.equal(refuse(new Error("not a protocol failure")), undefined);
});

test("a writer renders one message per line, in the order it was handed them", async () => {
  const written: string[] = [];
  const writer = newWriter(async (line) => {
    written.push(line);
  });

  await Promise.all([
    writer.write(response(1, {})),
    writer.write(notification("host.log", { level: "info", message: "x", fields: {} })),
    writer.write(request(1, "host.info", {})),
  ]);

  assert.deepEqual(written, [
    '{"jsonrpc":"2.0","id":1,"result":{}}\n',
    '{"jsonrpc":"2.0","method":"host.log","params":{"level":"info","message":"x","fields":{}}}\n',
    '{"jsonrpc":"2.0","id":1,"method":"host.info","params":{}}\n',
  ]);

  assert.ok(
    written.every((line) => line.endsWith("\n")),
    "the sink is handed the whole line, terminator included",
  );
});

test("a writer refuses a message that would arrive as something other than one message", () => {
  assert.throws(
    () =>
      render({ jsonrpc: "2.0", id: 1, result: {}, error: { code: PARSE_ERROR, message: "x" } }),
    /result and an error/,
  );

  assert.throws(
    () => render({ jsonrpc: "2.0", id: 1, error: { code: PARSE_ERROR, message: "x" } }),
    /no data\.slug/,
  );
});

test("a writer keeps going after one message it could not render", async () => {
  const written: string[] = [];
  const writer = newWriter(async (line) => {
    written.push(line);
  });

  assert.throws(() => writer.write({ jsonrpc: "1.0", id: 1, result: {} }));
  await writer.write(response(2, {}));

  assert.deepEqual(written, ['{"jsonrpc":"2.0","id":2,"result":{}}\n']);
});
