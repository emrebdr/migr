// The twenty-three operations, and the shape table that decides their bytes.
//
// docs/ir.md §3.2 fixes member order and §3.6 omits an unset optional rather than writing null, and
// §4.2 makes both a compatibility surface: the emitted bytes are hashed, so reordering a member
// invalidates every applied migration. The operations are also exported so a chain and a
// hand-written literal both work -- and a literal written in the wrong order would stringify in the
// wrong order. So nothing here trusts how a value was written: the encoder rebuilds every object
// from the shape below, which is what Go gets from struct field order and its json tags.
//
// One ordered member list per operation and per shared structure drives three things that would
// otherwise drift: encoding, decoding (where a member this copy does not declare is an error rather
// than something skipped, §3.4), and spec.test.ts's comparison against the document's own tables.
//
// `op` is a member of the value here where §3.1 says it is not a member of the document. TypeScript
// has no runtime type identity and drop_index, drop_unique, and drop_primary_key are otherwise the
// same shape, so the discriminant is how a value names itself. The encoder still writes it first.

import type { Action, Column, Default } from "./column.ts";
import type {
  CheckDef,
  ColumnState,
  ColumnsDef,
  ForeignKeyDef,
  TableDef,
  ViewDef,
} from "./define.ts";
import { OperationError } from "./error.ts";
import type { Kind } from "./kind.ts";
import { isKind } from "./kind.ts";
import type { Type } from "./type.ts";

/** CreateTable creates a table with its columns and, optionally, its primary key. */
export interface CreateTable {
  op: "create_table";
  /** The name of the table to create. */
  table: string;
  /** Every column the table starts with, in order. At least one, all distinct. */
  columns: Column[];
  /** The key columns, stated inline, which is how a new table states its key (§7.2). */
  primary_key?: string[];
}

/** DropTable removes a table and everything in it. */
export interface DropTable {
  op: "drop_table";
  /** The name of the table to remove. */
  table: string;
  /** What the table held, without which the drop cannot be undone. */
  from?: TableDef;
}

/** RenameTable renames a table, preserving its contents. */
export interface RenameTable {
  op: "rename_table";
  /** The table's current name. A string, not a recorded-state object (§6.6). */
  from: string;
  /** The name it takes. */
  to: string;
}

/** AddColumn adds one column to an existing table. */
export interface AddColumn {
  op: "add_column";
  /** The table the column joins. */
  table: string;
  /** The column itself, with its logical type and nullability. */
  column: Column;
}

/** DropColumn removes a column and the data it holds. */
export interface DropColumn {
  op: "drop_column";
  /** The table the column leaves. */
  table: string;
  /** The name of the column to remove. */
  column: string;
  /** What the column was, whose name must be the column being dropped. */
  from?: Column;
}

/** RenameColumn renames a column, preserving its data. */
export interface RenameColumn {
  op: "rename_column";
  /** The table the column belongs to. */
  table: string;
  /** The column's current name. A string, not a recorded-state object (§6.6). */
  from: string;
  /** The name it takes. */
  to: string;
}

/** AlterColumn changes a column's type, nullability, or default; at least one is required. */
export interface AlterColumn {
  op: "alter_column";
  /** The table the column belongs to. */
  table: string;
  /** The name of the column to change. */
  column: string;
  /** The new logical type, absent to leave the type alone. */
  type?: Type;
  /** The new nullability, absent to leave nullability alone. */
  nullable?: boolean;
  /** Sets a default, and cannot appear beside drop_default. */
  default?: Column["default"];
  /** Removes the existing default, which absence of default would not. */
  drop_default?: boolean;
  /** The prior state, without which the change cannot be undone. */
  from?: ColumnState;
}

/** AddIndex creates a non-unique index over the given columns. */
export interface AddIndex {
  op: "add_index";
  /** The table the index covers. */
  table: string;
  /** The index's own name. */
  name: string;
  /** The indexed columns, in order. At least one, all distinct. */
  columns: string[];
}

/** DropIndex removes an index. The table's rows are untouched. */
export interface DropIndex {
  op: "drop_index";
  /** The table the index covered. */
  table: string;
  /** The index's own name. */
  name: string;
  /** Which columns it covered, without which the drop cannot be undone. */
  from?: ColumnsDef;
}

/** AddForeignKey references another table's columns. */
export interface AddForeignKey {
  op: "add_foreign_key";
  /** The referencing table. */
  table: string;
  /** The constraint's own name. */
  name: string;
  /** The referencing side, in order. */
  columns: string[];
  /** The table referred to. */
  references_table: string;
  /** The referenced side, in the same order and of the same width. */
  references_columns: string[];
  /** What happens to a referencing row when its referent is deleted. */
  on_delete?: Action;
  /** What happens to a referencing row when its referent's key changes. */
  on_update?: Action;
}

