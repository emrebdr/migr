// The one error this SDK raises when a module is not the migration its filename names.
//
// A code rather than a sentinel per reason, because JavaScript has no errors.Is and a caller
// branching on a message is a caller that breaks when the message is improved. The session M7-T4
// lands maps these onto the protocol's own codes.

/** MigrationErrorCode is every reason a module cannot be read as the migration its filename names. */
export type MigrationErrorCode =
  | "malformed_name"
  | "no_migration"
  | "ambiguous_module"
  | "both_directions"
  | "no_reason"
  | "bad_member"
  | "duplicate";

/** OperationError reports an operation or a logical type the engine would refuse as IR. */
export class OperationError extends Error {
  /** operation names which operation of an emit it is about, when there is one to name. */
  readonly operation: string | undefined;

  /** Builds one refusal, naming the operation it is about when the emit knows which. */
  constructor(message: string, operation?: string) {
    super(message);

    this.name = "OperationError";
    this.operation = operation;
  }
}

/** MigrationError reports a migration this SDK will not load, and which rule refused it. */
export class MigrationError extends Error {
  /** code is the rule that refused it, which the session maps onto a protocol error code. */
  readonly code: MigrationErrorCode;
  /** file is what it was read from, and is undefined when the problem belongs to no one file. */
  readonly file: string | undefined;

  /** Builds one refusal, naming the rule and the file it was found in. */
  constructor(code: MigrationErrorCode, file: string | undefined, message: string) {
    super(message);

    this.name = "MigrationError";
    this.code = code;
    this.file = file;
  }
}
