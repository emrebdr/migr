// The host a capture stands behind, whose answers are a copy of the engine's rather than an invention.
//
// Every answer here is the one internal/runtime/host gives for the same callback, which is what makes
// a capture a statement about a real run. The one callback that differs between the modes differs
// here too: host.db.exec is held under plan and answered as performed under apply, and in neither
// case did a statement reach a database -- there is none.
//
// Where it deliberately refuses rather than answers: a read, a capability, or a host.info this stage
// did not answer. A migration may branch on any of the three, and an empty value answered in place of
// a missing one sends it down a branch the test never meant to ask about, which is the failure a
// capture exists to catch rather than one it should manufacture.

import type { Host, Info, Kind, Mode, Rows, Verdict } from "../index.ts";
import {
  HOST_CAPABILITY_GET,
  HOST_DB_EXEC,
  HOST_DB_QUERY,
  HOST_INFO,
  HOST_LOG,
  HOST_SCHEMA_APPLY,
} from "../wire/method.ts";

/** Document is one operation as the engine receives it, which is the IR and nothing around it. */
export type Document = Record<string, unknown>;

/** Statement is one statement a migration sent itself, with whatever it bound to it. */
export interface Statement {
  /** The statement as the migration wrote it, never interpolated. */
  sql: string;
  /** The values bound to it, in the order they were bound. */
  args: string[];
}

/** Line is one structured line a migration filed on the engine's own logger. */
export interface Line {
  /** The severity the migration chose. */
  level: string;
  /** The line itself. */
  message: string;
  /** Whatever the migration attached to it. */
  fields: Record<string, string>;
}

/** Stage is what a capture answers a migration's own questions with, and the mode it runs it in. */
export interface Stage {
  /** Whether the half is run or asked, and apply unless a test says otherwise. */
  mode?: Mode;
  /** The version the window reports, and a placeholder unless a test names one. */
  migration?: string;
  /** What each statement the migration reads with is answered with, keyed by its SQL. */
  reads?: Record<string, Rows>;
  /** What a statement the migration sends itself reports changing, keyed by its SQL. */
  affected?: Record<string, string>;
  /** What this run is against, refused rather than guessed when it is absent. */
  info?: Info;
  /** What the engine makes of an operation, keyed by the kind asked about. */
  verdicts?: Partial<Record<Kind, Verdict>>;
}

/** UnstagedError is a question a migration asked that the stage it ran against does not answer. */
export class UnstagedError extends Error {
  /** name is what a test matches on, since this is the one refusal a capture manufactures. */
  override readonly name = "UnstagedError";

  /** Builds the refusal, saying what was asked and where to stage the answer. */
  constructor(message: string) {
    super(`this capture staged no answer ${message}`);
  }
}

/** Recording is the host a capture stands behind, and everything it was asked. */
export interface Recording extends Host {
  /** The IR documents this host recorded, in the order they were sent. */
  readonly operations: Document[];
  /** The statements the migration read with, in order. */
  readonly reads: Statement[];
  /** The statements the migration sent itself, which no rollback undoes. */
  readonly statements: Statement[];
  /** The lines it filed on the engine's logger. */
  readonly logs: Line[];
  /** The first answer this host would not give, whatever the migration made of it. */
  refusal(): unknown;
}

/** recording builds the host one captured half runs against. */
export function recording(stage: Stage, mode: Mode): Recording {
  const operations: Document[] = [];
  const reads: Statement[] = [];
  const statements: Statement[] = [];
  const logs: Line[] = [];

  let refused: unknown;

  const refuse = (thrown: unknown): never => {
    refused ??= thrown;

    throw thrown;
  };

  const apply = (params: unknown): unknown => {
    const sent = (params as { operations: Document[] }).operations;

    operations.push(...sent);

    return { recorded: sent.length };
  };

  const query = (params: unknown): unknown => {
    const asked = params as Statement;

    reads.push(asked);

    const answer = stage.reads?.[asked.sql];

    if (answer === undefined) {
      return refuse(
        new UnstagedError(
          `for the read ${JSON.stringify(asked.sql)}: stage it in reads, so this migration ` +
            `branches on what the test meant rather than on an empty result`,
        ),
      );
    }

    return answer;
  };

  const exec = (params: unknown): unknown => {
    const sent = params as Statement;

    statements.push(sent);

    if (mode === "plan") {
      return { rowsAffected: "0", executed: false };
    }

    return { rowsAffected: stage.affected?.[sent.sql] ?? "0", executed: true };
  };

  const capability = (params: unknown): unknown => {
    const kind = (params as { operation: Document }).operation["op"] as Kind;
    const verdict = stage.verdicts?.[kind];

    if (verdict === undefined) {
      return refuse(
        new UnstagedError(
          `for a capability question about ${kind}: stage it in verdicts, since only a ` +
            `connected engine knows the answer`,
        ),
      );
    }

    return verdict;
  };

  const info = (): unknown => {
    if (stage.info === undefined) {
      return refuse(
        new UnstagedError(
          "for host.info: stage it in info, since a migration that branches on the driver or " +
            "the server version would otherwise read empty strings",
        ),
      );
    }

    return { ...stage.info, mode };
  };

  return {
    operations,
    reads,
    statements,
    logs,
    refusal: () => refused,
    call: async <R>(method: string, params: unknown): Promise<R> => {
      switch (method) {
        case HOST_SCHEMA_APPLY:
          return apply(params) as R;
        case HOST_DB_QUERY:
          return query(params) as R;
        case HOST_DB_EXEC:
          return exec(params) as R;
        case HOST_CAPABILITY_GET:
          return capability(params) as R;
        case HOST_INFO:
          return info() as R;
        default:
          throw new Error(`${method} is not a callback this protocol version defines`);
      }
    },
    notify: (method: string, params: unknown): void => {
      if (method !== HOST_LOG) {
        refused ??= new Error(`${method} is not a notification this protocol version defines`);

        return;
      }

      logs.push(params as Line);
    },
  };
}
