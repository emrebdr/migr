// One conversation with an engine: the two streams, and everything in flight on them.
//
// There is one read loop, and that is what keeps $/cancel readable while work is running. A session
// that served migration.execute inline would leave the notification sitting behind the very work it
// was sent to stop, so the two long methods are dispatched to a task of their own and answered when
// they finish.
//
// There are two tables and never one. `pending` is this side's own callbacks, keyed by this side's
// own sequence; `running` is the engine's requests this side is working on, each with its own
// AbortController. docs/protocol.md §2.5 is explicit that an id is unique only within its own
// direction, and that one table for both mismatches responses.
//
// The runtime holds none of §8.2's timers. Every one of them is a timer the engine keeps and a kill
// the engine performs, and migration.execute deliberately has none at all -- a backfill over a large
// table legitimately runs for hours.

import { check, undone } from "./migration.ts";
import type { Loaded, Registry } from "./load.ts";
import type { Migration } from "./migration.ts";
import type { Schema, Window } from "./schema.ts";
import type { Host } from "./schema.ts";
import { newSchema } from "./schema.ts";
import { LANGUAGE, SDK_NAME, VERSION, languageRuntime } from "./version.ts";
import type { Code } from "./wire/code.ts";
import {
  CANCELED,
  DECLARED_IRREVERSIBLE,
  INTERNAL_ERROR,
  METHOD_NOT_FOUND,
  MIGRATION_FAILED,
  NO_DOWN_AUTHORED,
  ProtocolError,
  SESSION_STATE,
  UNKNOWN_MIGRATION,
  UNSUPPORTED_IR,
  UNSUPPORTED_PROTOCOL,
} from "./wire/code.ts";
import { newReader, newWriter, refuse } from "./wire/frame.ts";
import type { Message } from "./wire/message.ts";
import {
  failure,
  isNotification,
  isResponse,
  notification,
  request,
  response,
} from "./wire/message.ts";
import {
  CANCEL,
  INITIALIZE,
  IR_VERSION,
  MIGRATION_DISCOVER,
  MIGRATION_EXECUTE,
  MIGRATION_VALIDATE,
  PROTOCOL_VERSION,
  SHUTDOWN,
} from "./wire/method.ts";
import type {
  Direction,
  Discovered,
  ExecuteResult,
  InitializeParams,
  WirePhase,
} from "./wire/payload.ts";
import { readCancel, readExecute, readInitialize, readValidate } from "./wire/payload.ts";
import { dependencies, online, phase } from "./migration.ts";

/** EXIT_OK is what a runtime exits with after answering shutdown, and after losing its engine. */
export const EXIT_OK = 0;

/** EXIT_FAILED is what a runtime exits with when the channel broke under it. */
export const EXIT_FAILED = 1;

/** Options is what one session is opened over: where its messages go, and what it can run. */
export interface Options {
  /** Loads this project's migrations, given what initialize said about the project. */
  load(project: InitializeParams): Promise<Registry>;
  /** Where protocol messages arrive, defaulting to this process's own stdin. */
  input?: AsyncIterable<Uint8Array>;
  /** Where protocol messages go, defaulting to this process's own stdout. */
  send?: (line: string) => Promise<void>;
}

/** run serves this program as a migr runtime until the engine is done with it. */
export async function run(options: Options): Promise<number> {
  const send = options.send ?? guard();
  const input = options.input ?? process.stdin;

  return serve(options, input, send);
}

/** guard points this process's default print target at stderr and keeps stdout for the protocol. */
export function guard(): (line: string) => Promise<void> {
  const stdout = process.stdout;
  const protocol = stdout.write.bind(stdout);

  stdout.write = process.stderr.write.bind(process.stderr) as typeof stdout.write;

  return (line: string) =>
    new Promise<void>((resolve, reject) => {
      protocol(line, (failed) => {
        if (failed) {
          reject(failed);

          return;
        }

        resolve();
      });
    });
}

