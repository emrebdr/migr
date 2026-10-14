package diff

import (
	"slices"

	"github.com/emrebdr/migr/internal/ir"
)

// One fixed order rather than a topological sort, because no operation carries a foreign key inline:
// ir.CreateTable holds columns and a primary key and nothing else, so every real dependency between
// two operations is a boundary between two kinds. Within a kind the order is the walk's, which is
// canonical.
//
// Drops come before creates because an index name is namespace-scoped on PostgreSQL, so a name moving
// from one table to another must be freed before it is claimed.
//
// The seventeen that are not renames are exactly their own mirror: every pair sits at opposite ends
// and alter_column, which inverts to itself, sits in the middle. So reversing a list of them gives a
// list in this order, which is what makes a derived down of a generated migration runnable rather
// than merely well formed. A test holds every one of the seventeen to its mirror, so a kind moved out
// of place fails rather than quietly costing that.
//
// A check is dropped before a foreign key and added after one, which is the one place that mirror
// moved: MySQL 8.0 refuses to rename a column a check constraint names (error 3959, measured --
// MariaDB 11.4 allows it and every other constraint kind allows it on every server), so the check
// has to be gone before the rename below it. The two ends swap together, so the mirror survives.

// core is every operation this differ emits that is not a rename, in the order it emits them.
var core = []ir.Kind{
	ir.KindDropView,
	ir.KindDropCheck,
	ir.KindDropForeignKey,
	ir.KindDropIndex,
	ir.KindDropUnique,
	ir.KindDropPrimaryKey,
	ir.KindDropColumn,
	ir.KindDropTable,
	ir.KindAlterColumn,
	ir.KindCreateTable,
	ir.KindAddColumn,
	ir.KindAddPrimaryKey,
	ir.KindAddUnique,
	ir.KindAddIndex,
	ir.KindAddForeignKey,
	ir.KindAddCheck,
	ir.KindCreateView,
}

// The two renames are the one asymmetry in that mirror, and the asymmetry is the point rather than a
// cost. Both are self-inverse, and a self-inverse kind preserves a mirror only at its exact center,
// which alter_column holds -- so these two cannot be mirrored and the rule they follow is stated
// instead: a rename establishes the name every operation after it uses, so it comes first going
// forward and ir.ReverseAll puts it last coming back, which is exactly where a rollback needs it.
// Forward, the object takes its target name and the rest of the list speaks that name; backward, the
// rest of the list speaks that name and the rename restores the old one at the end.
//
// Where each sits between the drops is decided by what the drops record. A dropped view's recorded
// body names the old table name, so drop_view goes above rename_table and its own inverse therefore
// lands below the rename back. A dropped check's recorded expression likewise names the old column,
// so drop_check goes above rename_column -- which is also what MySQL 8.0 requires. Everything else a
// drop records is a column list read off the renamed schema, so those go below it.

// order is every operation this differ emits, in the order it emits them.
var order = slices.Concat(
	core[:1],
	[]ir.Kind{ir.KindRenameTable},
	core[1:2],
	[]ir.Kind{ir.KindRenameColumn},
	core[2:],
)

// neverEmitted names every operation this differ does not propose, and why it does not.
//
// Coverage is checked against this, so a twenty-first operation fails the suite until somebody
// decides which half it is in.
var neverEmitted = map[ir.Kind]string{
	ir.KindRaw: "engine SQL names a driver and has no inverse, so closing a gap with it would " +
		"abandon both of the claims a generated migration is supposed to keep",
	ir.KindDataCopy:      rowless,
	ir.KindDataBackfill:  rowless,
	ir.KindDataTransform: rowless,
}

// rowless is why a differ proposes no data operation: the two schemas it compares hold no rows.
const rowless = "a differ compares two schemas, and a schema holds no rows for a data operation " +
	"to write"

// emit records one operation under its own kind, which is what puts it in the right place.
func (d *differ) emit(op ir.Operation) {
	d.buckets[op.Kind()] = append(d.buckets[op.Kind()], op)
}

// operations returns everything emitted, in order, and nothing at all for two schemas that agree.
func (d *differ) operations() ir.Operations {
	var ops ir.Operations

	for _, kind := range order {
		ops = append(ops, d.buckets[kind]...)
	}

	return ops
}
