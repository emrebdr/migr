package fold

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// A projected column type carries the logical type as its own native spelling, because
// schema.ColumnType requires one and a projection has never seen an engine. Nothing downstream can
// tell: ColumnType.Equal compares two exact types on the logical type alone, and internal/diff
// reads Logical(). What it does mean is stated rather than hidden -- a projected type is always
// exact, so meeting a live type the catalog could only guess at is a refusal by design (ADR 0070,
// C1). Measured: SQLite declares int16, int32 and float32 ambiguously and MySQL declares timestamp
// and timestamptz ambiguously, so those columns are refused rather than compared.

// projected renders one of an operation's columns as a snapshot holds it.
func projected(column ir.Column) (schema.Column, string) {
	typ, err := schema.ExactType(column.Type, column.Type.String())
	if err != nil {
		return schema.Column{}, fmt.Sprintf("its type %s is not one a schema can hold: %s",
			column.Type, err)
	}

	return schema.Column{
		Name:     column.Name,
		Type:     typ,
		Nullable: column.Nullable,
		Default:  projectedDefault(column.Default),
	}, ""
}

// projectedDefault renders an operation's default as a snapshot holds one, which is never engine SQL.
func projectedDefault(value *ir.Default) *schema.ColumnDefault {
	switch {
	case value == nil:
		return nil
	case value.Literal != nil:
		held := *value.Literal

		return &schema.ColumnDefault{Literal: &held}
	case value.Function != "":
		return &schema.ColumnDefault{Function: value.Function}
	default:
		return nil
	}
}

// addColumn folds a column onto the table it names.
func (f *folder) addColumn(at int32, op ir.AddColumn) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if _, held := table.Column(op.Column.Name); held {
			return table, fmt.Sprintf("it already holds a column %q", op.Column.Name)
		}

		column, problem := projected(op.Column)
		if problem != "" {
			return table, problem
		}

		table.Columns = append(table.Columns, column)

		return table, ""
	})
}

// dropColumn folds a column off the table it names, with every constraint that covered it.
func (f *folder) dropColumn(at int32, op ir.DropColumn) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if _, held := table.Column(op.Column); !held {
			return table, fmt.Sprintf("it holds no column %q", op.Column)
		}

		table.Columns = withoutColumn(table.Columns, op.Column)

		return uncovered(table, op.Column), ""
	})
}

// Every engine drops the constraints a dropped column was part of along with it -- that is M3-T7's
// own measurement, the gap engine.Unrestorable's implied half exists to refuse. So a projection that
// kept them would describe a schema no server would produce.

// uncovered returns a table with every constraint that covered one column gone with it.
func uncovered(table schema.Table, column string) schema.Table {
	table.Indexes = keptIndexes(table.Indexes, column)
	table.Uniques = keptUniques(table.Uniques, column)
	table.ForeignKeys = keptForeignKeys(table.ForeignKeys, column)

	if table.PrimaryKey != nil && covers(table.PrimaryKey.Columns, column) {
		table.PrimaryKey = nil
	}

	return table
}

// keptIndexes returns the indexes that do not cover a dropped column.
func keptIndexes(held []schema.Index, column string) []schema.Index {
	var kept []schema.Index

	for _, one := range held {
		if !covers(one.Columns, column) {
			kept = append(kept, one)
		}
	}

	return kept
}

// keptUniques returns the unique constraints that do not cover a dropped column.
func keptUniques(held []schema.Unique, column string) []schema.Unique {
	var kept []schema.Unique

	for _, one := range held {
		if !covers(one.Columns, column) {
			kept = append(kept, one)
		}
	}

	return kept
}

// keptForeignKeys returns the foreign keys that do not cover a dropped column.
func keptForeignKeys(held []schema.ForeignKey, column string) []schema.ForeignKey {
	var kept []schema.ForeignKey

	for _, one := range held {
		if !covers(one.Columns, column) {
			kept = append(kept, one)
		}
	}

	return kept
}

// covers reports whether a column list names one column.
func covers(columns []string, column string) bool {
	for _, one := range columns {
		if one == column {
			return true
		}
	}

	return false
}

