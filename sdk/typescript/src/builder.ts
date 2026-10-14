// The four chains a migration describes a change through.
//
// The builder is sugar over Schema.apply, never a second path: a chain and the operation written
// out emit the same bytes, which builder.test.ts asserts for all twenty. Nothing is emitted until
// apply, which sends the whole chain in one call and empties it, so applying twice does not emit
// twice.
//
// docs/ir.md §7.2 names two conventions no validation can check, and both are structural here
// rather than remembered: a column is NOT NULL unless the chain says null, and a new table states
// its key inline where an existing one states it as an operation -- different calls, so the wrong
// choice is not spellable.
//
// A change builder is a view of its chain rather than a separate object: Object.create puts the
// chain on its prototype, so its own modifiers are its own and everything else is the chain's. That
// is the Go SDK's struct embedding, and it works here only because every method is a closure over
// the shared builder -- nothing reads `this`, so a view cannot shadow what it inherits.

import type { Action, Column, Default } from "./column.ts";
import type {
  CheckDef,
  ColumnState,
  ColumnsDef,
  ForeignKeyDef,
  TableDef,
  ViewDef,
} from "./define.ts";
import type {
  AddForeignKey,
  AlterColumn,
  CopyColumn,
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
  Transform,
} from "./operation.ts";
import type { Type } from "./type.ts";

/** Sink is what a chain hands its operations to, which the window supplies. */
export type Sink = (ops: Operation[]) => Promise<void>;

/** Builder accumulates the operations built against one object until apply drains it. */
export interface Builder {
  /** What the operations act on, for the migration that forgot to apply them. */
  readonly subject: string;
  /** The object's current name, which a rename moves and every view of the chain reads. */
  name: string;
  /** Appends one operation to what this builder is holding. */
  add(op: Operation): void;
  /** Emits everything held and empties the builder, so applying twice does not emit twice. */
  apply(): Promise<void>;
  /** Reports how many operations are built and not yet applied. */
  held(): number;
}

/** newBuilder opens a builder over one object, which the window holds to having been applied. */
export function newBuilder(sink: Sink, subject: string, name: string): Builder {
  let held: Operation[] = [];

  return {
    subject,
    name,
    add(op: Operation): void {
      held.push(op);
    },
    apply(): Promise<void> {
      const sending = held;
      held = [];

      return sink(sending);
    },
    held(): number {
      return held.length;
    },
  };
}

/** TableChain is a change to an existing table, built up and then applied in one call. */
export interface TableChain {
  /** The table these changes act on, which a rename moves. */
  readonly name: string;
  /** Emits everything built against this table. */
  apply(): Promise<void>;
  /** Removes the table and everything in it, which was makes reversible. */
  drop(): Recorded<TableDef>;
  /** Renames the table, and every later change in the chain names it by its new name. */
  renameTo(to: string): TableChain;
  /** Adds a column, which is NOT NULL unless the chain says null. */
  addColumn(name: string, type: Type): ColumnChange;
  /** Removes a column and the data it holds, which was makes reversible. */
  dropColumn(name: string): Recorded<Column>;
  /** Renames a column, preserving its data. */
  renameColumn(from: string, to: string): TableChain;
  /** Changes a column's type, nullability, or default; at least one is required. */
  alterColumn(name: string): AlterChange;
  /** Creates a non-unique index over the given columns. */
  addIndex(name: string, ...columns: string[]): TableChain;
  /** Removes an index, which was makes reversible. */
  dropIndex(name: string): Recorded<ColumnsDef>;
  /** References another table's columns, which onDelete and onUpdate qualify. */
  addForeignKey(
    name: string,
    columns: string[],
    referencesTable: string,
    referencesColumns: string[],
  ): ForeignKeyChange;
  /** Removes a foreign key, which was makes reversible. */
  dropForeignKey(name: string): Recorded<ForeignKeyDef>;
  /** Adds a unique constraint over the given columns. */
  addUnique(name: string, ...columns: string[]): TableChain;
  /** Removes a unique constraint, which was makes reversible. */
  dropUnique(name: string): Recorded<ColumnsDef>;
  /** Adds a check constraint whose expression is engine SQL and does not port. */
  addCheck(name: string, expression: string): TableChain;
  /** Removes a check constraint, which was makes reversible. */
  dropCheck(name: string): Recorded<CheckDef>;
  /** Keys a table that already exists; a new table states its key inline. */
  addPrimaryKey(name: string, ...columns: string[]): TableChain;
  /** Removes a table's primary key, which was makes reversible. */
  dropPrimaryKey(name: string): Recorded<ColumnsDef>;
  /** Fills the NULLs of a column with a literal or a neutral function, and touches nothing else. */
  backfill(column: string, type: Type, value: Default): TableChain;
  /** Rewrites a column of every row from another column, through a neutral function. */
  transform(column: string, type: Type, from: string, fn: Transform): TableChain;
  /** Inserts every row of this table into another, column by named column. */
  copyInto(to: string, ...columns: CopyColumn[]): TableChain;
}