/** DropForeignKey removes a foreign key. */
export interface DropForeignKey {
  op: "drop_foreign_key";
  /** The referencing table. */
  table: string;
  /** The constraint's own name. */
  name: string;
  /** What the key referred to, without which the drop cannot be undone. */
  from?: ForeignKeyDef;
}

/** AddUnique adds a unique constraint over the given columns. */
export interface AddUnique {
  op: "add_unique";
  /** The table the constraint covers. */
  table: string;
  /** The constraint's own name. */
  name: string;
  /** The constrained columns, in order. At least one, all distinct. */
  columns: string[];
}

/** DropUnique removes a unique constraint. */
export interface DropUnique {
  op: "drop_unique";
  /** The table the constraint covered. */
  table: string;
  /** The constraint's own name. */
  name: string;
  /** Which columns it covered, without which the drop cannot be undone. */
  from?: ColumnsDef;
}

/** AddCheck adds a check constraint whose expression is engine SQL and does not port. */
export interface AddCheck {
  op: "add_check";
  /** The table the constraint covers. */
  table: string;
  /** The constraint's own name. */
  name: string;
  /** The constraint's own SQL, in the target engine's dialect. */
  expression: string;
}

/** DropCheck removes a check constraint. */
export interface DropCheck {
  op: "drop_check";
  /** The table the constraint covered. */
  table: string;
  /** The constraint's own name. */
  name: string;
  /** The expression it carried, without which the drop cannot be undone. */
  from?: CheckDef;
}

/** AddPrimaryKey keys a table that already exists; a new table states its key inline. */
export interface AddPrimaryKey {
  op: "add_primary_key";
  /** The table the key covers. */
  table: string;
  /** The constraint's own name, omitted where the engine names it for you. */
  name?: string;
  /** The key columns, in order. At least one, all distinct. */
  columns: string[];
}

/** DropPrimaryKey removes a table's primary key. The name is optional: MySQL has none. */
export interface DropPrimaryKey {
  op: "drop_primary_key";
  /** The table the key covered. */
  table: string;
  /** The constraint's own name, omitted where the engine has none. */
  name?: string;
  /** Which columns it covered, without which the drop cannot be undone. */
  from?: ColumnsDef;
}

/** CreateView creates or replaces a view whose query is engine SQL and does not port. */
export interface CreateView {
  op: "create_view";
  /** The name of the view to create. */
  view: string;
  /** The view's own SQL, in the target engine's dialect. */
  query: string;
  /** Overwrites an existing view, which needs from to be undoable. */
  or_replace?: boolean;
  /** The definition an or_replace overwrote. */
  from?: ViewDef;
}

/** DropView removes a view. */
export interface DropView {
  op: "drop_view";
  /** The name of the view to remove. */
  view: string;
  /** The query it was defined by, without which the drop cannot be undone. */
  from?: ViewDef;
}

/** Raw is the driver-scoped escape hatch: SQL run verbatim against the engine it names. */
export interface Raw {
  op: "raw";
  /** The engine this SQL is written for, which no other engine will run. */
  driver: string;
  /** The statement itself, in that engine's own dialect. */
  sql: string;
  /** Declares the statement cannot run inside a transaction, which irVersion 3 carries. */
  no_transaction?: boolean;
}

/** Transform names what data_transform applies to its source, the text functions over its text. */
export type Transform = "copy" | "lower" | "upper" | "trim";

/** TRANSFORMS lists every transform function, in the order docs/ir.md states them. */
export const TRANSFORMS = [
  "copy",
  "lower",
  "upper",
  "trim",
] as const satisfies readonly Transform[];

/** CopyColumn is one column data_copy reads from its source table and the column it writes it into. */
export interface CopyColumn {
  /** The column read. */
  from: string;
  /** The column written. */
  to: string;
}

/** DataCopy inserts every row of one table into another, column by named column. */
export interface DataCopy {
  op: "data_copy";
  /** The table read. */
  from: string;
  /** The table written, which must differ from from. */
  to: string;
  /** Each column read and the one it is written into. */
  columns: CopyColumn[];
}

/** DataBackfill fills the NULLs of one column with a literal or a neutral function, and nothing else. */
export interface DataBackfill {
  op: "data_backfill";
  /** The table written. */
  table: string;
  /** The column whose NULLs are filled. */
  column: string;
  /** That column's logical type, which the value is rendered in. */
  type: Type;
  /** What fills them: a literal or one of the neutral functions. */
  value: Default;
}

/** DataTransform rewrites one column of every row from another column, through a neutral function. */
export interface DataTransform {
  op: "data_transform";
  /** The table written. */
  table: string;
  /** The column written. */
  column: string;
  /** That column's logical type, which the result is converted to. */
  type: Type;
  /** The column read. */
  from: string;
  /** What is applied to it. */
  function: Transform;
}

