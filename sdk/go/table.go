package migr

import "context"

// builder accumulates the operations built against one object until Apply drains it.
type builder struct {
	schema *Schema
	// subject names what the operations act on, for the migration that forgot to apply them.
	subject string
	ops     []Operation
}

// add appends one operation to what this builder is holding.
func (b *builder) add(op Operation) {
	b.ops = append(b.ops, op)
}

// apply emits everything held and empties the builder, so applying twice does not emit twice.
func (b *builder) apply(ctx context.Context) error {
	held := b.ops
	b.ops = nil

	return b.schema.Apply(ctx, held...)
}

// held reports how many operations are built and not yet applied.
func (b *builder) held() int {
	return len(b.ops)
}

// Table is a change to an existing table, built up and then applied in one call.
type Table struct {
	build *builder
	name  string
}

// Apply emits everything built against this table, and returns the first problem found.
func (t *Table) Apply(ctx context.Context) error {
	return t.build.apply(ctx)
}

// Name returns the table these changes act on.
func (t *Table) Name() string {
	return t.name
}

// Drop removes the table and everything in it, which Was makes reversible.
func (t *Table) Drop() *Recorded[TableDef] {
	op := &DropTable{Table: t.name}
	t.build.add(op)

	return &Recorded[TableDef]{Table: t, record: func(d *TableDef) { op.From = d }}
}

// RenameTo renames the table, and every later change in the chain names it by its new name.
func (t *Table) RenameTo(to string) *Table {
	t.build.add(&RenameTable{From: t.name, To: to})
	t.name = to

	return t
}

// AddColumn adds a column, which is NOT NULL unless the chain says Null.
func (t *Table) AddColumn(name string, typ Type) *ColumnChange {
	op := &AddColumn{Table: t.name, Column: Column{Name: name, Type: typ}}
	t.build.add(op)

	return &ColumnChange{Table: t, column: &op.Column}
}

// DropColumn removes a column and the data it holds, which Was makes reversible.
func (t *Table) DropColumn(name string) *Recorded[Column] {
	op := &DropColumn{Table: t.name, Column: name}
	t.build.add(op)

	return &Recorded[Column]{Table: t, record: func(c *Column) { op.From = c }}
}

// RenameColumn renames a column, preserving its data.
func (t *Table) RenameColumn(from, to string) *Table {
	t.build.add(&RenameColumn{Table: t.name, From: from, To: to})

	return t
}

// AlterColumn changes a column's type, nullability, or default; at least one is required.
func (t *Table) AlterColumn(name string) *AlterChange {
	op := &AlterColumn{Table: t.name, Column: name}
	t.build.add(op)

	return &AlterChange{Table: t, op: op}
}

// AddIndex creates a non-unique index over the given columns.
func (t *Table) AddIndex(name string, columns ...string) *Table {
	t.build.add(&AddIndex{Table: t.name, Name: name, Columns: columns})

	return t
}

// DropIndex removes an index, which Was makes reversible.
func (t *Table) DropIndex(name string) *Recorded[ColumnsDef] {
	op := &DropIndex{Table: t.name, Name: name}
	t.build.add(op)

	return &Recorded[ColumnsDef]{Table: t, record: func(d *ColumnsDef) { op.From = d }}
}

// AddForeignKey references another table's columns, which OnDelete and OnUpdate qualify.
func (t *Table) AddForeignKey(name string, columns []string,
	referencesTable string, referencesColumns []string) *ForeignKeyChange {
	op := &AddForeignKey{
		Table: t.name, Name: name, Columns: columns,
		ReferencesTable: referencesTable, ReferencesColumns: referencesColumns,
	}
	t.build.add(op)

	return &ForeignKeyChange{Table: t, op: op}
}

// DropForeignKey removes a foreign key, which Was makes reversible.
func (t *Table) DropForeignKey(name string) *Recorded[ForeignKeyDef] {
	op := &DropForeignKey{Table: t.name, Name: name}
	t.build.add(op)

	return &Recorded[ForeignKeyDef]{Table: t, record: func(d *ForeignKeyDef) { op.From = d }}
}

