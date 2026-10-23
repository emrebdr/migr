package diff

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// A confirmed rename says the object in one schema named X is the object in the other named Y. So
// the whole feature is a pre-pass: emit the rename, rewrite the schema being changed as the engine
// would have left it, and hand the rest to a differ that knows nothing about any of this. Measured
// on all five servers, that rewrite is exactly what each one does -- an index follows a renamed
// column and keeps its own name, and a foreign key in another table follows a renamed table.
//
// What is deliberately not rewritten is a check expression and a view body, both being engine text.
// Every server rewrites those itself, and a textual substitution here would be the guess
// this package refuses -- a column name appears inside string literals and other tables' columns
// too. So a check or a view over a renamed object is proposed as a drop and an add, or as a
// replacement, which lands on the target schema either way.

// planRenames emits the confirmed renames and leaves behind the schema they produce, or refuses them.
func (d *differ) planRenames(confirmed Renames) {
	for _, one := range d.checkedTableRenames(confirmed.Tables) {
		d.emit(ir.RenameTable{From: one.From, To: one.To})
		d.from = renamedTable(d.from, one)
	}

	for _, one := range d.checkedColumnRenames(confirmed.Columns) {
		d.emit(ir.RenameColumn{Table: one.Table, From: one.From, To: one.To})
		d.from = renamedColumn(d.from, one)
	}
}

// renamedBy returns the schema a confirmed set of renames leaves, with anything it cannot do left be.
func renamedBy(from, to schema.Snapshot, confirmed Renames) schema.Snapshot {
	d := &differ{from: from, to: to, buckets: map[ir.Kind]ir.Operations{}}
	d.planRenames(confirmed)

	return d.from
}

// checkedTableRenames returns the table renames these two schemas support, refusing the rest.
func (d *differ) checkedTableRenames(confirmed []TableRename) []TableRename {
	kept := make([]TableRename, 0, len(confirmed))
	seen := newClaims()

	for _, one := range confirmed {
		if problem := d.tableRenameProblem(one, seen); problem != "" {
			d.refuse(ir.KindRenameTable, one.From, problem)

			continue
		}

		kept = append(kept, one)
	}

	return kept
}

// A confirmed rename arrives from outside, so it is checked rather than trusted -- and a rename this
// comparison cannot honor is a refusal rather than an error, because the diff still has an answer:
// the drop and the create it would have proposed anyway, which invariant #7 then gates. An operator
// who confirmed a rename and whose schema moved underneath reads it in the same list as everything
// else, which is the shape this package already has.

// tableRenameProblem reports what stops one confirmed table rename from being honored.
func (d *differ) tableRenameProblem(one TableRename, seen *claims) string {
	if problem := seen.claim(tableScope, one.From, one.To); problem != "" {
		return problem
	}

	if _, held := d.from.Table(one.From); !held {
		return fmt.Sprintf("the schema being changed holds no table %q to rename", one.From)
	}

	if _, held := d.to.Table(one.To); !held {
		return fmt.Sprintf("the target schema holds no table %q to rename it to", one.To)
	}

	if _, kept := d.to.Table(one.From); kept {
		return fmt.Sprintf("the target schema still holds a table %q, so that table is not the one "+
			"being renamed", one.From)
	}

	if _, taken := d.from.Table(one.To); taken {
		return fmt.Sprintf("the schema being changed already holds a table %q, and no engine "+
			"renames one onto a name that is taken: drop or rename %q in a migration of its own "+
			"first", one.To, one.To)
	}

	return ""
}

// checkedColumnRenames returns the column renames these two schemas support, refusing the rest.
func (d *differ) checkedColumnRenames(confirmed []ColumnRename) []ColumnRename {
	kept := make([]ColumnRename, 0, len(confirmed))
	seen := newClaims()

	for _, one := range confirmed {
		if problem := d.columnRenameProblem(one, seen); problem != "" {
			d.refuse(ir.KindRenameColumn, qualified(one.Table, one.From), problem)

			continue
		}

		kept = append(kept, one)
	}

	return kept
}

// columnRenameProblem reports what stops one confirmed column rename from being honored.
func (d *differ) columnRenameProblem(one ColumnRename, seen *claims) string {
	if problem := seen.claim(one.Table, one.From, one.To); problem != "" {
		return problem
	}

	before, held := d.from.Table(one.Table)
	if !held {
		return fmt.Sprintf("the schema being changed holds no table %q, so there is no column to "+
			"rename -- a column rename names its table by the name the target schema holds it "+
			"under, which a confirmed table rename has already established", one.Table)
	}

	after, held := d.to.Table(one.Table)
	if !held {
		return fmt.Sprintf("the target schema holds no table %q", one.Table)
	}

	return columnPairProblem(one, before, after)
}

