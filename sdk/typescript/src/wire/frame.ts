// One message per line, under a bounded budget.
//
// docs/protocol.md §2.3 fixes that budget at 16 MiB and is explicit about when it is enforced: a
// line is refused **at** the budget, never after a delimiter that may never arrive. A reader that
// accumulates until it finds a newline has a bound on neither memory nor time, and a peer that
// opens a line and stops writing is indistinguishable from one that is merely slow.
//
// §2.6 is why this reads rather than recovers. A line that cannot be framed ends the session and the
// reader is spent afterwards, because the id is exactly the part that could not be read -- so
// skipping the line would leave whoever was waiting on that request waiting forever, with the
// migration lock held. One best-effort error carrying "id": null goes out first.
//
// node:readline cannot implement either rule. It bounds no line, and Buffer.toString replaces
// invalid UTF-8 with U+FFFD silently where TextDecoder with fatal:true throws.

import { PARSE_ERROR, ProtocolError } from "./code.ts";
import type { Message } from "./message.ts";
import { checkSlug, decode, encode, refusal, validate } from "./message.ts";

/** MAX_LINE is the longest line a reader accepts, counting the newline that terminates it. */
export const MAX_LINE = 16 << 20;

/** NEWLINE is the one byte that ends a line, since the line is the frame. */
const NEWLINE = 0x0a;

/** CARRIAGE_RETURN is what §2.2 accepts on input and never emits. */
const CARRIAGE_RETURN = 0x0d;

/** BOM is the byte-order mark §2.2 forbids on either stream, at any point. */
const BOM = Uint8Array.of(0xef, 0xbb, 0xbf);

/** Reader reads one message per line, and is spent the moment it meets a line it cannot frame. */
export interface Reader {
  /** Returns the next message, undefined at end of stream, or the failure that ended the session. */
  read(): Promise<Message | undefined>;
  /** Reports how many lines have been read, which is what a failure names. */
  line(): number;
}

/** newReader reads protocol messages from a stream of bytes, one per line. */
export function newReader(source: AsyncIterable<Uint8Array>): Reader {
  const chunks = source[Symbol.asyncIterator]();

  let held: Uint8Array[] = [];
  let scanned = 0;
  let ended = false;
  let spent: unknown;
  let lines = 0;

  const size = (): number => held.reduce((total, part) => total + part.length, 0);

  const take = (): Uint8Array | undefined => {
    while (scanned < held.length) {
      const part = held[scanned] as Uint8Array;
      const at = part.indexOf(NEWLINE);

      if (at < 0) {
        scanned += 1;

        continue;
      }

      const line = join(held.slice(0, scanned), part.subarray(0, at));
      const rest = part.subarray(at + 1);

      held = held.slice(scanned + 1);
      scanned = 0;

      if (rest.length > 0) {
        held.unshift(rest);
      }

      lines += 1;

      return line;
    }

    return undefined;
  };

  const fill = async (): Promise<boolean> => {
    const next = await chunks.next();

    if (next.done === true) {
      ended = true;

      return false;
    }

    held.push(next.value);

    return true;
  };

  const framed = async (): Promise<Uint8Array | undefined> => {
    for (;;) {
      const line = take();

      if (line !== undefined) {
        return line;
      }

      if (size() > MAX_LINE) {
        throw tooLong(lines + 1);
      }

      if (ended || !(await fill())) {
        return cutOff(lines);
      }
    }
  };

  const cutOff = (at: number): undefined => {
    const rest = join(held, Uint8Array.of());

    held = [];
    scanned = 0;

    if (rest.length === 0 || blank(rest)) {
      return undefined;
    }

    throw new ProtocolError(
      PARSE_ERROR,
      `line ${at + 1} ends without a newline, so it is a message that was cut off`,
    );
  };

  const message = (line: Uint8Array): Message => {
    if (opensWithBOM(line)) {
      throw new ProtocolError(
        PARSE_ERROR,
        `line ${lines} opens with a byte-order mark, which no stream here ever carries`,
      );
    }

    return decode(text(line, lines));
  };

  const next = async (): Promise<Message | undefined> => {
    for (;;) {
      const line = await framed();

      if (line === undefined) {
        return undefined;
      }

      const trimmed = trimEOL(line);

      if (trimmed.length === 0) {
        continue;
      }

      return message(trimmed);
    }
  };

  return {
    async read(): Promise<Message | undefined> {
      if (spent !== undefined) {
        throw spent;
      }

      try {
        return await next();
      } catch (thrown) {
        spent = attribute(thrown, lines);

        throw spent;
      }
    },
    line(): number {
      return lines;
    },
  };
}

