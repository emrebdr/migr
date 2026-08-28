// A column, a default, and the two sets docs/ir.md closes.
//
// Both sets are closed by §10.2, so accepting a fifth default function or a sixth referential
// action emits a document the engine's decoder refuses. They are listed here as data as well as
// declared as types, because spec.test.ts reads the document and compares the two.

import type { Type } from "./type.ts";

/** Fn names a default-producing function every driver maps to its own spelling. */
export type Fn = "current_timestamp" | "current_date" | "current_time" | "uuid";

/** FUNCTIONS lists the four neutral default functions docs/ir.md §6.3 closes the set to. */
export const FUNCTIONS = [
  "current_timestamp",
  "current_date",
  "current_time",
  "uuid",
] as const satisfies readonly Fn[];

/** Action is what a foreign key does when its referenced row changes. */
export type Action = "no_action" | "restrict" | "cascade" | "set_null" | "set_default";

/** ACTIONS lists the five referential actions docs/ir.md §6.4 closes the set to. */
export const ACTIONS = [
  "no_action",
  "restrict",
  "cascade",
  "set_null",
  "set_default",
] as const satisfies readonly Action[];

/** Default is a column default: either a literal or a neutral function, never both. */
export interface Default {
  /** A constant value, always carried as a string so a round trip cannot retype it. */
  literal?: string;
  /** One of the neutral functions, which each driver writes in its own dialect. */
  function?: Fn;
}

/** Column is one column of a table, named with a logical type rather than a native one. */
export interface Column {
  /** The column's name, which is passed through exactly as written. */
  name: string;
  /** The logical type, which the target driver maps to a native one. */
  type: Type;
  /** Whether the column accepts no value; an unqualified column is NOT NULL (§7.2). */
  nullable?: boolean;
  /** The column's default, omitted when it has none. */
  default?: Default;
}

/** literal returns a default holding the given constant, which the IR always carries as a string. */
export function literal(value: string): Default {
  return { literal: value };
}

/** call returns a default produced by one of the four neutral functions. */
export function call(fn: Fn): Default {
  return { function: fn };
}