/** Recorded is a change that may record what it replaced, so that a rollback can restore it. */
export interface Recorded<T> extends TableChain {
  /** Records what the operation replaced; without it the operation cannot be undone. */
  was(prior: T): TableChain;
}

/** ColumnChange is a column being added to an existing table, and the modifiers it accepts. */
export interface ColumnChange extends TableChain {
  /** Makes the column accept no value, which a column does not by default. */
  null(): ColumnChange;
  /** Makes the column require a value, which is what an unqualified column already is. */
  notNull(): ColumnChange;
  /** Gives the column a default, built with literal or call. */
  default(value: Default): ColumnChange;
}

/** AlterChange is a change to an existing column, and what it may record about the prior state. */
export interface AlterChange extends TableChain {
  /** Changes the column's logical type, which is the change that may lose data. */
  type(of: Type): AlterChange;
  /** Makes the column accept no value. */
  null(): AlterChange;
  /** Makes the column require a value. */
  notNull(): AlterChange;
  /** Gives the column a default, built with literal or call. */
  default(value: Default): AlterChange;
  /** Removes the column's existing default, which an absent default would not. */
  dropDefault(): AlterChange;
  /** Records the column's prior logical type, without which a retype cannot be undone. */
  wasType(of: Type): AlterChange;
  /** Records the column's prior nullability. */
  wasNullable(nullable: boolean): AlterChange;
  /** Records the column's prior default. */
  wasDefault(value: Default): AlterChange;
  /** Records that the column had no default, which is what inverts setting one. */
  wasNoDefault(): AlterChange;
}

/** ForeignKeyChange is a foreign key being added, and the referential actions it may carry. */
export interface ForeignKeyChange extends TableChain {
  /** Says what happens to a referencing row when its referent is deleted. */
  onDelete(action: Action): ForeignKeyChange;
  /** Says what happens to a referencing row when its referent's key changes. */
  onUpdate(action: Action): ForeignKeyChange;
}

/** NewTable is a table being created, with its columns and, optionally, its primary key. */
export interface NewTable {
  /** Emits the table being created. */
  apply(): Promise<void>;
  /** Declares a column, which is NOT NULL unless the chain says null. */
  column(name: string, type: Type): NewColumn;
  /** States the new table's key inline, which is how a new table states one. */
  primaryKey(...columns: string[]): NewTable;
}

/** NewColumn is a column of a table being created, and the modifiers it accepts. */
export interface NewColumn extends NewTable {
  /** Makes the column accept no value, which a column does not by default. */
  null(): NewColumn;
  /** Makes the column require a value, which is what an unqualified column already is. */
  notNull(): NewColumn;
  /** Gives the column a default, built with literal or call. */
  default(value: Default): NewColumn;
}

/** ViewChain is a change to a view, built up and then applied in one call. */
export interface ViewChain {
  /** The view these changes act on. */
  readonly name: string;
  /** Emits everything built against this view. */
  apply(): Promise<void>;
  /** Defines the view, whose query is engine SQL and does not port. */
  create(query: string): ViewChain;
  /** Overwrites an existing view, which was makes reversible. */
  replace(query: string): ViewRecord;
  /** Removes the view, which was makes reversible. */
  drop(): ViewRecord;
}

/** ViewRecord is a view change that may record the query it replaced. */
export interface ViewRecord extends ViewChain {
  /** Records the query the view was defined by; without it the change cannot be undone. */
  was(query: string): ViewChain;
}

/** Statements are raw statements addressed to one engine by name, the IR's escape hatch. */
export interface Statements {
  /** Emits every statement built here. */
  apply(): Promise<void>;
  /** Adds one more statement, in the named engine's own dialect. */
  raw(driver: string, sql: string): Statements;
}

/** columns names the columns a dropped index, unique constraint, or primary key covered. */
export function columns(...names: string[]): ColumnsDef {
  return { columns: names };
}

