// The v1 operation vocabulary, spelled as docs/ir.md §7.1 spells it.

/** Kind names one operation in the v1 IR vocabulary, spelled as docs/ir.md spells it. */
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
  | "raw";

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
] as const satisfies readonly Kind[];

/** kinds returns every operation kind, in declaration order. */
export function kinds(): Kind[] {
  return [...KINDS];
}

/** isKind reports whether a value is one of the twenty operations this IR version defines. */
export function isKind(value: unknown): value is Kind {
  return (KINDS as readonly unknown[]).includes(value);
}