// AddUnique adds a unique constraint over the given columns.
func (t *Table) AddUnique(name string, columns ...string) *Table {
	t.build.add(&AddUnique{Table: t.name, Name: name, Columns: columns})

	return t
}

// DropUnique removes a unique constraint, which Was makes reversible.
func (t *Table) DropUnique(name string) *Recorded[ColumnsDef] {
	op := &DropUnique{Table: t.name, Name: name}
	t.build.add(op)

	return &Recorded[ColumnsDef]{Table: t, record: func(d *ColumnsDef) { op.From = d }}
}

// AddCheck adds a check constraint whose expression is engine SQL and does not port.
func (t *Table) AddCheck(name, expression string) *Table {
	t.build.add(&AddCheck{Table: t.name, Name: name, Expression: expression})

	return t
}

// DropCheck removes a check constraint, which Was makes reversible.
func (t *Table) DropCheck(name string) *Recorded[CheckDef] {
	op := &DropCheck{Table: t.name, Name: name}
	t.build.add(op)

	return &Recorded[CheckDef]{Table: t, record: func(d *CheckDef) { op.From = d }}
}

// AddPrimaryKey keys a table that already exists; a new table states its key inline.
func (t *Table) AddPrimaryKey(name string, columns ...string) *Table {
	t.build.add(&AddPrimaryKey{Table: t.name, Name: name, Columns: columns})

	return t
}

// DropPrimaryKey removes a table's primary key, which Was makes reversible.
func (t *Table) DropPrimaryKey(name string) *Recorded[ColumnsDef] {
	op := &DropPrimaryKey{Table: t.name, Name: name}
	t.build.add(op)

	return &Recorded[ColumnsDef]{Table: t, record: func(d *ColumnsDef) { op.From = d }}
}

// Recorded is a change that may record what it replaced, so that a rollback can restore it.
type Recorded[T any] struct {
	*Table
	record func(*T)
}

// Was records what the operation replaced; without it the operation cannot be undone.
func (r *Recorded[T]) Was(prior T) *Table {
	r.record(&prior)

	return r.Table
}

// Columns names the columns a dropped index, unique constraint, or primary key covered.
func Columns(names ...string) ColumnsDef {
	return ColumnsDef{Columns: names}
}

// ColumnChange is a column being added to an existing table, and the modifiers it accepts.
type ColumnChange struct {
	*Table
	column *Column
}

// Null makes the column accept no value, which a column does not by default.
func (c *ColumnChange) Null() *ColumnChange {
	c.column.Nullable = true

	return c
}

// NotNull makes the column require a value, which is what an unqualified column already is.
func (c *ColumnChange) NotNull() *ColumnChange {
	c.column.Nullable = false

	return c
}

// Default gives the column a default, built with Literal or Call.
func (c *ColumnChange) Default(d *Default) *ColumnChange {
	c.column.Default = d

	return c
}

// AlterChange is a change to an existing column, and what it may record about the prior state.
type AlterChange struct {
	*Table
	op *AlterColumn
}

// Type changes the column's logical type, which is the change that may lose data.
func (a *AlterChange) Type(t Type) *AlterChange {
	a.op.Type = &t

	return a
}

// Null makes the column accept no value.
func (a *AlterChange) Null() *AlterChange {
	return a.setNullable(true)
}

// NotNull makes the column require a value.
func (a *AlterChange) NotNull() *AlterChange {
	return a.setNullable(false)
}

// setNullable records the nullability this change asks for, which absence would leave alone.
func (a *AlterChange) setNullable(nullable bool) *AlterChange {
	a.op.Nullable = &nullable

	return a
}

// Default gives the column a default, built with Literal or Call.
func (a *AlterChange) Default(d *Default) *AlterChange {
	a.op.Default = d

	return a
}

