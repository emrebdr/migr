// docs/ir.md §9.1, run again on this side.
//
// The host decodes every operation as an IR document and runs all of this a second time, so a check
// here is not what makes a bad operation safe. It is here for ADR 0038 C's two reasons, neither of
// which is safety: the failure names the call that built it rather than a JSON document, and two
// SDKs must refuse the same operations before M7-T7's byte-for-byte comparison can mean anything.
//
// §9.1 is structure only. Whether a type's parameters are within an engine's limits, whether an
// expression is valid SQL, and whether the table exists are §9.2's -- the validator's and the
// capability model's, against a real schema.

import type { Action, Column, Default } from "./column.ts";
import { ACTIONS, FUNCTIONS } from "./column.ts";
import type { ColumnState, TableDef } from "./define.ts";
import { OperationError } from "./error.ts";
import type { Operation } from "./operation.ts";
import { TRANSFORMS } from "./operation.ts";

/** check reports an operation the engine would refuse, before it is sent. */
export function check(op: Operation): void {
  switch (op.op) {
    case "create_table":
      return checkCreateTable(op.table, op.columns, op.primary_key);
    case "drop_table":
      requireName("table", op.table);

      return checkTableDef(op.from);
    case "rename_table":
      return requireRename("table", op.from, op.to);
    case "add_column":
      requireName("table", op.table);

      return checkColumn(op.column);
    case "drop_column":
      return checkDropColumn(op.table, op.column, op.from);
    case "rename_column":
      requireName("table", op.table);

      return requireRename("column", op.from, op.to);
    case "alter_column":
      return checkAlterColumn(op);
    case "add_index":
    case "add_unique":
      return requireNamedColumns(op.table, op.name, op.columns);
    case "drop_index":
    case "drop_unique":
      requireNamed(op.table, op.name);

      return checkColumnsDef(op.from);
    case "add_foreign_key":
      return checkAddForeignKey(op);
    case "drop_foreign_key":
      requireNamed(op.table, op.name);

      return checkForeignKeyDef(op.from);
    case "add_check":
      requireNamed(op.table, op.name);

      return requireName("expression", op.expression);
    case "drop_check":
      requireNamed(op.table, op.name);

      return checkCheckDef(op.from);
    case "add_primary_key":
      requireName("table", op.table);
      requireColumns("columns", op.columns);

      return requireDistinctIn("columns", op.columns);
    case "drop_primary_key":
      requireName("table", op.table);

      return checkColumnsDef(op.from);
    case "create_view":
      requireName("view", op.view);
      requireName("query", op.query);

      return checkViewDef(op.from);
    case "drop_view":
      requireName("view", op.view);

      return checkViewDef(op.from);
    case "raw":
      requireName("driver", op.driver);

      return requireName("sql", op.sql);
    case "data_copy":
      return checkDataCopy(op);
    case "data_backfill":
      return checkDataBackfill(op);
    case "data_transform":
      return checkDataTransform(op);
  }
}

/** checkDataCopy reports a copy naming no table, copying a table into itself, or writing a column twice. */
function checkDataCopy(op: Extract<Operation, { op: "data_copy" }>): void {
  requireName("from", op.from);
  requireName("to", op.to);

  if (op.from === op.to) {
    throw new OperationError(
      `from and to both name ${JSON.stringify(op.from)}, and a table copied into itself ` +
        "duplicates every row",
    );
  }

  if (op.columns.length === 0) {
    throw new OperationError("columns must list at least one column to copy");
  }

  const written = new Set<string>();

  op.columns.forEach((pair, at) => {
    if (pair.from.trim() === "" || pair.to.trim() === "") {
      throw new OperationError(
        `columns[${at}] must name both the column it reads and the one it writes`,
      );
    }

    if (written.has(pair.to)) {
      throw new OperationError(
        `columns[${at}] writes ${JSON.stringify(pair.to)}, which an earlier pair already writes`,
      );
    }

    written.add(pair.to);
  });
}

/** checkDataBackfill reports a backfill naming no column, no type, or no value. */
function checkDataBackfill(op: Extract<Operation, { op: "data_backfill" }>): void {
  checkTarget(op.table, op.column, op.type);

  if (op.value === undefined) {
    throw new OperationError(
      "value is required: a backfill writes a literal or a neutral function",
    );
  }

  within("value", () => checkDefault(op.value));
}

/** checkDataTransform reports a transform naming no columns, an unknown function, or a copy into itself. */
function checkDataTransform(op: Extract<Operation, { op: "data_transform" }>): void {
  checkTarget(op.table, op.column, op.type);
  requireName("from", op.from);

  if (!(TRANSFORMS as readonly string[]).includes(op.function)) {
    throw new OperationError(
      `unknown function ${JSON.stringify(op.function)} (expected one of: ${TRANSFORMS.join(", ")})`,
    );
  }

  if (op.function === "copy" && op.from === op.column) {
    throw new OperationError(
      `copies ${JSON.stringify(op.column)} into itself, which changes nothing`,
    );
  }
}

