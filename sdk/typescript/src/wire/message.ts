// The envelope: one JSON-RPC 2.0 message, and the bytes it crosses the wire as.
//
// docs/protocol.md fixes the member order of what is emitted, and §2.6 makes the top level closed:
// an unknown member there is -32600, while an unknown member inside params, result, or data is
// ignored. That is §10.2's compatibility rule made structural, and it is what keeps the operations
// array inside host.schema.apply byte-identical to the array the checksum covers.
//
// So nothing here passes a parsed object through. decode reads the six members it knows and refuses
// a seventh; encode rebuilds the envelope in the documented order and carries the three payloads
// exactly as they arrived. A codec that re-stringified what it parsed would round-trip every
// documented example while testing nothing about the order it is supposed to emit.
//
// An id is three states, not two: a number, the one null §2.6 permits for a line that could not be
// framed, and absent on the two notifications §2.4 names. JSON.stringify drops undefined and keeps
// null, so writing them apart falls out -- reading them apart does not, and `in` is what does it.

import type { Code } from "./code.ts";
import { INVALID_REQUEST, PARSE_ERROR, ProtocolError } from "./code.ts";
import { describeNotifications, notifies } from "./method.ts";

/** JSONRPC is the only value the jsonrpc member ever carries, and both sides check it. */
export const JSONRPC = "2.0";

/** MEMBERS are the envelope's members, in the order every documented message emits them. */
const MEMBERS = ["jsonrpc", "id", "method", "params", "result", "error"] as const;

/** FAILURE_MEMBERS are an error object's members, in the order §7.1 shows them. */
const FAILURE_MEMBERS = ["code", "message", "data"] as const;

/** Failure is a JSON-RPC error object, whose data always carries the slug §7.1 requires. */
export interface Failure {
  /** The code §7 assigns. */
  code: Code;
  /** One line, in the terms whoever reads it can act on. */
  message: string;
  /** An object carrying at least `slug`, which is what the other side branches on. */
  data?: unknown;
}

/** Message is one JSON-RPC 2.0 message: a request, a notification, or a response. */
export interface Message {
  /** Always "2.0", and a message carrying anything else is not one. */
  jsonrpc: string;
  /** The number its sender waits on, the one null §2.6 permits, or absent on a notification. */
  id?: number | null;
  /** The method called, absent on a response. */
  method?: string;
  /** The call's parameters, carried exactly as they arrived. */
  params?: unknown;
  /** A successful answer's payload, carried exactly as it arrived. */
  result?: unknown;
  /** A failed answer's error object. */
  error?: Failure;
}

/** request builds a call its sender waits on: a method, an id, and its parameters. */
export function request(id: number, method: string, params: unknown): Message {
  return { jsonrpc: JSONRPC, id, method, params };
}

/** notification builds a call nothing answers, which only §2.4's two methods may be. */
export function notification(method: string, params: unknown): Message {
  return { jsonrpc: JSONRPC, method, params };
}

/** response builds the successful answer to a request. */
export function response(id: number, result: unknown): Message {
  return { jsonrpc: JSONRPC, id, result };
}

/** failure builds the failed answer to a request. */
export function failure(id: number, error: ProtocolError): Message {
  return { jsonrpc: JSONRPC, id, error: failureOf(error) };
}

/** refusal builds the one null-id error §2.6 permits, for a line that could not be framed. */
export function refusal(error: ProtocolError): Message {
  return { jsonrpc: JSONRPC, id: null, error: failureOf(error) };
}

/** failureOf renders one raised failure as the error object a response carries. */
function failureOf(error: ProtocolError): Failure {
  return { code: error.code, message: error.message, data: error.data };
}

/** isRequest reports whether a message is a call its sender is waiting on an answer to. */
export function isRequest(m: Message): boolean {
  return m.method !== undefined && typeof m.id === "number";
}

/** isNotification reports whether a message is a call that is never answered. */
export function isNotification(m: Message): boolean {
  return m.method !== undefined && !("id" in m);
}

/** isResponse reports whether a message answers a request. */
export function isResponse(m: Message): boolean {
  return m.method === undefined;
}

/** encode renders a message as the bytes of its line, without the newline that terminates it. */
export function encode(m: Message): string {
  const built: Record<string, unknown> = {};

  for (const member of MEMBERS) {
    const held = m[member];

    if (member in m && held !== undefined) {
      built[member] = member === "error" ? ordered(held as Failure) : held;
    }
  }

  return JSON.stringify(built);
}

/** ordered rebuilds an error object in the order §7.1 shows its members. */
function ordered(error: Failure): Record<string, unknown> {
  const built: Record<string, unknown> = {};

  for (const member of FAILURE_MEMBERS) {
    const held = error[member];

    if (member in error && held !== undefined) {
      built[member] = held;
    }
  }

  return built;
}

/** decode reads one line's text into a message, refusing everything that is not one. */
export function decode(text: string): Message {
  const parsed = parse(text);

  if (Array.isArray(parsed)) {
    throw new ProtocolError(
      INVALID_REQUEST,
      "a top-level array is not a message, and batch requests are not supported",
    );
  }

  if (typeof parsed !== "object" || parsed === null) {
    throw new ProtocolError(INVALID_REQUEST, "the line is JSON and is not a message");
  }

  const m = read(parsed as Record<string, unknown>);

  validate(m);

  return m;
}