/** State is how far through the lifecycle §3.1 draws the session has got.
 *
 * There is no third state for after shutdown. The loop ends when shutdown is answered and the
 * process exits 0 (§9), so "nothing more is answered" is the loop's shape rather than a branch
 * nothing can reach -- a session that kept reading would need one.
 */
type State = "new" | "ready";

/** serve reads until the engine says shutdown, closes its stdin, or sends a line it cannot frame. */
async function serve(
  options: Options,
  input: AsyncIterable<Uint8Array>,
  send: (line: string) => Promise<void>,
): Promise<number> {
  const reader = newReader(input);
  const writer = newWriter(send);
  const pending = new Map<number, (m: Message) => void>();
  const running = new Map<number, AbortController>();
  const working = new Set<Promise<void>>();

  let state: State = "new";
  let project: InitializeParams | undefined;
  let loaded: Promise<Registry> | undefined;
  let sequence = 0;

  const answer = async (m: Message): Promise<void> => {
    await writer.write(m);
  };

  const host: Host = {
    call: <R>(method: string, params: unknown, signal: AbortSignal): Promise<R> =>
      callback<R>(method, params, signal),
    notify: (method: string, params: unknown): void => {
      void writer.write(notification(method, params)).catch(() => undefined);
    },
  };

  const callback = async <R>(
    method: string,
    params: unknown,
    signal: AbortSignal,
  ): Promise<R> => {
    signal.throwIfAborted();

    sequence += 1;

    const id = sequence;
    const answered = new Promise<Message>((resolve) => {
      pending.set(id, resolve);
    });

    let abandon: (reason: unknown) => void = () => undefined;

    const abandoned = new Promise<never>((_, reject) => {
      abandon = reject;
    });

    const stop = (): void => {
      abandon(canceled(method));
    };

    void abandoned.catch(() => undefined);
    signal.addEventListener("abort", stop, { once: true });

    try {
      await writer.write(request(id, method, params));

      return payload<R>(await Promise.race([answered, abandoned]));
    } finally {
      signal.removeEventListener("abort", stop);
      pending.delete(id);
    }
  };

  const payload = <R>(m: Message): R => {
    if (m.error !== undefined) {
      throw raised(m.error.code, m.error.message, m.error.data);
    }

    return m.result as R;
  };

  const deliver = (m: Message): void => {
    const waiting = typeof m.id === "number" ? pending.get(m.id) : undefined;

    if (waiting !== undefined) {
      waiting(m);
    }
  };

  const notified = (m: Message): void => {
    if (m.method !== CANCEL) {
      return;
    }

    const named = readCancel(m.params);

    running.get(named?.id ?? -1)?.abort();
  };

  const migrations = async (): Promise<Registry> => {
    if (project === undefined) {
      throw new ProtocolError(INTERNAL_ERROR, "no project was opened by the handshake");
    }

    loaded ??= options.load(project);

    try {
      return await loaded;
    } catch (thrown) {
      throw unloadable(thrown);
    }
  };

  const initialize = async (id: number, params: unknown): Promise<Message> => {
    const offered = readInitialize(params);

    if (!offered.protocolVersions.includes(PROTOCOL_VERSION)) {
      throw new ProtocolError(
        UNSUPPORTED_PROTOCOL,
        `this SDK speaks protocol version ${PROTOCOL_VERSION}; the engine offered ` +
          offered.protocolVersions.join(", "),
        { supported: [PROTOCOL_VERSION] },
      );
    }

    if (!Number.isInteger(offered.irVersion) || offered.irVersion < 1) {
      throw new ProtocolError(
        UNSUPPORTED_IR,
        `the engine stated IR version ${offered.irVersion}, and no version below 1 exists`,
      );
    }

    project = offered;
    state = "ready";

    return response(id, {
      protocolVersion: PROTOCOL_VERSION,
      runtime: { name: LANGUAGE, version: languageRuntime() },
      sdk: { name: SDK_NAME, version: VERSION },
    });
  };

  const discover = async (id: number): Promise<Message> => {
    const registry = await migrations();

    return response(id, { migrations: listed(registry) });
  };

  const execute = async (params: unknown, signal: AbortSignal): Promise<ExecuteResult> => {
    const asked = readExecute(params);
    const entry = await found(asked.id);
    const window = newSchema(
      {
        migration: asked.id,
        direction: asked.direction,
        mode: asked.mode,
        signal,
        irVersion: project?.irVersion ?? IR_VERSION,
      },
      host,
    );

    return { operations: await performed(chosen(entry, asked.direction), window) };
  };

  const validate = async (params: unknown): Promise<Record<string, never>> => {
    const entry = await found(readValidate(params).id);

    try {
      await check(entry.migration);
    } catch (thrown) {
      throw new ProtocolError(MIGRATION_FAILED, `${entry.file}: ${describe(thrown)}`);
    }

    return {};
  };

  const found = async (version: string): Promise<Loaded> => {
    const entry = (await migrations()).lookup(version);

    if (entry === undefined) {
      throw new ProtocolError(
        UNKNOWN_MIGRATION,
        `no migration is registered under that version: ${version}`,
      );
    }

    return entry;
  };

  const perform = async (
    id: number,
    method: string,
    params: unknown,
    signal: AbortSignal,
  ): Promise<Message> => {
    try {
      const result =
        method === MIGRATION_VALIDATE ? await validate(params) : await execute(params, signal);

      return signal.aborted ? failure(id, canceled(method)) : response(id, result);
    } catch (thrown) {
      return failure(id, signal.aborted ? canceled(method) : failed(thrown));
    }
  };

  const background = (id: number, method: string, params: unknown): void => {
    const controller = new AbortController();

    running.set(id, controller);

    const task = (async (): Promise<void> => {
      try {
        await answer(await perform(id, method, params, controller.signal));
      } finally {
        running.delete(id);
      }
    })();

    working.add(task);
    void task.catch(() => undefined).finally(() => working.delete(task));
  };

  const admit = (method: string): void => {
    if (state === "new" && method !== INITIALIZE) {
      throw new ProtocolError(
        SESSION_STATE,
        `${JSON.stringify(method)} arrived before initialize, ` +
          "which is the first message on the channel",
      );
    }

    if (state === "ready" && method === INITIALIZE) {
      throw new ProtocolError(
        SESSION_STATE,
        "initialize arrived twice, and this session is already open",
      );
    }
  };

  const dispatch = async (m: Message): Promise<boolean> => {
    const id = m.id as number;
    const method = m.method as string;

    try {
      admit(method);
    } catch (thrown) {
      await answer(failure(id, failed(thrown)));

      return false;
    }

    if (method === SHUTDOWN) {
      await answer(response(id, {}));

      return true;
    }

    await served(id, method, m.params);

    return false;
  };

  const served = async (id: number, method: string, params: unknown): Promise<void> => {
    if (method === MIGRATION_EXECUTE || method === MIGRATION_VALIDATE) {
      background(id, method, params);

      return;
    }

    try {
      await answer(method === INITIALIZE ? await initialize(id, params) : await discover(id));
    } catch (thrown) {
      await answer(failure(id, failed(thrown)));
    }
  };

  const route = async (m: Message): Promise<boolean> => {
    if (isResponse(m)) {
      deliver(m);

      return false;
    }

    if (isNotification(m)) {
      notified(m);

      return false;
    }

    if (!knows(m.method as string)) {
      await answer(
        failure(
          m.id as number,
          new ProtocolError(
            METHOD_NOT_FOUND,
            `${JSON.stringify(m.method)} is not a method this runtime serves`,
          ),
        ),
      );

      return false;
    }

    return dispatch(m);
  };

  const ended = async (thrown: unknown): Promise<number> => {
    const refusal = refuse(thrown);

    if (refusal !== undefined) {
      await writer.write(refusal).catch(() => undefined);
    }

    return EXIT_FAILED;
  };

  const loop = async (): Promise<number> => {
    for (;;) {
      let m: Message | undefined;

      try {
        m = await reader.read();
      } catch (thrown) {
        return ended(thrown);
      }

      if (m === undefined) {
        return EXIT_OK;
      }

      if (await route(m)) {
        return EXIT_OK;
      }
    }
  };

  const code = await loop();

  for (const controller of running.values()) {
    controller.abort();
  }

  await Promise.allSettled([...working]);

  return code;
}