/** checkTarget reports a write naming no table, no column, or no type for that column. */
function checkTarget(table: string, column: string, type: string | undefined): void {
  requireName("table", table);
  requireName("column", column);

  if (type === undefined || type.trim() === "") {
    throw new OperationError(
      `type is required: it is the type ${JSON.stringify(column)} holds, which every driver ` +
        "renders the value in",
    );
  }
}

/** checkAll reports the first operation of an emit the engine would refuse, naming which. */
export function checkAll(ops: readonly Operation[]): void {
  ops.forEach((op, at) => {
    try {
      check(op);
    } catch (thrown) {
      throw new OperationError(
        `invalid operation ${at + 1} (${op.op}): ${messageOf(thrown)}`,
        `${at + 1} (${op.op})`,
      );
    }
  });
}

/** messageOf renders what a check threw, since JavaScript permits throwing anything. */
function messageOf(thrown: unknown): string {
  return thrown instanceof Error ? thrown.message : String(thrown);
}

/** checkCreateTable reports a table created with no name, no columns, or a key naming something else. */
function checkCreateTable(table: string, columns: Column[], key: string[] | undefined): void {
  requireName("table", table);

  if (columns.length === 0) {
    throw new OperationError("columns must list at least one column");
  }

  const declared = new Set<string>();

  columns.forEach((column, at) => {
    within(`columns[${at}]`, () => checkColumn(column));

    if (declared.has(column.name)) {
      throw new OperationError(
        `column ${JSON.stringify(column.name)} is declared more than once`,
      );
    }

    declared.add(column.name);
  });

  checkKeyColumns("primary_key", key, declared);
}

/** checkDropColumn reports a drop naming no column, or recording one that is not the column dropped. */
function checkDropColumn(table: string, column: string, from: Column | undefined): void {
  requireName("table", table);
  requireName("column", column);

  if (from === undefined) {
    return;
  }

  within("from", () => checkColumn(from));

  if (from.name !== column) {
    throw new OperationError(
      `from.name ${JSON.stringify(from.name)} does not name the column being dropped ` +
        `(${JSON.stringify(column)})`,
    );
  }
}

/** checkAlterColumn reports an alteration that is unusable, contradictory, or asks for nothing. */
function checkAlterColumn(op: Extract<Operation, { op: "alter_column" }>): void {
  requireName("table", op.table);
  requireName("column", op.column);

  if (op.type !== undefined) {
    requireName("type", op.type);
  }

  if (op.default !== undefined && op.drop_default === true) {
    throw new OperationError("default and drop_default cannot both be set");
  }

  checkDefault(op.default);
  within("from", () => checkColumnState(op.from));

  if (
    op.type === undefined &&
    op.nullable === undefined &&
    op.default === undefined &&
    op.drop_default !== true
  ) {
    throw new OperationError(
      "requests no change: set at least one of type, nullable, default, drop_default",
    );
  }
}

/** checkAddForeignKey reports a key whose two sides are unnamed, unequal, or badly qualified. */
function checkAddForeignKey(op: Extract<Operation, { op: "add_foreign_key" }>): void {
  requireNamedColumns(op.table, op.name, op.columns);
  requireName("references_table", op.references_table);
  requireColumns("references_columns", op.references_columns);
  requirePaired(op.columns, op.references_columns, "columns", "references_columns");
  checkAction("on_delete", op.on_delete);
  checkAction("on_update", op.on_update);
}

/** checkColumn reports a column that names itself with no name, or carries no usable type. */
function checkColumn(column: Column): void {
  requireName("column name", column.name);
  within(`column ${JSON.stringify(column.name)}`, () => {
    requireName("type", column.type);
    checkDefault(column.default);
  });
}

/** checkDefault reports a default that names no value, or names two. */
function checkDefault(value: Default | undefined): void {
  if (value === undefined) {
    return;
  }

  if (value.literal === undefined && value.function === undefined) {
    throw new OperationError("default must set either literal or function");
  }

  if (value.literal !== undefined && value.function !== undefined) {
    throw new OperationError("default must set literal or function, not both");
  }

  if (
    value.function !== undefined &&
    !(FUNCTIONS as readonly string[]).includes(value.function)
  ) {
    throw new OperationError(
      `unknown default function ${JSON.stringify(value.function)} ` +
        `(expected one of: ${FUNCTIONS.join(", ")})`,
    );
  }
}

/** checkTableDef reports a recorded definition that could not rebuild a table. */
function checkTableDef(from: TableDef | undefined): void {
  if (from === undefined) {
    return;
  }

  if (from.columns.length === 0) {
    throw new OperationError("from.columns must list at least one column");
  }

  const declared = new Set<string>();

  from.columns.forEach((column, at) => {
    within(`from.columns[${at}]`, () => checkColumn(column));
    declared.add(column.name);
  });

  checkKeyColumns("from.primary_key", from.primary_key, declared);
}

