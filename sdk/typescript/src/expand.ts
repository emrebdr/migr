// One column replaced through four migrations of its own: expand, backfill, swap, contract.
//
// The same declaration as the Go SDK's ReplaceColumn, emitting the same bytes (ADR 0079). Each phase
// is a migration object a file exports as its default, and the replacement is restated in every one
// of those files, so an applied file cannot change what it emits underneath its checksum. Swap and
// contract are unsafe while an older app version is still serving, and each says so with a warning
// sent in both modes, so migr plan shows it before migr up runs it.

import type { Column, Default } from "./column.ts";
import type { Migration } from "./migration.ts";
import { TRANSFORMS, type Transform } from "./operation.ts";
import type { Schema } from "./schema.ts";
import type { Type } from "./type.ts";

/** ReplacementError reports a column replacement that cannot be carried out as it is declared. */
export class ReplacementError extends Error {
  /** Builds one refusal, stating what makes the replacement impossible. */
  constructor(message: string) {
    super(`this column replacement cannot be carried out: ${message}`);

    this.name = "ReplacementError";
  }
}

/** Declared is everything one replacement states, which every phase reads and none changes. */
interface Declared {
  /** The table the column belongs to. */
  readonly table: string;
  /** The column being replaced, named as it is before expand. */
  readonly column: string;
  /** The column's type before expand. */
  readonly wasType: Type;
  /** Whether the column accepts NULL before expand. */
  readonly wasNull: boolean;
  /** The column's default before expand, absent when it has none. */
  readonly wasDefault: Default | undefined;
  /** The type the column becomes, which is the type it has unless `with` says otherwise. */
  readonly type: Type;
  /** Whether the replacing column accepts NULL once swap has run. */
  readonly null: boolean;
  /** The replacing column's default, from the moment expand adds it. */
  readonly default: Default | undefined;
  /** What the backfill applies to the old column to fill the new one. */
  readonly fn: Transform;
  /** The name the column ends under in the rename form, absent in the keep-name form. */
  readonly renamed: string | undefined;
  /** The name the new column carries until swap, in the keep-name form. */
  readonly via: string | undefined;
  /** The name the old column carries between swap and contract, in the keep-name form. */
  readonly retired: string | undefined;
}

/** Replacement is one column replaced through expand, backfill, swap and contract. */
export interface Replacement {
  /** wasNull records that the column accepts NULL before expand, which is not assumed. */
  wasNull(): Replacement;
  /** wasDefault records the default the column carries before expand. */
  wasDefault(value: Default): Replacement;
  /** with names the type the column becomes. */
  with(type: Type): Replacement;
  /** null lets the replacing column accept NULL, which it does not by default. */
  null(): Replacement;
  /** default gives the replacing column a default, from the moment expand adds it. */
  default(value: Default): Replacement;
  /** through names what the backfill applies to the old column, which is copy by default. */
  through(fn: Transform): Replacement;
  /** renameTo ends the column under a new name, so swap renames nothing and contract drops the old. */
  renameTo(name: string): Replacement;
  /** via names the column the new values are built in until swap, `<column>_next` by default. */
  via(name: string): Replacement;
  /** retire names the old column between swap and contract, `<column>_old` by default. */
  retire(name: string): Replacement;
  /** validate throws what makes this replacement impossible to carry out, reaching no database. */
  validate(): void;
  /** expand is the migration that adds the replacing column, nullable whatever it ends as. */
  expand(): Migration;
  /** backfill is the migration that fills the replacing column from the old one, in chunks. */
  backfill(): Migration;
  /** swap is the unsafe migration after which the replacing column is the one an app uses. */
  swap(): Migration;
  /** contract is the unsafe migration that drops the old column. */
  contract(): Migration;
}

/** replaceColumn declares a change to one NOT NULL column of the given type, in four phases. */
export function replaceColumn(table: string, column: string, was: Type): Replacement {
  return replacement({
    table,
    column,
    wasType: was,
    wasNull: false,
    wasDefault: undefined,
    type: was,
    null: false,
    default: undefined,
    fn: "copy",
    renamed: undefined,
    via: undefined,
    retired: undefined,
  });
}