/** parse reads a line as JSON, which is the one failure §2.6 calls a parse error. */
function parse(text: string): unknown {
  if (text.trim() === "") {
    throw new ProtocolError(PARSE_ERROR, "the line carries no message");
  }

  try {
    return JSON.parse(text);
  } catch (thrown) {
    throw new ProtocolError(
      PARSE_ERROR,
      `the line is not JSON: ${thrown instanceof Error ? thrown.message : String(thrown)}`,
    );
  }
}

/** read takes the six members the envelope defines, refusing a seventh (§2.6). */
function read(envelope: Record<string, unknown>): Message {
  for (const member of Object.keys(envelope)) {
    if (!(MEMBERS as readonly string[]).includes(member)) {
      throw new ProtocolError(
        INVALID_REQUEST,
        `the envelope carries ${JSON.stringify(member)}, which is not a member of a message`,
      );
    }
  }

  const stated = envelope["jsonrpc"];

  if (typeof stated !== "string") {
    throw new ProtocolError(
      INVALID_REQUEST,
      `a message states jsonrpc ${JSON.stringify(JSONRPC)}, and this line states none`,
    );
  }

  const m: Message = { jsonrpc: stated };

  if ("id" in envelope) {
    m.id = identifier(envelope["id"]);
  }

  if ("method" in envelope) {
    m.method = named(envelope["method"]);
  }

  if ("params" in envelope) {
    m.params = envelope["params"];
  }

  if ("result" in envelope) {
    m.result = envelope["result"];
  }

  if ("error" in envelope) {
    m.error = envelope["error"] as Failure;
  }

  return m;
}

/** named reads a method, which is a string and is one of the reasons a line is not a message. */
function named(held: unknown): string {
  if (typeof held !== "string") {
    throw new ProtocolError(
      INVALID_REQUEST,
      "method is a string, and this message carries another",
    );
  }

  return held;
}

/** identifier reads an id, refusing the strings and floats JSON-RPC allows and this does not. */
function identifier(held: unknown): number | null {
  if (held === null) {
    return null;
  }

  if (typeof held !== "number" || !Number.isInteger(held)) {
    throw new ProtocolError(
      INVALID_REQUEST,
      `id is an integer here, and this message carries ${JSON.stringify(held)}`,
    );
  }

  return held;
}

/** validate reports whether a message is one of the three shapes this protocol defines. */
export function validate(m: Message): void {
  if (m.jsonrpc !== JSONRPC) {
    throw new ProtocolError(
      INVALID_REQUEST,
      `jsonrpc must be ${JSON.stringify(JSONRPC)}, and this message carries ` +
        JSON.stringify(m.jsonrpc),
    );
  }

  if (m.method !== undefined) {
    validateCall(m, m.method);

    return;
  }

  validateAnswer(m);
}

/** validateCall checks a request or a notification, which are told apart by their id alone. */
function validateCall(m: Message, method: string): void {
  if ("result" in m || "error" in m) {
    throw new ProtocolError(
      INVALID_REQUEST,
      `${JSON.stringify(method)} carries a result or an error, which only a response does`,
    );
  }

  if (m.id === null) {
    throw new ProtocolError(
      INVALID_REQUEST,
      `${JSON.stringify(method)} carries a null id, which only the one framing error carries`,
    );
  }

  if (!("id" in m) && !notifies(method)) {
    throw new ProtocolError(
      INVALID_REQUEST,
      `${JSON.stringify(method)} has no id, and only ${describeNotifications()} ` +
        "may be sent as a notification",
    );
  }
}

/** validateAnswer checks a response, which carries an id and one of a result and an error. */
function validateAnswer(m: Message): void {
  if (!("id" in m)) {
    throw new ProtocolError(
      INVALID_REQUEST,
      "a message with no method and no id is not a request, a notification, or a response",
    );
  }

  if ("result" in m && "error" in m) {
    throw new ProtocolError(
      INVALID_REQUEST,
      `the response to id ${m.id} carries a result and an error, and it carries one or the other`,
    );
  }

  if (!("result" in m) && !("error" in m)) {
    throw new ProtocolError(
      INVALID_REQUEST,
      `the response to id ${m.id} carries neither a result nor an error`,
    );
  }

  if (m.id === null && m.error === undefined) {
    throw new ProtocolError(
      INVALID_REQUEST,
      "a null id answers no request, so it may only carry an error",
    );
  }
}

/** checkSlug reports an error this side is about to write carrying no slug, which §7.1 requires. */
export function checkSlug(m: Message): void {
  if (m.error === undefined) {
    return;
  }

  const data = m.error.data;
  const slug =
    typeof data === "object" && data !== null
      ? (data as Record<string, unknown>)["slug"]
      : undefined;

  if (typeof slug !== "string" || slug === "") {
    throw new ProtocolError(
      INVALID_REQUEST,
      `error ${m.error.code} carries no data.slug, which docs/protocol.md §7.1 requires`,
    );
  }
}
