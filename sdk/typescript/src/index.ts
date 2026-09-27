export { defineMigration } from "./migration.ts";
export type { Down, Migration, Up, Validate } from "./migration.ts";

export { newSchema } from "./schema.ts";
export type {
  Call,
  Direction,
  Host,
  Info,
  Level,
  Mode,
  Outcome,
  Rows,
  Schema,
  Verdict,
  Window,
} from "./schema.ts";

export { EXIT_FAILED, EXIT_OK, guard, run } from "./session.ts";
export type { Options } from "./session.ts";

export { main } from "./main.ts";

export { fromDirectory } from "./directory.ts";

export { columns } from "./builder.ts";
export type {
  AlterChange,
  ColumnChange,
  ForeignKeyChange,
  NewColumn,
  NewTable,
  Recorded,
  Sink,
  Statements,
  TableChain,
  ViewChain,
  ViewRecord,
} from "./builder.ts";

export { t, TYPE_KINDS } from "./type.ts";
export type { Type } from "./type.ts";

export { ACTIONS, call, FUNCTIONS, literal } from "./column.ts";
export type { Action, Column, Default, Fn } from "./column.ts";

export type {
  CheckDef,
  ColumnState,
  ColumnsDef,
  ForeignKeyDef,
  TableDef,
  ViewDef,
} from "./define.ts";

export { isKind, kinds } from "./kind.ts";
export type { Kind } from "./kind.ts";

export { decode, documentOf, encode } from "./operation.ts";
export type {
  AddCheck,
  AddColumn,
  AddForeignKey,
  AddIndex,
  AddPrimaryKey,
  AddUnique,
  AlterColumn,
  CreateTable,
  CreateView,
  DropCheck,
  DropColumn,
  DropForeignKey,
  DropIndex,
  DropPrimaryKey,
  DropTable,
  DropUnique,
  DropView,
  Operation,
  RenameColumn,
  RenameTable,
  Raw,
} from "./operation.ts";

export { check, checkAll } from "./check.ts";

export { MigrationError, OperationError } from "./error.ts";
export type { MigrationErrorCode } from "./error.ts";

export { EXTENSIONS, parseName } from "./name.ts";
export type { Named } from "./name.ts";

export { fromModule, Registry } from "./load.ts";
export type { Loaded } from "./load.ts";

export {
  SDK_NAME,
  VERSION,
  PROTOCOL_VERSION,
  PROTOCOL_VERSIONS,
  IR_VERSION,
  LANGUAGE,
  languageRuntime,
} from "./version.ts";
