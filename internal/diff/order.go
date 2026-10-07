package diff

import "github.com/emrebdr/migr/internal/ir"

// One fixed order rather than a topological sort, because no operation carries a foreign key inline:
// ir.CreateTable holds columns and a primary key, so every real dependency between two operations is
// a boundary between two kinds. Within a kind the order is the walk's, which is canonical.
//
// Drops come before creates because an index name is namespace-scoped on PostgreSQL, so a name moving
// from one table to another must be freed before it is claimed.
//
// And the order is exactly its own mirror: every pair sits at opposite ends and alter_column, which
// inverts to itself, sits in the middle. So reversing a list in this order gives a list in this order,
// which is what makes a derived down of a generated migration runnable rather than merely well formed
// -- the foreign key goes before the table it references, the column arrives before the index over it.
// A test holds every one of the seventeen to its mirror, so a kind moved out of place fails rather
// than quietly costing that.

// order is every operation this differ emits, in the order it emits them.
var order = []ir.Kind{
	ir.KindDropView,
	ir.KindDropForeignKey,
	ir.KindDropCheck,
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
	ir.KindAddCheck,
	ir.KindAddForeignKey,
	ir.KindCreateView,
}

// neverEmitted names every operation this differ does not propose, and why it does not.
//
// Coverage is checked against this, so a twenty-first operation fails the suite until somebody
// decides which half it is in.
var neverEmitted = map[ir.Kind]string{
	ir.KindRenameTable: "a rename is a heuristic a person has to confirm, which is M11-T4; until " +
		"then a renamed table reaches the diff as a drop and a create",
	ir.KindRenameColumn: "a rename is a heuristic a person has to confirm, which is M11-T4; until " +
		"then a renamed column reaches the diff as a drop and an add",
	ir.KindRaw: "engine SQL names a driver and has no inverse, so closing a gap with it would " +
		"abandon both of the claims a generated migration is supposed to keep",
}

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