// DropDefault removes the column's existing default, which an absent Default would not.
func (a *AlterChange) DropDefault() *AlterChange {
	a.op.DropDefault = true

	return a
}

// WasType records the column's prior logical type, without which a retype cannot be undone.
func (a *AlterChange) WasType(t Type) *AlterChange {
	a.prior().Type = &t

	return a
}

// WasNullable records the column's prior nullability.
func (a *AlterChange) WasNullable(nullable bool) *AlterChange {
	a.prior().Nullable = &nullable

	return a
}

// WasDefault records the column's prior default.
func (a *AlterChange) WasDefault(d *Default) *AlterChange {
	a.prior().Default = d

	return a
}

// WasNoDefault records that the column had no default, which is what inverts setting one.
func (a *AlterChange) WasNoDefault() *AlterChange {
	a.prior().NoDefault = true

	return a
}

// prior returns the recorded prior state, starting one the first time something is recorded.
func (a *AlterChange) prior() *ColumnState {
	if a.op.From == nil {
		a.op.From = &ColumnState{}
	}

	return a.op.From
}

// ForeignKeyChange is a foreign key being added, and the referential actions it may carry.
type ForeignKeyChange struct {
	*Table
	op *AddForeignKey
}

// OnDelete says what happens to a referencing row when its referent is deleted.
func (f *ForeignKeyChange) OnDelete(a ReferentialAction) *ForeignKeyChange {
	f.op.OnDelete = a

	return f
}

// OnUpdate says what happens to a referencing row when its referent's key changes.
func (f *ForeignKeyChange) OnUpdate(a ReferentialAction) *ForeignKeyChange {
	f.op.OnUpdate = a

	return f
}

// NewTable is a table being created, with its columns and, optionally, its primary key.
type NewTable struct {
	build *builder
	op    *CreateTable
}

// Apply emits the table, and returns the first problem found building it.
func (n *NewTable) Apply(ctx context.Context) error {
	return n.build.apply(ctx)
}

// Column declares a column, which is NOT NULL unless the chain says Null.
func (n *NewTable) Column(name string, typ Type) *NewColumn {
	n.op.Columns = append(n.op.Columns, Column{Name: name, Type: typ})

	return &NewColumn{NewTable: n, at: len(n.op.Columns) - 1}
}

// PrimaryKey states the new table's key inline, which is how a new table states one.
func (n *NewTable) PrimaryKey(columns ...string) *NewTable {
	n.op.PrimaryKey = columns

	return n
}

// NewColumn is a column of a table being created, and the modifiers it accepts.
type NewColumn struct {
	*NewTable
	at int
}

// Null makes the column accept no value, which a column does not by default.
func (c *NewColumn) Null() *NewColumn {
	c.declared().Nullable = true

	return c
}

// NotNull makes the column require a value, which is what an unqualified column already is.
func (c *NewColumn) NotNull() *NewColumn {
	c.declared().Nullable = false

	return c
}

// Default gives the column a default, built with Literal or Call.
func (c *NewColumn) Default(d *Default) *NewColumn {
	c.declared().Default = d

	return c
}

// declared returns the column this change modifies, which the table holds by value.
func (c *NewColumn) declared() *Column {
	return &c.op.Columns[c.at]
}

// Backfill fills the NULLs of a column with a literal or a neutral function, and touches nothing else.
func (t *Table) Backfill(column string, typ Type, value *Default) *Table {
	t.build.add(&DataBackfill{Table: t.name, Column: column, Type: typ, Value: value})

	return t
}

// Transform rewrites a column of every row from another column, through a neutral function.
func (t *Table) Transform(column string, typ Type, from string, f TransformFunction) *Table {
	t.build.add(&DataTransform{Table: t.name, Column: column, Type: typ, From: from, Function: f})

	return t
}

// CopyInto inserts every row of this table into another, column by named column.
func (t *Table) CopyInto(to string, columns ...CopyColumn) *Table {
	t.build.add(&DataCopy{From: t.name, To: to, Columns: columns})

	return t
}
