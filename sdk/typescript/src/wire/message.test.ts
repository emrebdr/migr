import assert from "node:assert/strict";
import { test } from "node:test";

import { INVALID_REQUEST, PARSE_ERROR, ProtocolError } from "./code.ts";
import {
  decode,
  encode,
  failure,
  isNotification,
  isRequest,
  isResponse,
  notification,
  refusal,
  request,
  response,
} from "./message.ts";
import { HOST_SCHEMA_APPLY } from "./method.ts";

// The envelope is closed and a payload is open, which is docs/protocol.md §2.6 and §10.2 together.
// An unknown member at the top level is -32600; one inside params, result, or data is ignored,
// because that is what makes adding a member to a payload a compatible change.
//
// And an id is three states. JSON.stringify writes them apart on its own -- it drops undefined and
// keeps null -- but reading them apart takes `in`, which is the trap this file exists to hold shut.

/** The two separators docs/ir.md §3.3 emits as UTF-8, by code point so this source stays readable. */
const LINE_SEPARATOR = String.fromCodePoint(0x2028);
const PARAGRAPH_SEPARATOR = String.fromCodePoint(0x2029);

/** refused decodes a line expected not to be a message, returning what refused it. */
function refused(line: string): ProtocolError {
  try {
    decode(line);
  } catch (thrown) {
    assert.ok(thrown instanceof ProtocolError, `refused with ${String(thrown)}`);

    return thrown;
  }

  return assert.fail(`${line} was read as a message`);
}

test("an id is a number, the one null, or absent, and each is told from the others", () => {
  assert.equal(
    encode(request(1, "host.info", {})),
    '{"jsonrpc":"2.0","id":1,"method":"host.info","params":{}}',
  );
  assert.equal(
    encode(notification("$/cancel", { id: 3 })),
    '{"jsonrpc":"2.0","method":"$/cancel","params":{"id":3}}',
  );
  assert.equal(
    encode(refusal(new ProtocolError(PARSE_ERROR, "line 4 is not JSON"))),
    '{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"line 4 is not JSON",' +
      '"data":{"slug":"parse_error"}}}',
  );
});

test("an absent id and a null id decode to different messages, which is what `in` reads", () => {
  const absent = decode('{"jsonrpc":"2.0","method":"host.log","params":{}}');
  const nulled = decode('{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"x"}}');

  assert.equal("id" in absent, false);
  assert.equal("id" in nulled, true);
  assert.equal(nulled.id, null);
  assert.equal(isNotification(absent), true);
  assert.equal(isResponse(nulled), true);
});

test("the three shapes are told apart by their method and their id", () => {
  assert.equal(isRequest(request(1, "host.info", {})), true);
  assert.equal(isNotification(notification("host.log", {})), true);
  assert.equal(isResponse(response(1, {})), true);
  assert.equal(isResponse(failure(1, new ProtocolError(PARSE_ERROR, "x"))), true);
  assert.equal(isRequest(notification("host.log", {})), false);
});

test("the envelope is closed, so an unknown member there is not a message", () => {
  const refusedFor = refused(
    '{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{},"extra":1}',
  );

  assert.equal(refusedFor.code, INVALID_REQUEST);
  assert.match(refusedFor.message, /"extra"/);
});

test("a payload is open, so an unknown member inside it crosses the wire untouched", () => {
  const line = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"later":true}}';

  assert.equal(encode(decode(line)), line);
});

test("a top-level array is a protocol error and never an implicit batch", () => {
  assert.equal(refused("[1,2]").code, INVALID_REQUEST);
  assert.match(refused("[]").message, /batch requests are not supported/);
});

test("a line that is not JSON is a parse error, where JSON that is not a message is not", () => {
  assert.equal(refused("not json").code, PARSE_ERROR);
  assert.equal(refused("").code, PARSE_ERROR);
  assert.equal(refused("3").code, INVALID_REQUEST);
  assert.equal(refused('"a string"').code, INVALID_REQUEST);
  assert.equal(refused("null").code, INVALID_REQUEST);
});

test("a message states the one version there is", () => {
  assert.match(refused('{"id":1,"method":"shutdown","params":{}}').message, /states none/);
  assert.match(
    refused('{"jsonrpc":"1.0","id":1,"method":"shutdown","params":{}}').message,
    /jsonrpc must be "2\.0"/,
  );
});

test("an id is an integer here, and not the strings or floats JSON-RPC allows", () => {
  assert.match(
    refused('{"jsonrpc":"2.0","id":"1","method":"shutdown","params":{}}').message,
    /integer/,
  );
  assert.match(
    refused('{"jsonrpc":"2.0","id":1.5,"method":"shutdown","params":{}}').message,
    /integer/,
  );
  assert.match(
    refused('{"jsonrpc":"2.0","id":true,"method":"shutdown","params":{}}').message,
    /integer/,
  );
});

test("only the two notifications may arrive without an id", () => {
  assert.doesNotThrow(() => decode('{"jsonrpc":"2.0","method":"$/cancel","params":{"id":1}}'));
  assert.doesNotThrow(() => decode('{"jsonrpc":"2.0","method":"host.log","params":{}}'));
  assert.match(
    refused('{"jsonrpc":"2.0","method":"shutdown","params":{}}').message,
    /only \$\/cancel and host\.log may be sent as a notification/,
  );
});

test("a call carrying a null id is refused, since only the framing error carries one", () => {
  assert.match(
    refused('{"jsonrpc":"2.0","id":null,"method":"shutdown","params":{}}').message,
    /only the one framing error carries/,
  );
});

test("a call carrying a result or an error is not a call", () => {
  assert.match(
    refused('{"jsonrpc":"2.0","id":1,"method":"shutdown","result":{}}').message,
    /only a response does/,
  );
});

test("a response carries an id and one of a result and an error", () => {
  assert.match(refused('{"jsonrpc":"2.0","result":{}}').message, /no method and no id/);
  assert.match(
    refused('{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-1,"message":"x"}}').message,
    /a result and an error/,
  );
  assert.match(refused('{"jsonrpc":"2.0","id":1}').message, /neither a result nor an error/);
  assert.match(
    refused('{"jsonrpc":"2.0","id":null,"result":{}}').message,
    /may only carry an error/,
  );
});

test("an error object emits its members in the order §7.1 shows them", () => {
  assert.equal(
    encode(
      failure(
        3,
        new ProtocolError(-32013, "this migration declares that it cannot be undone", {
          reason: "gone",
        }),
      ),
    ),
    '{"jsonrpc":"2.0","id":3,"error":{"code":-32013,"message":' +
      '"this migration declares that it cannot be undone",' +
      '"data":{"slug":"declared_irreversible","reason":"gone"}}}',
  );
});

test("a method is a string, and a message carrying another is not one", () => {
  assert.match(
    refused('{"jsonrpc":"2.0","id":1,"method":3,"params":{}}').message,
    /method is a string/,
  );
});

test("the two separators docs/ir.md §3.3 emits as UTF-8 reach the line unescaped", () => {
  const query = `SELECT id${LINE_SEPARATOR}FROM users${PARAGRAPH_SEPARATOR}WHERE active`;
  const line = encode(
    request(1, HOST_SCHEMA_APPLY, {
      operations: [{ op: "create_view", view: "active_users", query }],
    }),
  );

  assert.ok(line.includes(query), `the envelope escaped a separator: ${line}`);
  assert.ok(
    !line.includes("\\u202"),
    "the operations array is the one payload whose bytes are a compatibility surface " +
      "(docs/ir.md §4.2), and this is the encoder that writes them -- operation.ts's own " +
      "encode is never the code path that runs",
  );
});
