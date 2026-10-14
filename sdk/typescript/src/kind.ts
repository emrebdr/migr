// The operation vocabulary, spelled as docs/ir.md §7.1 spells it.

/** Kind names one operation in the IR vocabulary, spelled as docs/ir.md spells it. */
export type Kind =
  | "create_table"
  | "drop_table"
  | "rename_table"
  | "add_column"
  | "drop_column"
  | "rename_column"
  | "alter_column"
  | "add_index"
  | "drop_index"
  | "add_foreign_key"
  | "drop_foreign_key"
  | "add_unique"
  | "drop_unique"
  | "add_check"
  | "drop_check"
  | "add_primary_key"
  | "drop_primary_key"
  | "create_view"
  | "drop_view"
  | "raw"
  | "data_copy"
  | "data_backfill"
  | "data_transform";

/** KINDS lists every operation in the order docs/ir.md §7.1 declares them. */
const KINDS = [
  "create_table",
  "drop_table",
  "rename_table",
  "add_column",
  "drop_column",
  "rename_column",
  "alter_column",
  "add_index",
  "drop_index",
  "add_foreign_key",
  "drop_foreign_key",
  "add_unique",
  "drop_unique",
  "add_check",
  "drop_check",
  "add_primary_key",
  "drop_primary_key",
  "create_view",
  "drop_view",
  "raw",
  "data_copy",
  "data_backfill",
  "data_transform",
] as const satisfies readonly Kind[];

/** DATA_KINDS are the operations irVersion 2 added, every one of which writes rows rather than schema. */
const DATA_KINDS: readonly Kind[] = ["data_copy", "data_backfill", "data_transform"];

/** since returns the irVersion that first carries a kind, which an engine must state to read it. */
export function since(kind: Kind): number {
  return DATA_KINDS.includes(kind) ? 2 : 1;
}

/** kinds returns every operation kind, in declaration order. */
export function kinds(): Kind[] {
  return [...KINDS];
}

/** isKind reports whether a value is one of the operations this IR version defines. */
export function isKind(value: unknown): value is Kind {
  return (KINDS as readonly unknown[]).includes(value);
}