/** Writer writes one message per line, flushed, so nothing waits in a buffer for the next one. */
export interface Writer {
  /** Renders one message, checks everything a sender owes, and writes its line. */
  write(m: Message): Promise<void>;
}

/** newWriter writes protocol messages through one sink, one per line and one at a time.
 *
 * The sink is handed the whole line, terminator included: §2.2 makes each message one JSON object
 * followed by one \n, and a writer that left the newline to its caller would put two messages on
 * one line the first time somebody forgot.
 */
export function newWriter(send: (line: string) => Promise<void>): Writer {
  let queued: Promise<void> = Promise.resolve();

  return {
    write(m: Message): Promise<void> {
      const line = `${render(m)}\n`;

      queued = queued.then(
        () => send(line),
        () => send(line),
      );

      return queued;
    },
  };
}

/** render checks a message against everything a sender owes and returns the bytes of its line. */
export function render(m: Message): string {
  validate(m);
  checkSlug(m);

  const line = encode(m);

  if (/[\n\r]/.test(line)) {
    throw new ProtocolError(
      PARSE_ERROR,
      "the encoded message carries a newline, which would arrive as two messages",
    );
  }

  if (Buffer.byteLength(line, "utf8") + 1 > MAX_LINE) {
    throw new ProtocolError(
      PARSE_ERROR,
      `the encoded message is past the ${MAX_LINE >> 20} MiB one line may be`,
    );
  }

  return line;
}

/** refuse builds the one null-id error §2.6 permits, or nothing when there is nobody left to tell. */
export function refuse(thrown: unknown): Message | undefined {
  return thrown instanceof ProtocolError ? refusal(thrown) : undefined;
}

/** tooLong refuses a line at the budget rather than after a delimiter that may never arrive. */
function tooLong(at: number): ProtocolError {
  return new ProtocolError(
    PARSE_ERROR,
    `line ${at} is longer than the ${MAX_LINE >> 20} MiB one message may be`,
  );
}

/** text decodes one line as UTF-8, refusing the bytes §2.2 rules out rather than replacing them. */
function text(line: Uint8Array, at: number): string {
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(line);
  } catch {
    throw new ProtocolError(PARSE_ERROR, `line ${at} is not valid UTF-8`);
  }
}

/** attribute names the line a failure was found on, keeping the code the decoder chose. */
function attribute(thrown: unknown, at: number): unknown {
  if (!(thrown instanceof ProtocolError) || thrown.message.startsWith("line ")) {
    return thrown;
  }

  return new ProtocolError(thrown.code, `line ${at}: ${thrown.message}`, thrown.data);
}

/** join concatenates the parts one line was read in, plus whatever of the last one it covers. */
function join(parts: readonly Uint8Array[], tail: Uint8Array): Uint8Array {
  return Buffer.concat([...parts, tail]);
}

/** trimEOL drops the terminator, accepting the carriage return §2.2 never emits. */
function trimEOL(line: Uint8Array): Uint8Array {
  return line.length > 0 && line[line.length - 1] === CARRIAGE_RETURN
    ? line.subarray(0, line.length - 1)
    : line;
}

/** blank reports whether what a stream ended with is whitespace rather than a message cut off. */
function blank(rest: Uint8Array): boolean {
  return rest.every(
    (byte) => byte === 0x20 || byte === 0x09 || byte === NEWLINE || byte === CARRIAGE_RETURN,
  );
}

/** opensWithBOM reports a line beginning with the byte-order mark no stream here carries. */
function opensWithBOM(line: Uint8Array): boolean {
  return line.length >= BOM.length && BOM.every((byte, at) => line[at] === byte);
}
