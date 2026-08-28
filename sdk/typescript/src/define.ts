// The recorded prior state an operation carries in `from`, per docs/ir.md §6.5.
//
// Supplied, the operation inverts exactly; absent, it is irreversible with a reason (§8). Note
// that `from` means two things in this format: a string in rename_table and rename_column, and one
// of these objects everywhere else (§6.6). The two never appear on one operation, so `op` always
// determines which.

import type { Action, Column, Default } from "./column.ts";
import type { Type } from "./type.ts";

/** TableDef is the definition a dropped table had, enough to recreate it. */
export interface TableDef {
  /** Every column the table carried, in order. */
  columns: Column[];
  /** The key columns, omitted when the table had none. */
  primary_key?: string[];
}

/** ColumnsDef is a bare column list: the definition of an index, unique, or primary key. */
export interface ColumnsDef {
  /** The columns the dropped object covered, in order. */
  columns: string[];
}

/** ForeignKeyDef is the definition a dropped foreign key had. */
export interface ForeignKeyDef {
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

/** CheckDef is the expression a dropped check constraint carried. */
export interface CheckDef {
  /** The constraint's own SQL, which is engine dialect and does not port. */
  expression: string;
}

/** ViewDef is the query a view was defined by before it was dropped or replaced. */
export interface ViewDef {
  /** The view's own SQL, which is engine dialect and does not port. */
  query: string;
}

/** ColumnState is what a column looked like before an alter_column changed it. */
export interface ColumnState {
  /** The prior logical type, absent when the alteration left the type alone. */
  type?: Type;
  /** The prior nullability, absent when the alteration left it alone. */
  nullable?: boolean;
  /** The prior default, absent when the alteration left it alone. */
  default?: Default;
  /** That the column had no default, which is what inverts setting one. */
  no_default?: boolean;
}
