package engine

import (
	"github.com/emrebdr/migr/internal/ir"
)

// separator joins the parts of one identity, and is a byte no identifier can hold.
const separator = "\x00"

// Every judgment in Unrestorable compares an operation against the live schema, and an earlier
// operation in the same list has already changed that schema by the time this one runs. So an
// operation whose identity something earlier touched is not judged at all: a migration that creates
// a table and then indexes it would otherwise be refused for indexing a table that "does not
// exist", which is strictly worse than missing a disagreement.

// ledger remembers what the operations before this one named, since they move what it is judged against.
type ledger struct {
	// touched is every identity the operations so far have named.
	touched map[string]bool
}

// newLedger returns a ledger that has seen nothing.
func newLedger() *ledger {
	return &ledger{touched: make(map[string]bool)}
}

// moved reports whether anything earlier in the list touched what this operation names.
func (l *ledger) moved(op ir.Operation) bool {
	for _, identity := range identities(op) {
		if l.touched[identity] {
			return true
		}
	}

	return false
}

// touch records everything one operation names, so nothing after it is judged against stale state.
func (l *ledger) touch(op ir.Operation) {
	for _, identity := range identities(op) {
		l.touched[identity] = true
	}
}

// identities names everything one operation acts on, at the granularity a later one could collide with.
func identities(op ir.Operation) []string {
	switch o := op.(type) {
	case ir.CreateTable:
		return tableIdentities(o.Table, columnNames(o.Columns)...)
	case ir.DropTable:
		return []string{o.Table}
	case ir.RenameTable:
		return []string{o.From, o.To}
	case ir.AddColumn:
		return tableIdentities(o.Table, o.Column.Name)
	case ir.DropColumn:
		return tableIdentities(o.Table, o.Column)
	case ir.RenameColumn:
		return tableIdentities(o.Table, o.From, o.To)
	case ir.AlterColumn:
		return tableIdentities(o.Table, o.Column)
	case ir.AddIndex:
		return objectIdentities(o.Table, o.Name, o.Columns)
	case ir.DropIndex:
		return objectIdentities(o.Table, o.Name, nil)
	case ir.AddUnique:
		return objectIdentities(o.Table, o.Name, o.Columns)
	case ir.DropUnique:
		return objectIdentities(o.Table, o.Name, nil)
	case ir.AddCheck:
		return objectIdentities(o.Table, o.Name, nil)
	case ir.DropCheck:
		return objectIdentities(o.Table, o.Name, nil)
	case ir.AddPrimaryKey:
		return objectIdentities(o.Table, o.Name, o.Columns)
	case ir.DropPrimaryKey:
		return objectIdentities(o.Table, o.Name, nil)
	case ir.AddForeignKey:
		return append(objectIdentities(o.Table, o.Name, o.Columns),
			tableIdentities(o.ReferencesTable, o.ReferencesColumns...)...)
	case ir.DropForeignKey:
		return objectIdentities(o.Table, o.Name, nil)
	case ir.CreateView:
		return []string{o.View}
	case ir.DropView:
		return []string{o.View}
	default:
		return nil
	}
}

// tableIdentities names a table and the columns of it one operation acts on.
func tableIdentities(table string, columns ...string) []string {
	named := make([]string, 0, len(columns)+1)
	named = append(named, table)

	for _, column := range columns {
		named = append(named, table+separator+column)
	}

	return named
}

// objectIdentities names a table, the object on it, and the columns that object covers.
func objectIdentities(table, object string, columns []string) []string {
	named := tableIdentities(table, columns...)

	return append(named, table+separator+"~"+object)
}

// columnNames lists the names a column list declares, which is what a later operation collides with.
func columnNames(columns []ir.Column) []string {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.Name)
	}

	return names
}
