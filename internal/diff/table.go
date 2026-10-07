package diff

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// Tables, columns and views are matched by name, because add_column, drop_column, alter_column and
// create_view ... or_replace exist: a matched pair can be changed rather than replaced. A rename
// therefore reaches the operations as a drop and a create, which is what M11-T4 exists to fix; until
// then invariant #7 is the net, since drop_table and drop_column are destructive and migr up refuses
// a migration that destroys what the project has not permitted.

// planTables emits what the two schemas' tables and columns differ by, and runs first.
func (d *differ) planTables() {
	for _, table := range d.from.Tables {
		if _, kept := d.to.Table(table.Name); !kept {
			d.dropTable(table)
		}
	}

	for _, table := range d.to.Tables {
		before, existed := d.from.Table(table.Name)
		if !existed {
			d.createTable(table)

			continue
		}

		d.alignColumns(before, table)
	}
}

// createTable emits a table the target schema holds and this one does not.
func (d *differ) createTable(table schema.Table) {
	if problem := tableProblem(table); problem != "" {
		d.refuseTable(table.Name, ir.KindCreateTable, problem)

		return
	}

	d.emit(ir.CreateTable{
		Table:      table.Name,
		Columns:    irColumns(table.Columns),
		PrimaryKey: keyColumns(table.PrimaryKey),
	})
}

// A dropped table's own indexes, uniques, checks and foreign keys are dropped explicitly first, even
// though every engine takes them with the table. A recorded table definition holds columns and a
// primary key and nothing else, so a bare drop_table is a migration whose inverse rebuilds a
// different table -- which is what internal/engine's implied gate refuses. Dropping each one by name
// records what it was, so the reversed list puts every one of them back.

// dropTable emits a table this schema holds and the target does not, with everything on it.
func (d *differ) dropTable(table schema.Table) {
	if problem := d.recreatableTable(table); problem != "" {
		d.refuse(ir.KindDropTable, table.Name, problem)

		return
	}

	d.dropBelongings(table)
	d.emit(ir.DropTable{Table: table.Name, From: irTableDef(table)})
}

// recreatableTable reports what stops a table from being dropped in a way its inverse can undo.
func (d *differ) recreatableTable(table schema.Table) string {
	if problem := tableProblem(table); problem != "" {
		return problem
	}

	for _, belonging := range belongingsOf(table) {
		if belonging.problem == "" {
			continue
		}

		return fmt.Sprintf("dropping it takes its %s with it, which a recorded table definition "+
			"cannot hold, so that %s has to be dropped by name first -- and it cannot be: %s",
			belonging.element, belonging.element, belonging.problem)
	}

	return ""
}

// dropBelongings emits a drop for everything on a table that a recorded table definition cannot hold.
func (d *differ) dropBelongings(table schema.Table) {
	for _, key := range table.ForeignKeys {
		d.dropForeignKey(table.Name, key)
	}

	for _, check := range table.Checks {
		d.dropCheck(table.Name, check)
	}

	for _, index := range table.Indexes {
		d.dropIndex(table.Name, index)
	}

	for _, unique := range table.Uniques {
		d.dropUnique(table.Name, unique)
	}
}

// belonging is one thing on a table that a recorded table definition cannot hold.
type belonging struct {
	// element names what kind of thing it is, for a message about the table it belongs to.
	element string
	// problem is what stops it from being expressed, and is empty where nothing does.
	problem string
}

// belongingsOf lists everything on a table a drop has to record separately, in reporting order.
func belongingsOf(table schema.Table) []belonging {
	found := make([]belonging, 0,
		len(table.ForeignKeys)+len(table.Checks)+len(table.Indexes)+len(table.Uniques))

	for _, key := range table.ForeignKeys {
		found = append(found, belonging{"foreign key", foreignKeyProblem(key)})
	}

	for _, check := range table.Checks {
		found = append(found, belonging{"check constraint", checkProblem(check)})
	}

	for _, index := range table.Indexes {
		found = append(found, belonging{"index", indexProblem(index)})
	}

	for _, unique := range table.Uniques {
		found = append(found, belonging{"unique constraint", uniqueProblem(unique)})
	}

	return found
}

// alignColumns emits what one table's columns differ by between the two schemas.
func (d *differ) alignColumns(before, after schema.Table) {
	for _, column := range before.Columns {
		if _, kept := after.Column(column.Name); !kept {
			d.dropColumn(after.Name, column)
		}
	}

	for _, column := range after.Columns {
		was, existed := before.Column(column.Name)
		if !existed {
			d.addColumn(after.Name, column)

			continue
		}

		d.alterColumn(after.Name, was, column)
	}
}

// dropColumn emits a column this schema holds and the target does not, recording what it was.
func (d *differ) dropColumn(table string, column schema.Column) {
	if problem := columnProblem(column); problem != "" {
		d.refuse(ir.KindDropColumn, qualified(table, column.Name), problem)

		return
	}

	recorded := irColumn(column)
	d.emit(ir.DropColumn{Table: table, Column: column.Name, From: &recorded})
}

// addColumn emits a column the target schema holds and this one does not.
func (d *differ) addColumn(table string, column schema.Column) {
	if problem := columnProblem(column); problem != "" {
		d.refuseColumn(table, column.Name)
		d.refuse(ir.KindAddColumn, qualified(table, column.Name), problem)

		return
	}

	d.emit(ir.AddColumn{Table: table, Column: irColumn(column)})
}
