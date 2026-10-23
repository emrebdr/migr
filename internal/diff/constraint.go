package diff

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// The five constraint kinds are set differences under the model's own Equal: anything the target
// holds that nothing equal to it is already there is added, and anything here the target does not
// hold is dropped. One rule for five kinds, and no pairing heuristic to get wrong -- there is no
// alter_index, so a changed index is a drop and an add, and a renamed one is too.
//
// Two of those Equal methods deliberately ignore a name, because no two engines agree on one: a
// primary key's and a foreign key's. So a key whose name moved is no change at all, which
// is what keeps a diff between two engines from proposing to rebuild every key it can see.

// planConstraints emits what the two schemas' constraints differ by, for every table that will exist.
func (d *differ) planConstraints() {
	for _, after := range d.to.Tables {
		if !d.willExist(after.Name) {
			continue
		}

		before, existed := d.from.Table(after.Name)
		if existed {
			d.alignPrimaryKey(before, after)
		}

		d.alignIndexes(before, after)
		d.alignUniques(before, after)
		d.alignChecks(before, after)
		d.alignForeignKeys(before, after)
	}
}

// A new table states its key inline in create_table, where an existing one states it as an operation
// (docs/ir.md §7.2), which is why a table the target schema is creating is not aligned here at all.

// alignPrimaryKey emits the key one table differs by, as a drop and an add, there being no alter.
func (d *differ) alignPrimaryKey(before, after schema.Table) {
	if before.PrimaryKey.Equal(after.PrimaryKey) {
		return
	}

	dropping := markedBeyond(primaryKeyBeyond(before.PrimaryKey))
	adding := d.addedKeyProblem(after)

	if dropping != "" || adding != "" {
		d.refusePrimaryKey(after.Name, heldBack(dropping, adding, before.PrimaryKey != nil),
			heldBack(adding, dropping, after.PrimaryKey != nil))

		return
	}

	if before.PrimaryKey != nil {
		d.emit(ir.DropPrimaryKey{Table: after.Name, Name: before.PrimaryKey.Name,
			From: &ir.ColumnsDef{Columns: before.PrimaryKey.Columns}})
	}

	if after.PrimaryKey != nil {
		d.emit(ir.AddPrimaryKey{Table: after.Name, Name: after.PrimaryKey.Name,
			Columns: after.PrimaryKey.Columns})
	}
}

// A key moves as a drop and an add, there being no alter, so neither half can be emitted without the
// other: a drop alone leaves the table with no key the target asked for, and an add alone names one
// the table already has. One half refused therefore holds the other back, and says so.

// heldBack returns a half's own problem, or the reason it is not proposed when the other half has one.
func heldBack(own, other string, proposed bool) string {
	if own != "" || other == "" || !proposed {
		return own
	}

	return "the other half of this key change cannot be proposed, so this half is held back: " + other
}

// refusePrimaryKey reports both halves of a key change, since emitting either alone would fail.
func (d *differ) refusePrimaryKey(table, dropping, adding string) {
	if dropping != "" {
		d.refuse(ir.KindDropPrimaryKey, table, dropping)
	}

	if adding != "" {
		d.refuse(ir.KindAddPrimaryKey, table, adding)
	}
}

// addedKeyProblem reports what stops a primary key the target schema holds from being added.
func (d *differ) addedKeyProblem(after schema.Table) string {
	if after.PrimaryKey == nil {
		return ""
	}

	if marked := markedBeyond(after.PrimaryKey.Beyond); marked != "" {
		return marked
	}

	if problem := d.coveredProblem(after.Name, after.PrimaryKey.Columns); problem != "" {
		return problem
	}

	return nullableKeyProblem(after)
}

// nullableKeyProblem reports a key the target schema states over a column it calls nullable.
func nullableKeyProblem(after schema.Table) string {
	for _, name := range after.PrimaryKey.Columns {
		column, carries := after.Column(name)
		if carries && column.Nullable {
			return fmt.Sprintf("the target schema says column %q accepts null and keys the table on "+
				"it, which no engine holds at once: a primary key makes its columns NOT NULL",
				name)
		}
	}

	return ""
}

// alignIndexes emits the indexes one table differs by, matched as a set.
func (d *differ) alignIndexes(before, after schema.Table) {
	for _, index := range before.Indexes {
		if hasEqual(after.Indexes, index, schema.Index.Equal) {
			continue
		}

		d.dropIndex(after.Name, index)
	}

	for _, index := range after.Indexes {
		if hasEqual(before.Indexes, index, schema.Index.Equal) {
			continue
		}

		d.addIndex(after.Name, index)
	}
}

// addIndex emits an index the target schema holds and this one does not.
func (d *differ) addIndex(table string, index schema.Index) {
	problem := indexProblem(index)
	if problem == "" {
		problem = d.coveredProblem(table, index.Columns)
	}

	if problem != "" {
		d.refuse(ir.KindAddIndex, index.Name, problem)

		return
	}

	d.emit(ir.AddIndex{Table: table, Name: index.Name, Columns: index.Columns})
}

// dropIndex emits an index this schema holds and the target does not, recording what it covered.
func (d *differ) dropIndex(table string, index schema.Index) {
	if problem := indexProblem(index); problem != "" {
		d.refuse(ir.KindDropIndex, index.Name, problem)

		return
	}

	d.emit(ir.DropIndex{Table: table, Name: index.Name,
		From: &ir.ColumnsDef{Columns: index.Columns}})
}

// alignUniques emits the unique constraints one table differs by, matched as a set.
func (d *differ) alignUniques(before, after schema.Table) {
	for _, unique := range before.Uniques {
		if hasEqual(after.Uniques, unique, schema.Unique.Equal) {
			continue
		}

		d.dropUnique(after.Name, unique)
	}

	for _, unique := range after.Uniques {
		if hasEqual(before.Uniques, unique, schema.Unique.Equal) {
			continue
		}

		d.addUnique(after.Name, unique)
	}
}