/** listed reports what a registry read, or everything wrong with the directory it was read from. */
function listed(registry: Registry): Discovered[] {
  try {
    return registry.all().map((entry) => ({
      version: entry.version,
      file: entry.file,
      dependencies: [...dependencies(entry.migration)],
      ...wirePhase(entry.migration),
      ...wireOnline(entry.migration),
    }));
  } catch (thrown) {
    throw unloadable(thrown);
  }
}

/** wirePhase renders the step a migration declares itself as the wire carries it, and nothing where it declares none. */
function wirePhase(m: Migration): { phase?: WirePhase } {
  const declared = phase(m);

  if (declared === undefined) {
    return {};
  }

  return {
    phase: {
      change: declared.change,
      name: declared.name,
      ...(declared.after === undefined || declared.after === "" ? {} : { after: declared.after }),
    },
  };
}

/** wireOnline renders how a migration runs online as the wire carries it, and nothing where it declares none. */
function wireOnline(m: Migration): { online?: string } {
  const declared = online(m);

  return declared === undefined ? {} : { online: declared };
}

/** unloadable reports migrations that could not be read, with the code sdk/go's discover uses. */
function unloadable(thrown: unknown): ProtocolError {
  return thrown instanceof ProtocolError
    ? thrown
    : new ProtocolError(INTERNAL_ERROR, describe(thrown));
}