// columnPairProblem reports which end of a confirmed column rename its two tables do not support.
func columnPairProblem(one ColumnRename, before, after schema.Table) string {
	if _, held := before.Column(one.From); !held {
		return fmt.Sprintf("table %q holds no column %q to rename", one.Table, one.From)
	}

	if _, held := after.Column(one.To); !held {
		return fmt.Sprintf("the target schema's table %q holds no column %q to rename it to",
			one.Table, one.To)
	}

	if _, kept := after.Column(one.From); kept {
		return fmt.Sprintf("the target schema's table %q still holds a column %q, so that column "+
			"is not the one being renamed", one.Table, one.From)
	}

	if _, taken := before.Column(one.To); taken {
		return fmt.Sprintf("table %q already holds a column %q, and no engine renames one onto a "+
			"name that is taken", one.Table, one.To)
	}

	return ""
}

// tableScope is what table renames are recorded under, and is no name a table could have.
const tableScope = "\x00tables"

// claims records which names a set of renames has already spoken for, within one table or schema.
type claims struct {
	// taken maps a scope and a name to whether some rename already names it.
	taken map[string]bool
}

// newClaims returns an empty record of what a set of confirmed renames has claimed.
func newClaims() *claims {
	return &claims{taken: make(map[string]bool)}
}

// claim records one rename's two ends, reporting a name a rename in the same set already claimed.
func (c *claims) claim(scope, from, to string) string {
	for _, one := range []struct{ side, name string }{{"renames", from}, {"renames onto", to}} {
		key := scope + "\x00" + one.side + "\x00" + one.name
		if c.taken[key] {
			return fmt.Sprintf("another confirmed rename already %s %q, and two renames cannot "+
				"name one object", one.side, one.name)
		}

		c.taken[key] = true
	}

	return ""
}

// renamedTable returns the schema one confirmed table rename leaves, references included.
func renamedTable(held schema.Snapshot, one TableRename) schema.Snapshot {
	rewritten := held

	rewritten.Tables = mapped(held.Tables, func(table schema.Table) schema.Table {
		if table.Name == one.From {
			table.Name = one.To
		}

		table.ForeignKeys = mapped(table.ForeignKeys, func(key schema.ForeignKey) schema.ForeignKey {
			if key.ReferencesTable == one.From {
				key.ReferencesTable = one.To
			}

			return key
		})

		return table
	})

	return rewritten.Canonical()
}

// renamedColumn returns the schema one confirmed column rename leaves, every reference included.
func renamedColumn(held schema.Snapshot, one ColumnRename) schema.Snapshot {
	rewritten := held

	rewritten.Tables = mapped(held.Tables, func(table schema.Table) schema.Table {
		if table.Name == one.Table {
			return renamedWithin(table, one.From, one.To)
		}

		return referencing(table, one.Table, one.From, one.To)
	})

	return rewritten.Canonical()
}

// renamedWithin returns the table a column rename happened in, with every reference to it rewritten.
func renamedWithin(table schema.Table, from, to string) schema.Table {
	table.Columns = mapped(table.Columns, func(column schema.Column) schema.Column {
		if column.Name == from {
			column.Name = to
		}

		return column
	})

	table.PrimaryKey = renamedKey(table.PrimaryKey, from, to)

	table.Indexes = mapped(table.Indexes, func(index schema.Index) schema.Index {
		index.Columns = renamedIn(index.Columns, from, to)

		return index
	})

	table.Uniques = mapped(table.Uniques, func(unique schema.Unique) schema.Unique {
		unique.Columns = renamedIn(unique.Columns, from, to)

		return unique
	})

	table.ForeignKeys = mapped(table.ForeignKeys, func(key schema.ForeignKey) schema.ForeignKey {
		key.Columns = renamedIn(key.Columns, from, to)

		return key
	})

	return table
}

// referencing returns another table with every reference it makes to a renamed column rewritten.
func referencing(table schema.Table, owner, from, to string) schema.Table {
	table.ForeignKeys = mapped(table.ForeignKeys, func(key schema.ForeignKey) schema.ForeignKey {
		if key.ReferencesTable == owner {
			key.ReferencesColumns = renamedIn(key.ReferencesColumns, from, to)
		}

		return key
	})

	return table
}

// renamedKey returns a primary key over a renamed column, a table without one having none.
func renamedKey(key *schema.PrimaryKey, from, to string) *schema.PrimaryKey {
	if key == nil {
		return nil
	}

	moved := *key
	moved.Columns = renamedIn(key.Columns, from, to)

	return &moved
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
func mapped[T any](held []T, rewrite func(T) T) []T {
	if held == nil {
		return nil
	}

	rewritten := make([]T, 0, len(held))
	for _, one := range held {
		rewritten = append(rewritten, rewrite(one))
	}

	return rewritten
}