// addUnique emits a unique constraint the target schema holds and this one does not.
func (d *differ) addUnique(table string, unique schema.Unique) {
	problem := uniqueProblem(unique)
	if problem == "" {
		problem = d.coveredProblem(table, unique.Columns)
	}

	if problem != "" {
		d.refuse(ir.KindAddUnique, unique.Name, problem)

		return
	}

	d.emit(ir.AddUnique{Table: table, Name: unique.Name, Columns: unique.Columns})
}

// dropUnique emits a unique constraint this schema holds and the target does not.
func (d *differ) dropUnique(table string, unique schema.Unique) {
	if problem := uniqueProblem(unique); problem != "" {
		d.refuse(ir.KindDropUnique, unique.Name, problem)

		return
	}

	d.emit(ir.DropUnique{Table: table, Name: unique.Name,
		From: &ir.ColumnsDef{Columns: unique.Columns}})
}

// alignChecks emits the check constraints one table differs by, matched as a set.
func (d *differ) alignChecks(before, after schema.Table) {
	for _, check := range before.Checks {
		if hasEqual(after.Checks, check, schema.Check.Equal) {
			continue
		}

		d.dropCheck(after.Name, check)
	}

	for _, check := range after.Checks {
		if hasEqual(before.Checks, check, schema.Check.Equal) {
			continue
		}

		d.addCheck(after.Name, check)
	}
}

// addCheck emits a check constraint the target schema holds and this one does not.
func (d *differ) addCheck(table string, check schema.Check) {
	if problem := checkProblem(check); problem != "" {
		d.refuse(ir.KindAddCheck, checkSubject(table, check), problem)

		return
	}

	d.emit(ir.AddCheck{Table: table, Name: check.Name, Expression: check.Expression})
}

// dropCheck emits a check constraint this schema holds and the target does not.
func (d *differ) dropCheck(table string, check schema.Check) {
	if problem := checkProblem(check); problem != "" {
		d.refuse(ir.KindDropCheck, checkSubject(table, check), problem)

		return
	}

	d.emit(ir.DropCheck{Table: table, Name: check.Name,
		From: &ir.CheckDef{Expression: check.Expression}})
}

// checkSubject names a check constraint for a refusal, an engine naming one nothing at all.
func checkSubject(table string, check schema.Check) string {
	if check.Name == "" {
		return table
	}

	return check.Name
}

// alignForeignKeys emits the foreign keys one table differs by, matched as a set.
func (d *differ) alignForeignKeys(before, after schema.Table) {
	for _, key := range before.ForeignKeys {
		if hasEqual(after.ForeignKeys, key, schema.ForeignKey.Equal) {
			continue
		}

		d.dropForeignKey(after.Name, key)
	}

	for _, key := range after.ForeignKeys {
		if hasEqual(before.ForeignKeys, key, schema.ForeignKey.Equal) {
			continue
		}

		d.addForeignKey(after.Name, key)
	}
}

// addForeignKey emits a foreign key the target schema holds and this one does not.
func (d *differ) addForeignKey(table string, key schema.ForeignKey) {
	problem := foreignKeyProblem(key)
	if problem == "" {
		problem = d.referenceProblem(table, key)
	}

	if problem != "" {
		d.refuse(ir.KindAddForeignKey, keySubject(table, key), problem)

		return
	}

	d.emit(ir.AddForeignKey{
		Table: table, Name: key.Name, Columns: key.Columns,
		ReferencesTable: key.ReferencesTable, ReferencesColumns: key.ReferencesColumns,
		OnDelete: key.OnDelete, OnUpdate: key.OnUpdate,
	})
}

// dropForeignKey emits a foreign key this schema holds and the target does not.
func (d *differ) dropForeignKey(table string, key schema.ForeignKey) {
	if problem := foreignKeyProblem(key); problem != "" {
		d.refuse(ir.KindDropForeignKey, keySubject(table, key), problem)

		return
	}

	d.emit(ir.DropForeignKey{Table: table, Name: key.Name, From: &ir.ForeignKeyDef{
		Columns:         key.Columns,
		ReferencesTable: key.ReferencesTable, ReferencesColumns: key.ReferencesColumns,
		OnDelete: key.OnDelete, OnUpdate: key.OnUpdate,
	}})
}

// A referenced table the target schema never names is left alone rather than refused: the model says
// outright that a foreign key may point at a table this snapshot does not hold, a snapshot being one
// namespace and a cross-schema reference being real. What is refused is a reference the target schema
// does name and will not have.

// referenceProblem reports a foreign key whose own or whose referenced columns will not be there.
func (d *differ) referenceProblem(table string, key schema.ForeignKey) string {
	if problem := d.coveredProblem(table, key.Columns); problem != "" {
		return problem
	}

	if _, named := d.to.Table(key.ReferencesTable); !named {
		return ""
	}

	if !d.willExist(key.ReferencesTable) {
		return fmt.Sprintf("it references table %q, which this diff is not creating",
			key.ReferencesTable)
	}

	return d.coveredProblem(key.ReferencesTable, key.ReferencesColumns)
}

// coveredProblem reports the first column an operation covers that is not going to be there.
func (d *differ) coveredProblem(table string, columns []string) string {
	column, short := d.unavailable(table, columns)
	if !short {
		return ""
	}

	return fmt.Sprintf("it covers column %q, which this diff is not adding",
		qualified(table, column))
}

// keySubject names a foreign key for a refusal, two engines reporting one with no name of its own.
func keySubject(table string, key schema.ForeignKey) string {
	if key.Name == "" {
		return table
	}

	return key.Name
}
