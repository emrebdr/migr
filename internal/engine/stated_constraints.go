package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// statedAddIndex reports a table, a column or a name that denies what adding an index states.
func statedAddIndex(op ir.AddIndex, live *schema.Snapshot) []string {
	return statedAddCovering(live, op.Table, op.Name, op.Columns)
}

// statedAddUnique reports a table, a column or a name that denies what adding a constraint states.
func statedAddUnique(op ir.AddUnique, live *schema.Snapshot) []string {
	return statedAddCovering(live, op.Table, op.Name, op.Columns)
}

// statedAddCovering reports what an operation covering columns under a new name states and is denied.
func statedAddCovering(live *schema.Snapshot, name, object string, columns []string) []string {
	table, held := live.Table(name)
	if !held {
		return absent("table", name)
	}

	found := describeMissingColumns(table, columns)

	if taken := namedBy(table, object); taken != "" {
		found = append(found, fmt.Sprintf("table %q already carries %s named %q, so this creates "+
			"one under a name that is taken", name, taken, object))
	}

	return found
}

// statedDropIndex reports an index this table does not carry, or one whose recorded shape it denies.
func statedDropIndex(op ir.DropIndex, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return absent("table", op.Table)
	}

	at := slices.IndexFunc(table.Indexes, func(i schema.Index) bool { return i.Name == op.Name })
	if at < 0 {
		return []string{fmt.Sprintf("table %q carries no index named %q, so this names something "+
			"that is not there", op.Table, op.Name)}
	}

	if op.From == nil {
		return nil
	}

	return describeCovered("index", op.Name, table.Indexes[at].Columns, op.From.Columns)
}

// statedDropUnique reports a constraint this table does not carry, or a recorded shape it denies.
func statedDropUnique(op ir.DropUnique, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return absent("table", op.Table)
	}

	at := slices.IndexFunc(table.Uniques, func(u schema.Unique) bool { return u.Name == op.Name })
	if at < 0 {
		return []string{fmt.Sprintf("table %q carries no unique constraint named %q, so this "+
			"names something that is not there", op.Table, op.Name)}
	}

	if op.From == nil {
		return nil
	}

	return describeCovered("unique constraint", op.Name, table.Uniques[at].Columns, op.From.Columns)
}

// statedAddCheck reports a table this schema does not hold, or a constraint name already taken.
func statedAddCheck(op ir.AddCheck, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return absent("table", op.Table)
	}

	if taken := namedBy(table, op.Name); taken != "" {
		return []string{fmt.Sprintf("table %q already carries %s named %q, so this creates one "+
			"under a name that is taken", op.Table, taken, op.Name)}
	}

	return nil
}

// statedDropCheck reports a check this table does not carry, the condition being engine text.
func statedDropCheck(op ir.DropCheck, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return absent("table", op.Table)
	}

	if slices.ContainsFunc(table.Checks, func(c schema.Check) bool { return c.Name == op.Name }) {
		return nil
	}

	return []string{fmt.Sprintf("table %q carries no check constraint named %q, so this names "+
		"something that is not there", op.Table, op.Name)}
}

// statedAddPrimaryKey reports a table, a column or a key already there that denies adding one.
func statedAddPrimaryKey(op ir.AddPrimaryKey, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return absent("table", op.Table)
	}

	found := describeMissingColumns(table, op.Columns)

	if table.PrimaryKey != nil {
		found = append(found, fmt.Sprintf("table %q already has a primary key over (%s), so this "+
			"adds a second one", op.Table, strings.Join(table.PrimaryKey.Columns, ", ")))
	}

	return found
}

// statedDropPrimaryKey reports a table with no key, or one whose recorded columns it denies.
func statedDropPrimaryKey(op ir.DropPrimaryKey, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return absent("table", op.Table)
	}

	if table.PrimaryKey == nil {
		return []string{fmt.Sprintf("table %q has no primary key, so this drops something that "+
			"is not there", op.Table)}
	}

	if op.From == nil {
		return nil
	}

	return describeCovered("primary key", op.Table, table.PrimaryKey.Columns, op.From.Columns)
}