/** performed runs one direction of a migration, then seals the window it acted through. */
async function performed(
  direction: (s: Schema) => void | Promise<void>,
  window: Window,
): Promise<number> {
  let raisedBy: unknown;

  try {
    await direction(window);
  } catch (thrown) {
    raisedBy = thrown;
  }

  let emitted = 0;

  try {
    emitted = await window.seal();
  } catch (thrown) {
    raisedBy ??= thrown;
  }

  if (raisedBy !== undefined) {
    throw raisedBy;
  }

  return emitted;
}

/** chosen returns what runs a migration one way, or §7.4's refusal for a down it has none of. */
function chosen(entry: Loaded, direction: Direction): (s: Schema) => void | Promise<void> {
  const m: Migration = entry.migration;

  if (direction === "up") {
    return (s) => m.up(s);
  }

  const { undo, reason } = undone(m);

  if (undo === "declared") {
    throw new ProtocolError(
      DECLARED_IRREVERSIBLE,
      "this migration declares that it cannot be undone",
      { reason },
    );
  }

  const down = m.down;

  if (undo === "derived" || down === undefined) {
    throw new ProtocolError(NO_DOWN_AUTHORED, `${entry.file} authors no down of its own`);
  }

  return (s) => down(s);
}

/** knows reports whether a method is one this protocol version defines for a runtime to serve. */
function knows(method: string): boolean {
  return (
    method === INITIALIZE ||
    method === MIGRATION_DISCOVER ||
    method === MIGRATION_EXECUTE ||
    method === MIGRATION_VALIDATE ||
    method === SHUTDOWN
  );
}

/** canceled is what a request abandoned because the other side asked is answered with. */
function canceled(method: string): ProtocolError {
  return new ProtocolError(CANCELED, `${method} was abandoned because the engine asked`);
}

/** failed renders whatever a handler raised as the error its response carries. */
function failed(thrown: unknown): ProtocolError {
  return thrown instanceof ProtocolError
    ? thrown
    : new ProtocolError(MIGRATION_FAILED, describe(thrown));
}

/** raised turns an error the host answered with into one a migration can branch on. */
function raised(code: Code, message: string, data: unknown): ProtocolError {
  return new ProtocolError(
    code,
    message,
    typeof data === "object" && data !== null ? (data as Record<string, unknown>) : undefined,
  );
}

/** describe renders what was thrown, since JavaScript permits throwing anything. */
function describe(thrown: unknown): string {
  return thrown instanceof Error ? thrown.message : String(thrown);
}
