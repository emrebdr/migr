package fold

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// Every constraint an operation adds is checked against the columns the schema so far actually
// holds, which is what makes the same fold readable as M10-T3's reference-integrity check: an index
// over a column nothing created is a refusal naming both, rather than a constraint describing a
// schema no server would accept.

// addIndex folds an index onto the table it names.
func (f *folder) addIndex(at int32, op ir.AddIndex) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if problem := coverable(table, op.Columns); problem != "" {
			return table, problem
		}

		if named(indexNames(table.Indexes), op.Name) {
			return table, fmt.Sprintf("it already carries an index %q", op.Name)
		}

		table.Indexes = append(table.Indexes,
			schema.Index{Name: op.Name, Columns: op.Columns})

		return table, ""
	})
}

// dropIndex folds an index off the table it names.
func (f *folder) dropIndex(at int32, op ir.DropIndex) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if !named(indexNames(table.Indexes), op.Name) {
			return table, fmt.Sprintf("it carries no index %q", op.Name)
		}

		var kept []schema.Index

		for _, one := range table.Indexes {
			if one.Name != op.Name {
				kept = append(kept, one)
			}
		}

		table.Indexes = kept

		return table, ""
	})
}

// addUnique folds a unique constraint onto the table it names.
func (f *folder) addUnique(at int32, op ir.AddUnique) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if problem := coverable(table, op.Columns); problem != "" {
			return table, problem
		}

		if named(uniqueNames(table.Uniques), op.Name) {
			return table, fmt.Sprintf("it already carries a unique constraint %q", op.Name)
		}

		table.Uniques = append(table.Uniques,
			schema.Unique{Name: op.Name, Columns: op.Columns})

		return table, ""
	})
}

// dropUnique folds a unique constraint off the table it names.
func (f *folder) dropUnique(at int32, op ir.DropUnique) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if !named(uniqueNames(table.Uniques), op.Name) {
			return table, fmt.Sprintf("it carries no unique constraint %q", op.Name)
		}

		var kept []schema.Unique

		for _, one := range table.Uniques {
			if one.Name != op.Name {
				kept = append(kept, one)
			}
		}

		table.Uniques = kept

		return table, ""
	})
}

// addCheck folds a check constraint onto the table it names, expression and all.
func (f *folder) addCheck(at int32, op ir.AddCheck) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if named(checkNames(table.Checks), op.Name) {
			return table, fmt.Sprintf("it already carries a check constraint %q", op.Name)
		}

		table.Checks = append(table.Checks,
			schema.Check{Name: op.Name, Expression: op.Expression})

		return table, ""
	})
}

// A check's expression is engine text and is folded verbatim rather than judged, there being nothing
// here that parses SQL. What it cannot be is compared: every server rewrites its own, measured as
// four spellings of `id > 0` across four servers, which is why a caller comparing a projection
// against a live schema compares a check for presence rather than for content.

// dropCheck folds a check constraint off the table it names.
func (f *folder) dropCheck(at int32, op ir.DropCheck) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if !named(checkNames(table.Checks), op.Name) {
			return table, fmt.Sprintf("it carries no check constraint %q", op.Name)
		}

		var kept []schema.Check

		for _, one := range table.Checks {
			if one.Name != op.Name {
				kept = append(kept, one)
			}
		}

		table.Checks = kept

		return table, ""
	})
}

// addPrimaryKey folds a primary key onto the table it names, which no table may hold two of.
func (f *folder) addPrimaryKey(at int32, op ir.AddPrimaryKey) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if problem := coverable(table, op.Columns); problem != "" {
			return table, problem
		}

		if table.PrimaryKey != nil {
			return table, "it already has a primary key, and no table holds two"
		}

		table.PrimaryKey = &schema.PrimaryKey{Name: op.Name, Columns: op.Columns}

		return keyed(table, op.Columns), ""
	})
}

// Every engine makes a primary key's columns NOT NULL whether the migration said so or not, which
// is the implied change M9-T7 measured and engine.Unrestorable refuses over. A projection that left
// them nullable would describe a schema no server would produce, and would then have migr diff
// propose tightening columns the engine had already tightened.

