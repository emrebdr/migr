package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// Three operations, each measured rather than reasoned about. A dropped column takes every index,
// constraint and reference over it, and ir.Column has no field for any of them -- M3-T7 measured
// PostgreSQL destroying an index that way and the reversal bringing the column back without it. A
// dropped table takes everything ir.TableDef cannot hold, which is every one of its constraints. And
// a primary key over a nullable column implies a NOT NULL that dropping the key does not undo --
// measured on PostgreSQL, MySQL and MariaDB alike, and ir.AddPrimaryKey carries no payload at all,
// an add being inverted by name.
//
// Each detail names the remedy, and each remedy is an operation the author adds rather than a flag:
// drop the index explicitly first, or set the column NOT NULL with an explicit alter_column. Both
// make the migration round-trip on every engine rather than on some of them.

// impliedBy reports what the live schema shows an operation will change and no payload records.
func impliedBy(op ir.Operation, live *schema.Snapshot) []string {
	switch o := op.(type) {
	case ir.DropColumn:
		return impliedByDropColumn(o, live)
	case ir.DropTable:
		return impliedByDropTable(o, live)
	case ir.AddPrimaryKey:
		return impliedByAddPrimaryKey(o, live)
	default:
		return nil
	}
}

// impliedByDropColumn reports everything the live schema shows a dropped column takes with it.
func impliedByDropColumn(op ir.DropColumn, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return nil
	}

	lost := coveringNames(table, op.Column)
	lost = append(lost, referencingNames(live, op.Table, op.Column)...)

	if len(lost) == 0 {
		return nil
	}

	return []string{fmt.Sprintf("dropping column %q takes %s with it, and a recorded column "+
		"carries no index, constraint or reference, so nothing could put %s back: drop %s "+
		"explicitly first, which is what makes this migration reversible",
		op.Column, join(lost), them(lost), join(lost))}
}

// coveringNames lists everything on one table that covers a column, in the order they are reported.
func coveringNames(table schema.Table, column string) []string {
	var lost []string

	for _, index := range table.Indexes {
		if slices.Contains(index.Columns, column) {
			lost = append(lost, "index "+quoted(index.Name))
		}
	}

	for _, unique := range table.Uniques {
		if slices.Contains(unique.Columns, column) {
			lost = append(lost, "unique constraint "+quoted(unique.Name))
		}
	}

	for _, key := range table.ForeignKeys {
		if slices.Contains(key.Columns, column) {
			lost = append(lost, "foreign key "+quoted(key.Name))
		}
	}

	return lost
}

// referencingNames lists every other table's foreign key that points at one column.
func referencingNames(live *schema.Snapshot, table, column string) []string {
	var lost []string

	for _, other := range live.Canonical().Tables {
		if other.Name == table {
			continue
		}

		for _, key := range other.ForeignKeys {
			if key.ReferencesTable == table && slices.Contains(key.ReferencesColumns, column) {
				lost = append(lost, fmt.Sprintf("foreign key %s on table %q",
					quoted(key.Name), other.Name))
			}
		}
	}

	return lost
}

// impliedByDropTable reports everything the live schema shows a dropped table takes with it.
func impliedByDropTable(op ir.DropTable, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return nil
	}

	lost := tableBelongings(table)
	lost = append(lost, referencingTable(live, op.Table)...)

	if len(lost) == 0 {
		return nil
	}

	return []string{fmt.Sprintf("dropping table %q takes %s with it, and a recorded table "+
		"definition holds its columns and its primary key and nothing else, so nothing could put "+
		"%s back: drop %s explicitly first, which is what makes this migration reversible",
		op.Table, join(lost), them(lost), join(lost))}
}

// tableBelongings lists everything on a table that a recorded table definition cannot hold.
func tableBelongings(table schema.Table) []string {
	var lost []string

	for _, index := range table.Indexes {
		lost = append(lost, "index "+quoted(index.Name))
	}

	for _, unique := range table.Uniques {
		lost = append(lost, "unique constraint "+quoted(unique.Name))
	}

	for _, check := range table.Checks {
		lost = append(lost, "check constraint "+quoted(check.Name))
	}

	for _, key := range table.ForeignKeys {
		lost = append(lost, "foreign key "+quoted(key.Name))
	}

	return lost
}

// referencingTable lists every other table's foreign key that points at one table.
func referencingTable(live *schema.Snapshot, table string) []string {
	var lost []string

	for _, other := range live.Canonical().Tables {
		if other.Name == table {
			continue
		}

		for _, key := range other.ForeignKeys {
			if key.ReferencesTable == table {
				lost = append(lost, fmt.Sprintf("foreign key %s on table %q",
					quoted(key.Name), other.Name))
			}
		}
	}

	return lost
}

// impliedByAddPrimaryKey reports a key over a column this schema says accepts null.
func impliedByAddPrimaryKey(op ir.AddPrimaryKey, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return nil
	}

	var nullable []string

	for _, name := range op.Columns {
		column, carries := table.Column(name)
		if carries && column.Nullable {
			nullable = append(nullable, quoted(name))
		}
	}

	if len(nullable) == 0 {
		return nil
	}

	return []string{fmt.Sprintf("this schema says %s %s null, and a primary key over a column "+
		"makes it NOT NULL -- an add is inverted by name, so dropping the key again will not "+
		"restore what it changed: set %s NOT NULL with an explicit alter_column first, which this "+
		"migration's inverse can then undo", join(nullable), accepts(nullable),
		join(nullable))}
}

// quoted renders an object's name, and names the unnamed thing an engine reports no name for.
func quoted(name string) string {
	if name == "" {
		return "an unnamed one"
	}

	return fmt.Sprintf("%q", name)
}

// join renders a list of objects the way a sentence names them.
func join(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}

	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// accepts renders the verb a list of columns takes, since one column and several read differently.
func accepts(names []string) string {
	if len(names) == 1 {
		return "accepts"
	}

	return "accept"
}

// them renders what a sentence calls a list of objects it has already named.
func them(names []string) string {
	if len(names) == 1 {
		return "it"
	}

	return "them"
}
