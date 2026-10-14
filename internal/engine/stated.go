package engine

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// Two things are deliberately never compared, both because the server rewrites them: a check's
// condition and a view's body. ADR 0065 measured that a condition asked for as "n" > 0 reads back
// as (`n` > 0) from MySQL and (n > 0) from PostgreSQL, and that a view asked for as SELECT * comes
// back with an expanded column list -- so comparing either would refuse every correct rollback on
// those engines. A column's type is compared only where the catalog answered exactly, since a guess
// must not refuse anything (ADR 0065, A1).

// statedBy reports what one operation states about the live schema that the live schema denies.
func statedBy(op ir.Operation, live *schema.Snapshot) []string {
	switch o := op.(type) {
	case ir.CreateTable:
		return absentTable(live, o.Table)
	case ir.DropTable:
		return statedDropTable(o, live)
	case ir.RenameTable:
		return append(presentTable(live, o.From), absentTable(live, o.To)...)
	case ir.AddColumn:
		return statedAddColumn(o, live)
	case ir.DropColumn:
		return statedDropColumn(o, live)
	case ir.RenameColumn:
		return statedRenameColumn(o, live)
	case ir.AlterColumn:
		return statedAlterColumn(o, live)
	case ir.AddIndex:
		return statedAddIndex(o, live)
	case ir.DropIndex:
		return statedDropIndex(o, live)
	case ir.AddUnique:
		return statedAddUnique(o, live)
	case ir.DropUnique:
		return statedDropUnique(o, live)
	case ir.AddCheck:
		return statedAddCheck(o, live)
	case ir.DropCheck:
		return statedDropCheck(o, live)
	case ir.AddPrimaryKey:
		return statedAddPrimaryKey(o, live)
	case ir.DropPrimaryKey:
		return statedDropPrimaryKey(o, live)
	case ir.AddForeignKey:
		return statedAddForeignKey(o, live)
	case ir.DropForeignKey:
		return statedDropForeignKey(o, live)
	case ir.CreateView:
		return statedCreateView(o, live)
	case ir.DropView:
		return presentView(live, o.View)
	case ir.DataCopy:
		return statedDataCopy(o, live)
	case ir.DataBackfill:
		return statedWrite(live, o.Table, o.Column, o.Type)
	case ir.DataTransform:
		return statedDataTransform(o, live)
	default:
		return nil
	}
}

// statedDropTable reports a table this schema does not hold, or one whose recorded shape it denies.
func statedDropTable(op ir.DropTable, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return absent("table", op.Table)
	}

	if op.From == nil {
		return nil
	}

	return describeTableDef(table, op.From)
}

// describeTableDef reports every way a recorded table definition and the live table disagree.
func describeTableDef(table schema.Table, def *ir.TableDef) []string {
	var found []string

	for _, recorded := range def.Columns {
		live, held := table.Column(recorded.Name)
		if !held {
			found = append(found, fmt.Sprintf("its recorded definition names column %q, which "+
				"this table does not hold", recorded.Name))

			continue
		}

		found = append(found, describeColumn(recorded, live)...)
	}

	return append(found, describeKeyColumns(table, def.PrimaryKey)...)
}

// describeKeyColumns reports a recorded primary key the live table's own key does not match.
func describeKeyColumns(table schema.Table, recorded []string) []string {
	if len(recorded) == 0 {
		return nil
	}

	if table.PrimaryKey == nil {
		return []string{fmt.Sprintf("its recorded definition names a primary key over (%s) and "+
			"this table has none", strings.Join(recorded, ", "))}
	}

	if !sameColumns(table.PrimaryKey.Columns, recorded) {
		return []string{fmt.Sprintf("its recorded definition names a primary key over (%s) and "+
			"this table's own is over (%s)", strings.Join(recorded, ", "),
			strings.Join(table.PrimaryKey.Columns, ", "))}
	}

	return nil
}

// statedAddColumn reports a table this schema does not hold, or a column it already holds.
func statedAddColumn(op ir.AddColumn, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return absent("table", op.Table)
	}

	if _, holds := table.Column(op.Column.Name); holds {
		return []string{fmt.Sprintf("table %q already holds a column %q, so this adds one that "+
			"is already there -- a derived inverse of an up that emitted differently the second "+
			"time has this shape", op.Table, op.Column.Name)}
	}

	return nil
}

// statedDropColumn reports a column this schema does not hold, or one whose recorded shape it denies.
func statedDropColumn(op ir.DropColumn, live *schema.Snapshot) []string {
	column, found := liveColumn(live, op.Table, op.Column)
	if found != nil {
		return found
	}

	if op.From == nil {
		return nil
	}

	return describeColumn(*op.From, column)
}

// statedRenameColumn reports a column this schema does not hold, or a name it already holds.
func statedRenameColumn(op ir.RenameColumn, live *schema.Snapshot) []string {
	table, held := live.Table(op.Table)
	if !held {
		return absent("table", op.Table)
	}

	var found []string

	if _, holds := table.Column(op.From); !holds {
		found = append(found, absent("column", op.From)...)
	}

	if _, holds := table.Column(op.To); holds {
		found = append(found, fmt.Sprintf("table %q already holds a column %q, so this renames "+
			"one onto a name that is taken", op.Table, op.To))
	}

	return found
}

// statedAlterColumn reports a column this schema does not hold, or one its recorded state denies.
func statedAlterColumn(op ir.AlterColumn, live *schema.Snapshot) []string {
	column, found := liveColumn(live, op.Table, op.Column)
	if found != nil {
		return found
	}

	if op.From == nil {
		return nil
	}

	return describeColumnState(*op.From, column)
}