/** extend returns a view of one chain that carries its own modifiers, as Go's embedding does. */
function extend<P extends object, E extends object>(chain: P, modifiers: E): P & E {
  return Object.assign(Object.create(chain) as P, modifiers);
}

/** tableChain opens a chain over a table that already exists. */
export function tableChain(build: Builder): TableChain {
  const chain: TableChain = {
    get name(): string {
      return build.name;
    },
    apply: () => build.apply(),
    backfill: (column: string, type: Type, value: Default) => {
      build.add({ op: "data_backfill", table: build.name, column, type, value });

      return chain;
    },
    transform: (column: string, type: Type, from: string, fn: Transform) => {
      build.add({ op: "data_transform", table: build.name, column, type, from, function: fn });

      return chain;
    },
    copyInto: (to: string, ...columns: CopyColumn[]) => {
      build.add({ op: "data_copy", from: build.name, to, columns });

      return chain;
    },
    drop: () => {
      const op: DropTable = { op: "drop_table", table: build.name };
      build.add(op);

      return recorded<TableDef>(chain, (prior) => {
        op.from = prior;
      });
    },
    renameTo: (to: string) => {
      build.add({ op: "rename_table", from: build.name, to });
      build.name = to;

      return chain;
    },
    addColumn: (name: string, type: Type) => {
      const column: Column = { name, type };
      build.add({ op: "add_column", table: build.name, column });

      return columnChange(chain, column);
    },
    dropColumn: (name: string) => {
      const op: DropColumn = { op: "drop_column", table: build.name, column: name };
      build.add(op);

      return recorded<Column>(chain, (prior) => {
        op.from = prior;
      });
    },
    renameColumn: (from: string, to: string) => {
      build.add({ op: "rename_column", table: build.name, from, to });

      return chain;
    },
    alterColumn: (name: string) => {
      const op: AlterColumn = { op: "alter_column", table: build.name, column: name };
      build.add(op);

      return alterChange(chain, op);
    },
    addIndex: (name: string, ...over: string[]) => {
      build.add({ op: "add_index", table: build.name, name, columns: over });

      return chain;
    },
    dropIndex: (name: string) => {
      const op: DropIndex = { op: "drop_index", table: build.name, name };
      build.add(op);

      return recorded<ColumnsDef>(chain, (prior) => {
        op.from = prior;
      });
    },
    addForeignKey: (
      name: string,
      over: string[],
      referencesTable: string,
      referencesColumns: string[],
    ) => {
      const op: AddForeignKey = {
        op: "add_foreign_key",
        table: build.name,
        name,
        columns: over,
        references_table: referencesTable,
        references_columns: referencesColumns,
      };
      build.add(op);

      return foreignKeyChange(chain, op);
    },
    dropForeignKey: (name: string) => {
      const op: DropForeignKey = { op: "drop_foreign_key", table: build.name, name };
      build.add(op);

      return recorded<ForeignKeyDef>(chain, (prior) => {
        op.from = prior;
      });
    },
    addUnique: (name: string, ...over: string[]) => {
      build.add({ op: "add_unique", table: build.name, name, columns: over });

      return chain;
    },
    dropUnique: (name: string) => {
      const op: DropUnique = { op: "drop_unique", table: build.name, name };
      build.add(op);

      return recorded<ColumnsDef>(chain, (prior) => {
        op.from = prior;
      });
    },
    addCheck: (name: string, expression: string) => {
      build.add({ op: "add_check", table: build.name, name, expression });

      return chain;
    },
    dropCheck: (name: string) => {
      const op: DropCheck = { op: "drop_check", table: build.name, name };
      build.add(op);

      return recorded<CheckDef>(chain, (prior) => {
        op.from = prior;
      });
    },
    addPrimaryKey: (name: string, ...over: string[]) => {
      build.add({ op: "add_primary_key", table: build.name, name, columns: over });

      return chain;
    },
    dropPrimaryKey: (name: string) => {
      const op: DropPrimaryKey = { op: "drop_primary_key", table: build.name, name };
      build.add(op);

      return recorded<ColumnsDef>(chain, (prior) => {
        op.from = prior;
      });
    },
  };

  return chain;
}

/** recorded returns a change that may record what it replaced, over the chain that built it. */
function recorded<T>(chain: TableChain, record: (prior: T) => void): Recorded<T> {
  const change: Recorded<T> = extend(chain, {
    was: (prior: T): TableChain => {
      record(prior);

      return chain;
    },
  });

  return change;
}