/** replacement wraps one declaration in the chain, each link returning a new one. */
function replacement(d: Declared): Replacement {
  return {
    wasNull: () => replacement({ ...d, wasNull: true }),
    wasDefault: (value) => replacement({ ...d, wasDefault: value }),
    with: (type) => replacement({ ...d, type }),
    null: () => replacement({ ...d, null: true }),
    default: (value) => replacement({ ...d, default: value }),
    through: (fn) => replacement({ ...d, fn }),
    renameTo: (name) => replacement({ ...d, renamed: name }),
    via: (name) => replacement({ ...d, via: name }),
    retire: (name) => replacement({ ...d, retired: name }),
    validate: () => validate(d),
    expand: () => expandPhase(d),
    backfill: () => backfillPhase(d),
    swap: () => swapPhase(d),
    contract: () => (d.fn === "copy" ? contractPhase(d) : lossyContractPhase(d)),
  };
}

/** next is the name the replacing column carries from expand until swap. */
function next(d: Declared): string {
  return d.renamed ?? d.via ?? `${d.column}_next`;
}

/** old is the name the replaced column carries from swap until contract drops it. */
function old(d: Declared): string {
  if (d.renamed !== undefined) {
    return d.column;
  }

  return d.retired ?? `${d.column}_old`;
}

/** final is the name the replacing column ends under. */
function final(d: Declared): string {
  return d.renamed ?? d.column;
}

/** staged is the replacing column as expand adds it: its final type and default, and nullable. */
function staged(d: Declared): Column {
  return withDefault({ name: next(d), type: d.type, nullable: true }, d.default);
}

/** retiredColumn is the old column as contract finds it: its own type and default, and nullable. */
function retiredColumn(d: Declared): Column {
  return withDefault({ name: old(d), type: d.wasType, nullable: true }, d.wasDefault);
}

/** withDefault adds a default to a column where there is one, leaving the member absent otherwise. */
function withDefault(column: Column, value: Default | undefined): Column {
  return value === undefined ? column : { ...column, default: value };
}

/** validate throws a blank or colliding name, an unknown function, or a replacement of nothing. */
function validate(d: Declared): void {
  const refusal = checkNames(d) ?? checkColumns(d);

  if (refusal !== undefined) {
    throw new ReplacementError(refusal);
  }
}

/** checkNames states a blank name, a name used for two columns, or a form given both kinds of name. */
function checkNames(d: Declared): string | undefined {
  if (d.table.trim() === "") {
    return "table is required";
  }

  if (d.column.trim() === "") {
    return "column is required";
  }

  if (d.renamed !== undefined && (d.via !== undefined || d.retired !== undefined)) {
    return `renameTo(${JSON.stringify(d.renamed)}) ends the column under a name of its own, so via and retire name nothing`;
  }

  if (d.renamed === undefined && (next(d) === d.column || old(d) === d.column)) {
    return `${JSON.stringify(d.column)} is the column being replaced, and cannot also name the column it is built in or retired as`;
  }

  if (d.renamed !== undefined && d.renamed === d.column) {
    return `renameTo(${JSON.stringify(d.renamed)}) names the column it renames`;
  }

  if (next(d) === old(d)) {
    return `${JSON.stringify(next(d))} cannot name both the new column and the retired one`;
  }

  return undefined;
}

/** checkColumns states an unknown function, or a replacement that would change nothing. */
function checkColumns(d: Declared): string | undefined {
  if (!(TRANSFORMS as readonly string[]).includes(d.fn)) {
    return `unknown function ${JSON.stringify(d.fn)} (expected one of: ${TRANSFORMS.join(", ")})`;
  }

  if (unchanged(d)) {
    return `${JSON.stringify(d.column)} keeps its name, its type, its nullability and its values, so there is nothing to replace -- name a type with with, a function with through, or a name with renameTo`;
  }

  return undefined;
}

/** unchanged reports a replacement that would rebuild a column into exactly what it was. */
function unchanged(d: Declared): boolean {
  return (
    d.renamed === undefined &&
    d.fn === "copy" &&
    d.type === d.wasType &&
    d.null === d.wasNull &&
    d.default?.literal === d.wasDefault?.literal &&
    d.default?.function === d.wasDefault?.function
  );
}

/** quoted names a column of the replacement's table as an operator reads it. */
function quoted(d: Declared, column: string): string {
  return `${JSON.stringify(d.table)}.${JSON.stringify(column)}`;
}