// describeColumnState reports every way a recorded prior column state and the live column disagree.
func describeColumnState(state ir.ColumnState, live schema.Column) []string {
	var found []string

	if state.Type != nil {
		found = append(found, describeType(*state.Type, live)...)
	}

	if state.Nullable != nil && *state.Nullable != live.Nullable {
		found = append(found, fmt.Sprintf("its recorded prior state says the column %s and this "+
			"schema says it %s", nullability(*state.Nullable), nullability(live.Nullable)))
	}

	if state.NoDefault && live.Default != nil {
		found = append(found, fmt.Sprintf("its recorded prior state says the column had no "+
			"default and this schema holds %s", live.Default))
	}

	if state.Default != nil {
		found = append(found, describeDefault(state.Default, live.Default)...)
	}

	return found
}

// describeColumn reports every way a recorded column definition and the live column disagree.
func describeColumn(recorded ir.Column, live schema.Column) []string {
	found := describeType(recorded.Type, live)

	if recorded.Nullable != live.Nullable {
		found = append(found, fmt.Sprintf("its recorded definition says column %q %s and this "+
			"schema says it %s", recorded.Name, nullability(recorded.Nullable),
			nullability(live.Nullable)))
	}

	return append(found, describeDefault(recorded.Default, live.Default)...)
}

// describeType reports a recorded type the live column contradicts, saying nothing about a guess.
func describeType(recorded schema.Type, live schema.Column) []string {
	logical, exact := live.Type.Logical()
	if !exact {
		return nil
	}

	if logical.Equal(recorded) {
		return nil
	}

	return []string{fmt.Sprintf("it records column %q as %s and this schema holds %s",
		live.Name, recorded, logical)}
}

// describeDefault reports a recorded default the live column's own contradicts.
func describeDefault(recorded *ir.Default, live *schema.ColumnDefault) []string {
	if recorded == nil {
		return nil
	}

	if live == nil {
		return []string{fmt.Sprintf("it records a default of %s and this schema holds none",
			describeRecordedDefault(recorded))}
	}

	if sameDefault(recorded, live) {
		return nil
	}

	return []string{fmt.Sprintf("it records a default of %s and this schema holds %s",
		describeRecordedDefault(recorded), live)}
}

// sameDefault reports whether a recorded default and a live one produce the same value.
func sameDefault(recorded *ir.Default, live *schema.ColumnDefault) bool {
	if recorded.Literal != nil {
		return live.Literal != nil && *live.Literal == *recorded.Literal
	}

	return live.Function == recorded.Function
}

// describeRecordedDefault renders a recorded default the way a message names it.
func describeRecordedDefault(recorded *ir.Default) string {
	if recorded.Literal != nil {
		return fmt.Sprintf("%q", *recorded.Literal)
	}

	return string(recorded.Function)
}

// nullability renders what a column accepts, so two of them read as the opposites they are.
func nullability(nullable bool) string {
	if nullable {
		return "accepts null"
	}

	return "is not null"
}

// liveColumn returns one live column, or what is wrong with asking for it.
func liveColumn(live *schema.Snapshot, table, column string) (schema.Column, []string) {
	held, holds := live.Table(table)
	if !holds {
		return schema.Column{}, absent("table", table)
	}

	found, carries := held.Column(column)
	if !carries {
		return schema.Column{}, []string{fmt.Sprintf("table %q holds no column %q, so this names "+
			"something that is not there", table, column)}
	}

	return found, nil
}

// absent reports an object an operation names that this schema does not hold.
func absent(element, name string) []string {
	return []string{fmt.Sprintf("this schema holds no %s %q, so this operation names something "+
		"that is not there", element, name)}
}

// presentTable reports a table an operation needs and this schema does not hold.
func presentTable(live *schema.Snapshot, name string) []string {
	if _, held := live.Table(name); held {
		return nil
	}

	return absent("table", name)
}

// absentTable reports a table an operation would create and this schema already holds.
func absentTable(live *schema.Snapshot, name string) []string {
	if _, held := live.Table(name); !held {
		return nil
	}

	return []string{fmt.Sprintf("this schema already holds a table %q, so this creates one that "+
		"is already there -- a derived inverse of an up that emitted differently the second time "+
		"has this shape", name)}
}

// presentView reports a view an operation needs and this schema does not hold.
func presentView(live *schema.Snapshot, name string) []string {
	if _, held := live.View(name); held {
		return nil
	}

	return absent("view", name)
}

// statedCreateView reports a view this schema already holds, which only a replace may name.
func statedCreateView(op ir.CreateView, live *schema.Snapshot) []string {
	if op.OrReplace {
		return nil
	}

	if _, held := live.View(op.View); !held {
		return nil
	}

	return []string{fmt.Sprintf("this schema already holds a view %q and this operation does not "+
		"replace one", op.View)}
}

// sameColumns reports whether two column lists are the same list in the same order.
func sameColumns(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}

	return true
}

// statedDataCopy reports a column a copy reads or writes that the live schema does not hold.
func statedDataCopy(op ir.DataCopy, live *schema.Snapshot) []string {
	var found []string

	for _, pair := range op.Columns {
		_, read := liveColumn(live, op.From, pair.From)
		_, written := liveColumn(live, op.To, pair.To)
		found = append(append(found, read...), written...)
	}

	return found
}

// statedWrite reports a column a data write names that is not there, or is not the type it says.
func statedWrite(live *schema.Snapshot, table, column string, stated schema.Type) []string {
	held, missing := liveColumn(live, table, column)
	if missing != nil {
		return missing
	}

	return describeType(stated, held)
}

// statedDataTransform reports a transform's target as statedWrite does, and a source that is not there.
func statedDataTransform(op ir.DataTransform, live *schema.Snapshot) []string {
	found := statedWrite(live, op.Table, op.Column, op.Type)
	_, read := liveColumn(live, op.Table, op.From)

	return append(found, read...)
}