/** Operation is one schema change a migration emits, which the engine compiles for its driver. */
export type Operation =
  | CreateTable
  | DropTable
  | RenameTable
  | AddColumn
  | DropColumn
  | RenameColumn
  | AlterColumn
  | AddIndex
  | DropIndex
  | AddForeignKey
  | DropForeignKey
  | AddUnique
  | DropUnique
  | AddCheck
  | DropCheck
  | AddPrimaryKey
  | DropPrimaryKey
  | CreateView
  | DropView
  | Raw
  | DataCopy
  | DataBackfill
  | DataTransform;

/** Member is one member of an emitted object, and the shape of its value when it has one. */
export interface Member {
  /** The member's name on the wire. */
  readonly name: string;
  /** Whether the document says it is always present. */
  readonly required: boolean;
  /** The shape of its value, for a member that is an object or a list of them. */
  readonly of?: Shape;
  /** Whether the member is a list of that shape rather than one of it. */
  readonly list?: boolean;
  /** What a required member emits when the value leaves it out, which only nullable has. */
  readonly whenAbsent?: unknown;
  /** Whether the document says this member is omitted when empty as well as when unset. */
  readonly omitEmpty?: boolean;
}

/** Shape is one object's members, in the order docs/ir.md lists them. */
export type Shape = readonly Member[];

/** req declares a member the document says is always present. */
function req(name: string, of?: Shape, extra?: Partial<Member>): Member {
  return { name, required: true, ...(of === undefined ? {} : { of }), ...extra };
}

/** opt declares a member that is omitted when unset and never emitted as null. */
function opt(name: string, of?: Shape, extra?: Partial<Member>): Member {
  return { name, required: false, ...(of === undefined ? {} : { of }), ...extra };
}

/** EMPTY marks the optional members the document says are omitted when empty, not only when unset. */
const EMPTY: Partial<Member> = { omitEmpty: true };

/** DEFAULT is the shape of docs/ir.md §6.2, of which exactly one member is present. */
const DEFAULT: Shape = [opt("literal"), opt("function")];

/** COLUMN is the shape of docs/ir.md §6.1, whose nullable is emitted even when false. */
const COLUMN: Shape = [
  req("name"),
  req("type"),
  req("nullable", undefined, { whenAbsent: false }),
  opt("default", DEFAULT),
];

/** TABLE_DEF is what a dropped table held, enough to recreate it. */
const TABLE_DEF: Shape = [
  req("columns", COLUMN, { list: true }),
  opt("primary_key", undefined, EMPTY),
];

/** COLUMNS_DEF is the bare column list an index, unique, or primary key is. */
const COLUMNS_DEF: Shape = [req("columns")];

/** FOREIGN_KEY_DEF is the definition a dropped foreign key had. */
const FOREIGN_KEY_DEF: Shape = [
  req("columns"),
  req("references_table"),
  req("references_columns"),
  opt("on_delete", undefined, EMPTY),
  opt("on_update", undefined, EMPTY),
];

/** CHECK_DEF is the expression a dropped check constraint carried. */
const CHECK_DEF: Shape = [req("expression")];

/** VIEW_DEF is the query a view was defined by. */
const VIEW_DEF: Shape = [req("query")];

/** COLUMN_STATE is what a column looked like before an alter_column changed it. */
const COLUMN_STATE: Shape = [
  opt("type"),
  opt("nullable"),
  opt("default", DEFAULT),
  opt("no_default", undefined, EMPTY),
];

/** COPY_COLUMN is one pair data_copy reads and writes. */
const COPY_COLUMN: Shape = [req("from"), req("to")];