// keyed returns a table whose primary key columns no longer accept null, as every engine leaves them.
func keyed(table schema.Table, key []string) schema.Table {
	table.Columns = mapped(table.Columns, func(column schema.Column) schema.Column {
		if covers(key, column.Name) {
			column.Nullable = false
		}

		return column
	})

	return table
}

// dropPrimaryKey folds a table's primary key off it.
func (f *folder) dropPrimaryKey(at int32, op ir.DropPrimaryKey) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if table.PrimaryKey == nil {
			return table, "it has no primary key to drop"
		}

		table.PrimaryKey = nil

		return table, ""
	})
}

// addForeignKey folds a reference onto the table it names, checking both ends of it.
func (f *folder) addForeignKey(at int32, op ir.AddForeignKey) {
	if problem := f.referenceable(op); problem != "" {
		f.refuse(at, op.Kind(), op.Name, problem)

		return
	}

	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if problem := coverable(table, op.Columns); problem != "" {
			return table, problem
		}

		if named(keyNames(table.ForeignKeys), op.Name) {
			return table, fmt.Sprintf("it already carries a foreign key %q", op.Name)
		}

		table.ForeignKeys = append(table.ForeignKeys, schema.ForeignKey{
			Name: op.Name, Columns: op.Columns,
			ReferencesTable: op.ReferencesTable, ReferencesColumns: op.ReferencesColumns,
			OnDelete: op.OnDelete, OnUpdate: op.OnUpdate,
		})

		return table, ""
	})
}

// A referenced table this schema does not hold is refused rather than ignored, which is the one
// place this projection is stricter than the differ. internal/diff leaves such a reference alone
// because a snapshot is one namespace and a cross-schema reference is real; here the
// schema is everything the operations built, so a name nothing built is a name nothing will have.

// referenceable reports what stops the far end of a foreign key from being there.
func (f *folder) referenceable(op ir.AddForeignKey) string {
	referenced, held := f.held.Table(op.ReferencesTable)
	if !held {
		return fmt.Sprintf("it references table %q, which this schema does not hold",
			op.ReferencesTable)
	}

	for _, column := range op.ReferencesColumns {
		if _, carries := referenced.Column(column); !carries {
			return fmt.Sprintf("it references column %q, which this schema does not hold",
				qualified(op.ReferencesTable, column))
		}
	}

	return ""
}

// dropForeignKey folds a reference off the table it names.
func (f *folder) dropForeignKey(at int32, op ir.DropForeignKey) {
	f.rewrite(at, op.Kind(), op.Table, func(table schema.Table) (schema.Table, string) {
		if !named(keyNames(table.ForeignKeys), op.Name) {
			return table, fmt.Sprintf("it carries no foreign key %q", op.Name)
		}

		var kept []schema.ForeignKey

		for _, one := range table.ForeignKeys {
			if one.Name != op.Name {
				kept = append(kept, one)
			}
		}

		table.ForeignKeys = kept

		return table, ""
	})
}

// coverable reports the first column a constraint covers that the table does not hold.
func coverable(table schema.Table, columns []string) string {
	for _, column := range columns {
		if _, held := table.Column(column); !held {
			return fmt.Sprintf("it covers column %q, which this schema does not hold",
				qualified(table.Name, column))
		}
	}

	return ""
}

// named reports whether a list of constraint names already holds one.
func named(held []string, name string) bool {
	for _, one := range held {
		if one == name {
			return true
		}
	}

	return false
}

// indexNames names every index a table carries.
func indexNames(held []schema.Index) []string {
	return mapped(held, func(one schema.Index) string { return one.Name })
}

// uniqueNames names every unique constraint a table carries.
func uniqueNames(held []schema.Unique) []string {
	return mapped(held, func(one schema.Unique) string { return one.Name })
}

// checkNames names every check constraint a table carries.
func checkNames(held []schema.Check) []string {
	return mapped(held, func(one schema.Check) string { return one.Name })
}

// keyNames names every foreign key a table carries.
func keyNames(held []schema.ForeignKey) []string {
	return mapped(held, func(one schema.ForeignKey) string { return one.Name })
}