/** columnChange returns the modifiers a column being added accepts, over the chain that added it. */
function columnChange(chain: TableChain, column: Column): ColumnChange {
  const change: ColumnChange = extend(chain, {
    null: (): ColumnChange => {
      column.nullable = true;

      return change;
    },
    notNull: (): ColumnChange => {
      column.nullable = false;

      return change;
    },
    default: (value: Default): ColumnChange => {
      column.default = value;

      return change;
    },
  });

  return change;
}

/** alterChange returns the modifiers an alteration accepts, and what it may record. */
function alterChange(chain: TableChain, op: AlterColumn): AlterChange {
  const prior = (): ColumnState => (op.from ??= {});

  const change: AlterChange = extend(chain, {
    type: (of: Type): AlterChange => {
      op.type = of;

      return change;
    },
    null: (): AlterChange => {
      op.nullable = true;

      return change;
    },
    notNull: (): AlterChange => {
      op.nullable = false;

      return change;
    },
    default: (value: Default): AlterChange => {
      op.default = value;

      return change;
    },
    dropDefault: (): AlterChange => {
      op.drop_default = true;

      return change;
    },
    wasType: (of: Type): AlterChange => {
      prior().type = of;

      return change;
    },
    wasNullable: (nullable: boolean): AlterChange => {
      prior().nullable = nullable;

      return change;
    },
    wasDefault: (value: Default): AlterChange => {
      prior().default = value;

      return change;
    },
    wasNoDefault: (): AlterChange => {
      prior().no_default = true;

      return change;
    },
  });

  return change;
}

/** foreignKeyChange returns the referential actions a foreign key being added may carry. */
function foreignKeyChange(chain: TableChain, op: AddForeignKey): ForeignKeyChange {
  const change: ForeignKeyChange = extend(chain, {
    onDelete: (action: Action): ForeignKeyChange => {
      op.on_delete = action;

      return change;
    },
    onUpdate: (action: Action): ForeignKeyChange => {
      op.on_update = action;

      return change;
    },
  });

  return change;
}

/** newTableChain opens a chain over a table being created, which states its key inline. */
export function newTableChain(build: Builder, op: CreateTable): NewTable {
  const chain: NewTable = {
    apply: () => build.apply(),
    column: (name: string, type: Type) => {
      const column: Column = { name, type };
      op.columns.push(column);

      return newColumn(chain, column);
    },
    primaryKey: (...over: string[]) => {
      op.primary_key = over;

      return chain;
    },
  };

  return chain;
}

/** newColumn returns the modifiers a column of a table being created accepts. */
function newColumn(chain: NewTable, column: Column): NewColumn {
  const change: NewColumn = extend(chain, {
    null: (): NewColumn => {
      column.nullable = true;

      return change;
    },
    notNull: (): NewColumn => {
      column.nullable = false;

      return change;
    },
    default: (value: Default): NewColumn => {
      column.default = value;

      return change;
    },
  });

  return change;
}

/** viewChain opens a chain over a view. */
export function viewChain(build: Builder): ViewChain {
  const chain: ViewChain = {
    get name(): string {
      return build.name;
    },
    apply: () => build.apply(),
    create: (query: string) => {
      build.add({ op: "create_view", view: build.name, query });

      return chain;
    },
    replace: (query: string) => {
      const op: CreateView = {
        op: "create_view",
        view: build.name,
        query,
        or_replace: true,
      };
      build.add(op);

      return viewRecord(chain, (prior) => {
        op.from = prior;
      });
    },
    drop: () => {
      const op: DropView = { op: "drop_view", view: build.name };
      build.add(op);

      return viewRecord(chain, (prior) => {
        op.from = prior;
      });
    },
  };

  return chain;
}

/** viewRecord returns a view change that may record the query it replaced. */
function viewRecord(chain: ViewChain, record: (prior: ViewDef) => void): ViewRecord {
  const change: ViewRecord = extend(chain, {
    was: (query: string): ViewChain => {
      record({ query });

      return chain;
    },
  });

  return change;
}

/** statementsChain opens a chain of raw statements, each addressed to one engine by name. */
export function statementsChain(build: Builder): Statements {
  const chain: Statements = {
    apply: () => build.apply(),
    raw: (driver: string, sql: string) => {
      build.add({ op: "raw", driver, sql });

      return chain;
    },
  };

  return chain;
}