// statedAddForeignKey reports what adding a reference states about both of its sides and is denied.
func statedAddForeignKey(op ir.AddForeignKey, live *schema.Snapshot) []string {
	found := statedAddCovering(live, op.Table, op.Name, op.Columns)

	parent, held := live.Table(op.ReferencesTable)
	if !held {
		return append(found, fmt.Sprintf("it references table %q, which this schema does not hold",
			op.ReferencesTable))
	}

	for _, column := range op.ReferencesColumns {
		if _, carries := parent.Column(column); !carries {
			found = append(found, fmt.Sprintf("it references %q(%q), which that table does not "+
				"hold", op.ReferencesTable, column))
		}
	}

	return found
}

// statedDropForeignKey reports a key this table does not carry, or a recorded shape it denies.
func statedDropForeignKey(op ir.DropForeignKey, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return absent("table", op.Table)
	}

	at := slices.IndexFunc(table.ForeignKeys,
		func(k schema.ForeignKey) bool { return k.Name == op.Name })
	if at < 0 {
		return []string{fmt.Sprintf("table %q carries no foreign key named %q, so this names "+
			"something that is not there", op.Table, op.Name)}
	}

	if op.From == nil {
		return nil
	}

	return describeForeignKeyDef(table.ForeignKeys[at], op.From)
}

// describeForeignKeyDef reports every way a recorded key definition and the live key disagree.
func describeForeignKeyDef(live schema.ForeignKey, def *ir.ForeignKeyDef) []string {
	found := describeCovered("foreign key", live.Name, live.Columns, def.Columns)

	if live.ReferencesTable != def.ReferencesTable {
		found = append(found, fmt.Sprintf("it records the key as referencing %q and this schema "+
			"holds one referencing %q", def.ReferencesTable, live.ReferencesTable))
	}

	found = append(found, describeCovered("referenced side of foreign key", live.Name,
		live.ReferencesColumns, def.ReferencesColumns)...)

	if def.OnDelete.Canonical() != live.OnDelete.Canonical() {
		found = append(found, fmt.Sprintf("it records on_delete as %s and this schema holds %s",
			def.OnDelete.Canonical(), live.OnDelete.Canonical()))
	}

	if def.OnUpdate.Canonical() != live.OnUpdate.Canonical() {
		found = append(found, fmt.Sprintf("it records on_update as %s and this schema holds %s",
			def.OnUpdate.Canonical(), live.OnUpdate.Canonical()))
	}

	return found
}

// describeCovered reports a recorded column list the live object's own does not match.
func describeCovered(element, name string, live, recorded []string) []string {
	if len(recorded) == 0 || sameColumns(live, recorded) {
		return nil
	}

	return []string{fmt.Sprintf("it records the %s %q as covering (%s) and this schema holds one "+
		"covering (%s)", element, name, strings.Join(recorded, ", "), strings.Join(live, ", "))}
}

// describeMissingColumns reports every column an operation names that its table does not hold.
func describeMissingColumns(table schema.Table, columns []string) []string {
	var found []string

	for _, column := range columns {
		if _, carries := table.Column(column); !carries {
			found = append(found, fmt.Sprintf("it covers column %q, which table %q does not hold",
				column, table.Name))
		}
	}

	return found
}

// namedBy reports what a table already carries under one name, and nothing where the name is free.
func namedBy(table schema.Table, name string) string {
	if name == "" {
		return ""
	}

	if slices.ContainsFunc(table.Indexes, func(i schema.Index) bool { return i.Name == name }) {
		return "an index"
	}

	if slices.ContainsFunc(table.Uniques, func(u schema.Unique) bool { return u.Name == name }) {
		return "a unique constraint"
	}

	if slices.ContainsFunc(table.Checks, func(c schema.Check) bool { return c.Name == name }) {
		return "a check constraint"
	}

	if slices.ContainsFunc(table.ForeignKeys,
		func(k schema.ForeignKey) bool { return k.Name == name }) {
		return "a foreign key"
	}

	return ""
}