/** unsafe files the warning an unsafe step carries, in both modes, so a preview shows it too. */
function unsafe(s: Schema, d: Declared, step: string, consequence: string): void {
  s.log("warn", `unsafe step: ${step}: ${consequence}`, {
    table: d.table,
    column: d.column,
    step,
  });
}

/** swapConsequence says what swap changes under an app version that is still serving. */
function swapConsequence(d: Declared): string {
  if (d.renamed !== undefined) {
    return `${quoted(d, d.column)} stops being required and ${quoted(d, d.renamed)} becomes the column an app writes, so every app version still serving must already write it`;
  }

  return `${quoted(d, d.column)} now names the new column and ${quoted(d, old(d))} the old one, so every app version still serving must already read and write the new one`;
}

/** expandPhase adds the replacing column, nullable, so no app version writing the table is refused. */
function expandPhase(d: Declared): Migration {
  return {
    async up(s) {
      validate(d);

      const column = staged(d);
      const change = s.table(d.table).addColumn(column.name, column.type).null();

      await (column.default === undefined ? change : change.default(column.default)).apply();
    },
    async down(s) {
      validate(d);
      unsafe(
        s,
        d,
        "expand down",
        `drops ${quoted(d, next(d))}, which an app version that writes both columns still writes`,
      );

      await s.table(d.table).dropColumn(next(d)).was(staged(d)).apply();
    },
    validate: () => validate(d),
  };
}

/** backfillPhase fills the replacing column from the old one, and undoes nothing of its own. */
function backfillPhase(d: Declared): Migration {
  return {
    async up(s) {
      validate(d);

      await s.table(d.table).transform(next(d), d.type, d.column, d.fn).apply();
    },
    down() {
      validate(d);
    },
    validate: () => validate(d),
  };
}

/** swapPhase relaxes the old column, tightens the new one, and in the keep-name form swaps names. */
function swapPhase(d: Declared): Migration {
  return {
    async up(s) {
      validate(d);
      unsafe(s, d, "swap", swapConsequence(d));

      const table = s.table(d.table);

      if (!d.wasNull) {
        table.alterColumn(d.column).null().wasNullable(false);
      }

      if (!d.null) {
        table.alterColumn(next(d)).notNull().wasNullable(true);
      }

      if (d.renamed === undefined) {
        table.renameColumn(d.column, old(d)).renameColumn(next(d), d.column);
      }

      await table.apply();
    },
    async down(s) {
      validate(d);
      unsafe(
        s,
        d,
        "swap down",
        `${quoted(d, d.column)} is the column an app reads again, so every app version still serving must already read it`,
      );

      const table = s.table(d.table);

      if (d.renamed === undefined) {
        table.renameColumn(d.column, next(d)).renameColumn(old(d), d.column);
      }

      if (!d.null) {
        table.alterColumn(next(d)).null().wasNullable(false);
      }

      if (!d.wasNull) {
        table.alterColumn(d.column).notNull().wasNullable(true);
      }

      await table.apply();
    },
    validate: () => validate(d),
  };
}

/** contractUp drops the old column and the values in it, recording it as swap left it. */
async function contractUp(s: Schema, d: Declared): Promise<void> {
  validate(d);
  unsafe(
    s,
    d,
    "contract",
    `drops ${quoted(d, old(d))} and the values in it, so no app version still serving may read it`,
  );

  await s.table(d.table).dropColumn(old(d)).was(retiredColumn(d)).apply();
}

/** contractPhase drops the old column, and its down copies the values back from the new one. */
function contractPhase(d: Declared): Migration {
  return {
    up: (s) => contractUp(s, d),
    async down(s) {
      validate(d);

      const retired = retiredColumn(d);
      const added = s.table(d.table).addColumn(retired.name, retired.type).null();

      await (retired.default === undefined ? added : added.default(retired.default))
        .transform(retired.name, retired.type, final(d), "copy")
        .apply();
    },
    validate: () => validate(d),
  };
}

/** lossyContractPhase drops an old column whose values the backfill did not carry over whole. */
function lossyContractPhase(d: Declared): Migration {
  return {
    up: (s) => contractUp(s, d),
    irreversible: `${quoted(d, final(d))} was filled through ${d.fn}, which loses what the old column held, so the values contract drops cannot be rebuilt from it`,
    validate: () => validate(d),
  };
}
