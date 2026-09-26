// The error codes docs/protocol.md §7 assigns, and the slug every one of them carries.
//
// §7.1 admits no exemption: an error carries `data.slug`, the reserved five included, because the
// slug is what the other side branches on and a number is what an implementer writes down wrong.
// The slugs here are held to the document's own tables, both ways, by protocol.test.ts.

/** Code is one of the error codes docs/protocol.md §7 assigns. */
export type Code = (typeof CODES)[number];

/** The five codes JSON-RPC 2.0 defines for itself, used exactly as it defines them (§7.2). */
export const PARSE_ERROR = -32700;
export const INVALID_REQUEST = -32600;
export const METHOD_NOT_FOUND = -32601;
export const INVALID_PARAMS = -32602;
export const INTERNAL_ERROR = -32603;

/** The codes a runtime reports to the engine (§7.3). */
export const UNSUPPORTED_PROTOCOL = -32001;
export const UNSUPPORTED_IR = -32002;
export const SESSION_STATE = -32003;
export const UNKNOWN_MIGRATION = -32010;
export const MIGRATION_FAILED = -32011;
export const NO_DOWN_AUTHORED = -32012;
export const DECLARED_IRREVERSIBLE = -32013;
export const IRREVERSIBLE_OPERATION = -32014;

/** The codes the host reports to a runtime (§7.3). */
export const NOT_READ_ONLY = -32020;
export const INVALID_OPERATION = -32021;
export const DATABASE = -32022;

/** CANCELED marks a request abandoned because the other side asked, and either may send it. */
export const CANCELED = -32030;

/** SLUGS names each code in the form §7.1 requires every error carry. */
export const SLUGS = {
  [PARSE_ERROR]: "parse_error",
  [INVALID_REQUEST]: "invalid_request",
  [METHOD_NOT_FOUND]: "method_not_found",
  [INVALID_PARAMS]: "invalid_params",
  [INTERNAL_ERROR]: "internal_error",
  [UNSUPPORTED_PROTOCOL]: "unsupported_protocol",
  [UNSUPPORTED_IR]: "unsupported_ir",
  [SESSION_STATE]: "session_state",
  [UNKNOWN_MIGRATION]: "unknown_migration",
  [MIGRATION_FAILED]: "migration_failed",
  [NO_DOWN_AUTHORED]: "no_down_authored",
  [DECLARED_IRREVERSIBLE]: "declared_irreversible",
  [IRREVERSIBLE_OPERATION]: "irreversible_operation",
  [NOT_READ_ONLY]: "not_read_only",
  [INVALID_OPERATION]: "invalid_operation",
  [DATABASE]: "database",
  [CANCELED]: "canceled",
} as const satisfies Record<number, string>;

/** CODES lists every code this protocol version assigns, in the order §7 tabulates them. */
export const CODES = [
  PARSE_ERROR,
  INVALID_REQUEST,
  METHOD_NOT_FOUND,
  INVALID_PARAMS,
  INTERNAL_ERROR,
  UNSUPPORTED_PROTOCOL,
  UNSUPPORTED_IR,
  SESSION_STATE,
  UNKNOWN_MIGRATION,
  MIGRATION_FAILED,
  NO_DOWN_AUTHORED,
  DECLARED_IRREVERSIBLE,
  IRREVERSIBLE_OPERATION,
  NOT_READ_ONLY,
  INVALID_OPERATION,
  DATABASE,
  CANCELED,
] as const;

/** slugOf names one code, or reports that this protocol version assigns it nothing. */
export function slugOf(code: number): string | undefined {
  return (SLUGS as Record<number, string | undefined>)[code];
}

/** ProtocolError is a JSON-RPC error object, and the failure this side raises for one. */
export class ProtocolError extends Error {
  /** The code §7 assigns this failure. */
  readonly code: Code;
  /** The data member, which always carries the slug §7.1 requires. */
  readonly data: Record<string, unknown>;

  /** Builds one failure, filling in the slug its code carries. */
  constructor(code: Code, message: string, data?: Readonly<Record<string, unknown>>) {
    super(message);

    this.name = "ProtocolError";
    this.code = code;
    this.data = { slug: slugOf(code) ?? "unknown", ...data };
  }

  /** slug is what the other side branches on, rather than the number or the prose. */
  get slug(): string {
    return String(this.data["slug"]);
  }
}

/** codeOf reports the code a failure carries, and undefined when it carries none. */
export function codeOf(thrown: unknown): Code | undefined {
  return thrown instanceof ProtocolError ? thrown.code : undefined;
}