/** SHAPES holds every operation's members, in the order docs/ir.md §7.3 lists them. */
export const SHAPES: Readonly<Record<Kind, Shape>> = {
  create_table: [
    req("table"),
    req("columns", COLUMN, { list: true }),
    opt("primary_key", undefined, EMPTY),
  ],
  drop_table: [req("table"), opt("from", TABLE_DEF)],
  rename_table: [req("from"), req("to")],
  add_column: [req("table"), req("column", COLUMN)],
  drop_column: [req("table"), req("column"), opt("from", COLUMN)],
  rename_column: [req("table"), req("from"), req("to")],
  alter_column: [
    req("table"),
    req("column"),
    opt("type"),
    opt("nullable"),
    opt("default", DEFAULT),
    opt("drop_default", undefined, EMPTY),
    opt("from", COLUMN_STATE),
  ],
  add_index: [req("table"), req("name"), req("columns")],
  drop_index: [req("table"), req("name"), opt("from", COLUMNS_DEF)],
  add_foreign_key: [
    req("table"),
    req("name"),
    req("columns"),
    req("references_table"),
    req("references_columns"),
    opt("on_delete", undefined, EMPTY),
    opt("on_update", undefined, EMPTY),
  ],
  drop_foreign_key: [req("table"), req("name"), opt("from", FOREIGN_KEY_DEF)],
  add_unique: [req("table"), req("name"), req("columns")],
  drop_unique: [req("table"), req("name"), opt("from", COLUMNS_DEF)],
  add_check: [req("table"), req("name"), req("expression")],
  drop_check: [req("table"), req("name"), opt("from", CHECK_DEF)],
  add_primary_key: [req("table"), opt("name", undefined, EMPTY), req("columns")],
  drop_primary_key: [req("table"), opt("name", undefined, EMPTY), opt("from", COLUMNS_DEF)],
  create_view: [
    req("view"),
    req("query"),
    opt("or_replace", undefined, EMPTY),
    opt("from", VIEW_DEF),
  ],
  drop_view: [req("view"), opt("from", VIEW_DEF)],
  raw: [req("driver"), req("sql"), opt("no_transaction", undefined, EMPTY)],
  data_copy: [req("from"), req("to"), req("columns", COPY_COLUMN, { list: true })],
  data_backfill: [req("table"), req("column"), req("type"), req("value", DEFAULT)],
  data_transform: [req("table"), req("column"), req("type"), req("from"), req("function")],
};

/** STRUCTURES holds the shared structures docs/ir.md §6 defines, for a test that reads them back. */
export const STRUCTURES = { COLUMN, DEFAULT } as const;

/** documentOf renders one operation as the members the document defines, in its order. */
export function documentOf(op: Operation): Record<string, unknown> {
  return { op: op.op, ...ordered(op as unknown as Record<string, unknown>, SHAPES[op.op]) };
}

/** encode renders one operation as the exact bytes the engine hashes. */
export function encode(op: Operation): string {
  return JSON.stringify(documentOf(op));
}

/** ordered rebuilds one object in the order the document lists its members. */
function ordered(value: Record<string, unknown>, shape: Shape): Record<string, unknown> {
  const built: Record<string, unknown> = {};

  for (const member of shape) {
    const held = value[member.name] ?? member.whenAbsent;

    if (!omitted(held, member)) {
      built[member.name] = converted(held, member);
    }
  }

  return built;
}

/** omitted reports whether a member is left out, which §3.6 says of every unset optional. */
function omitted(held: unknown, member: Member): boolean {
  if (held === undefined) {
    return true;
  }

  if (member.omitEmpty !== true) {
    return false;
  }

  return held === "" || held === false || (Array.isArray(held) && held.length === 0);
}

/** converted renders one member's value, recursing into the shape it declares. */
function converted(held: unknown, member: Member): unknown {
  if (member.of === undefined) {
    return held;
  }

  if (member.list === true) {
    return (held as Record<string, unknown>[]).map((each) => ordered(each, member.of as Shape));
  }

  return ordered(held as Record<string, unknown>, member.of);
}

/** decode reads one IR document as the operation this copy declares for it. */
export function decode(document: unknown): Operation {
  const members = objectOf(document, "operation");
  const named = members["op"];

  if (!isKind(named)) {
    throw new OperationError(
      `${JSON.stringify(named)} is not an operation this IR version defines`,
    );
  }

  const { op: _discriminator, ...rest } = members;

  return { op: named, ...read(rest, SHAPES[named], named) } as Operation;
}

/** read builds one object from a document, refusing a member this copy does not declare. */
function read(
  members: Record<string, unknown>,
  shape: Shape,
  path: string,
): Record<string, unknown> {
  const declared = new Map(shape.map((member) => [member.name, member]));

  for (const name of Object.keys(members)) {
    if (!declared.has(name)) {
      throw new OperationError(
        `${path} carries ${JSON.stringify(name)}, which it does not define`,
      );
    }
  }

  const built: Record<string, unknown> = {};

  for (const member of shape) {
    const held = members[member.name];

    if (held !== undefined) {
      built[member.name] = restored(held, member, `${path}.${member.name}`);
    }
  }

  return built;
}

/** restored reads one member's value, recursing into the shape it declares. */
function restored(held: unknown, member: Member, path: string): unknown {
  if (member.of === undefined) {
    return held;
  }

  if (member.list === true) {
    return listOf(held, path).map((each, at) =>
      read(objectOf(each, `${path}[${at}]`), member.of as Shape, `${path}[${at}]`),
    );
  }

  return read(objectOf(held, path), member.of, path);
}

/** objectOf refuses a document member that should be an object and is not. */
function objectOf(value: unknown, path: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new OperationError(`${path} is not an object`);
  }

  return value as Record<string, unknown>;
}

/** listOf refuses a document member that should be a list and is not. */
function listOf(value: unknown, path: string): unknown[] {
  if (!Array.isArray(value)) {
    throw new OperationError(`${path} is not a list`);
  }

  return value;
}