// withoutColumn returns a column list with one of them gone.
func withoutColumn(columns []schema.Column, name string) []schema.Column {
	kept := make([]schema.Column, 0, len(columns))

	for _, column := range columns {
		if column.Name != name {
			kept = append(kept, column)
		}
	}

	return kept
}

// renameColumn folds a column's new name onto its table, and onto every reference to it.
func (f *folder) renameColumn(at int32, op ir.RenameColumn) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if _, held := table.Column(op.From); !held {
			return table, fmt.Sprintf("it holds no column %q", op.From)
		}

		if _, taken := table.Column(op.To); taken {
			return table, fmt.Sprintf("it already holds a column %q", op.To)
		}

		return renamedWithin(table, op.From, op.To), ""
	})

	f.repointColumn(op.Table, op.From, op.To)
}

// repointColumn rewrites every other table's reference to a renamed column, as each engine does.
func (f *folder) repointColumn(owner, from, to string) {
	for i := range f.held.Tables {
		if f.held.Tables[i].Name == owner {
			continue
		}

		for j := range f.held.Tables[i].ForeignKeys {
			key := &f.held.Tables[i].ForeignKeys[j]
			if key.ReferencesTable == owner {
				key.ReferencesColumns = renamedIn(key.ReferencesColumns, from, to)
			}
		}
	}
}

// renamedWithin returns the table a column rename happened in, with every reference to it rewritten.
func renamedWithin(table schema.Table, from, to string) schema.Table {
	table.Columns = mapped(table.Columns, func(column schema.Column) schema.Column {
		if column.Name == from {
			column.Name = to
		}

		return column
	})

	if table.PrimaryKey != nil {
		moved := *table.PrimaryKey
		moved.Columns = renamedIn(moved.Columns, from, to)
		table.PrimaryKey = &moved
	}

	table.Indexes = mapped(table.Indexes, func(one schema.Index) schema.Index {
		one.Columns = renamedIn(one.Columns, from, to)

		return one
	})

	table.Uniques = mapped(table.Uniques, func(one schema.Unique) schema.Unique {
		one.Columns = renamedIn(one.Columns, from, to)

		return one
	})

	table.ForeignKeys = mapped(table.ForeignKeys, func(one schema.ForeignKey) schema.ForeignKey {
		one.Columns = renamedIn(one.Columns, from, to)

		return one
	})

	return table
}

// renamedIn returns a column list with one name replaced, in the order the list holds them.
func renamedIn(columns []string, from, to string) []string {
	return mapped(columns, func(column string) string {
		if column == from {
			return to
		}

		return column
	})
}

// mapped returns a new slice of what a rewrite makes of each element, leaving the original alone.
func mapped[T, R any](held []T, rewrite func(T) R) []R {
	if held == nil {
		return nil
	}

	rewritten := make([]R, 0, len(held))
	for _, one := range held {
		rewritten = append(rewritten, rewrite(one))
	}

	return rewritten
}

// alterColumn folds one column's changed attributes onto it, leaving the rest as they are.
func (f *folder) alterColumn(at int32, op ir.AlterColumn) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		held, carries := table.Column(op.Column)
		if !carries {
			return table, fmt.Sprintf("it holds no column %q", op.Column)
		}

		altered, problem := altered(held, op)
		if problem != "" {
			return table, problem
		}

		table.Columns = mapped(table.Columns, func(column schema.Column) schema.Column {
			if column.Name == op.Column {
				return altered
			}

			return column
		})

		return table, ""
	})
}

// altered returns one column with what an alteration states about it changed, and nothing else.
func altered(held schema.Column, op ir.AlterColumn) (schema.Column, string) {
	if op.Type != nil {
		typ, err := schema.ExactType(*op.Type, op.Type.String())
		if err != nil {
			return schema.Column{}, fmt.Sprintf("its new type %s is not one a schema can hold: %s",
				op.Type, err)
		}

		held.Type = typ
	}

	if op.Nullable != nil {
		held.Nullable = *op.Nullable
	}

	if op.Default != nil {
		held.Default = projectedDefault(op.Default)
	}

	if op.DropDefault {
		held.Default = nil
	}

	return held, ""
}