/** checkColumnsDef reports a recorded column list that is absent, unnamed, or repeated. */
function checkColumnsDef(from: { columns: string[] } | undefined): void {
  if (from === undefined) {
    return;
  }

  requireColumns("from.columns", from.columns);
  requireDistinctIn("from.columns", from.columns);
}

/** checkForeignKeyDef reports whether both sides of the recorded key are named and equally wide. */
function checkForeignKeyDef(
  from: Extract<Operation, { op: "drop_foreign_key" }>["from"] | undefined,
): void {
  if (from === undefined) {
    return;
  }

  requireColumns("from.columns", from.columns);
  requireName("from.references_table", from.references_table);
  requireColumns("from.references_columns", from.references_columns);
  requirePaired(from.columns, from.references_columns, "from.columns", "from.references_columns");
  checkAction("from.on_delete", from.on_delete);
  checkAction("from.on_update", from.on_update);
}

/** checkCheckDef reports a recorded expression that is absent. */
function checkCheckDef(from: { expression: string } | undefined): void {
  if (from !== undefined) {
    requireName("from.expression", from.expression);
  }
}

/** checkViewDef reports a recorded query that is absent. */
function checkViewDef(from: { query: string } | undefined): void {
  if (from !== undefined) {
    requireName("from.query", from.query);
  }
}

/** checkColumnState reports a recorded prior state that could not be restored. */
function checkColumnState(from: ColumnState | undefined): void {
  if (from === undefined) {
    return;
  }

  if (from.type !== undefined) {
    requireName("type", from.type);
  }

  if (from.default !== undefined && from.no_default === true) {
    throw new OperationError("from.default and from.no_default cannot both be set");
  }

  checkDefault(from.default);
}

/** checkKeyColumns reports whether every key column was declared by the table it keys. */
function checkKeyColumns(
  field: string,
  key: string[] | undefined,
  declared: ReadonlySet<string>,
): void {
  if (key === undefined) {
    return;
  }

  key.forEach((name, at) => {
    if (!declared.has(name)) {
      throw new OperationError(
        `${field}[${at}]: ${JSON.stringify(name)} is not a column of this table`,
      );
    }
  });
}

/** checkAction reports a referential action that is not one of the accepted set. */
function checkAction(field: string, action: Action | undefined): void {
  if (action !== undefined && !(ACTIONS as readonly string[]).includes(action)) {
    throw new OperationError(
      `unknown ${field} action ${JSON.stringify(action)} (expected one of: ${ACTIONS.join(", ")})`,
    );
  }
}

/** requireName reports whether a required identifier member is present. */
function requireName(field: string, value: string | undefined): void {
  if (value === undefined || value.trim() === "") {
    throw new OperationError(`${field} is required`);
  }
}

/** requireColumns reports whether a required column list is present and fully named. */
function requireColumns(field: string, columns: readonly string[]): void {
  if (columns.length === 0) {
    throw new OperationError(`${field} must list at least one column`);
  }

  columns.forEach((column, at) => {
    if (column.trim() === "") {
      throw new OperationError(`${field}[${at}] is empty`);
    }
  });
}

/** requireDistinctIn reports whether a named column list mentions any column twice. */
function requireDistinctIn(field: string, columns: readonly string[]): void {
  const seen = new Set<string>();

  for (const column of columns) {
    if (seen.has(column)) {
      throw new OperationError(`${field} lists ${JSON.stringify(column)} more than once`);
    }

    seen.add(column);
  }
}

/** requirePaired reports two lists that pair by position and are not the same width. */
function requirePaired(
  columns: readonly string[],
  references: readonly string[],
  field: string,
  other: string,
): void {
  if (columns.length !== references.length) {
    throw new OperationError(
      `${field} has ${columns.length} entries but ${other} has ${references.length}`,
    );
  }
}

/** requireRename reports whether a rename names two distinct, non-empty identifiers. */
function requireRename(subject: string, from: string, to: string): void {
  requireName("from", from);
  requireName("to", to);

  if (from === to) {
    throw new OperationError(
      `${subject} rename has the same from and to (${JSON.stringify(from)})`,
    );
  }
}

/** requireNamed reports whether a table and an object name are both present. */
function requireNamed(table: string, name: string): void {
  requireName("table", table);
  requireName("name", name);
}

/** requireNamedColumns reports whether a named object over columns is fully specified. */
function requireNamedColumns(table: string, name: string, columns: readonly string[]): void {
  requireNamed(table, name);
  requireColumns("columns", columns);
  requireDistinctIn("columns", columns);
}

/** within runs a check and prefixes what it reports with the member it was about. */
function within(field: string, checking: () => void): void {
  try {
    checking();
  } catch (thrown) {
    throw new OperationError(`${field}: ${messageOf(thrown)}`);
  }
}
