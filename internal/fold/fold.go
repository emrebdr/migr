package fold

import (
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// Forward returns the schema a list of operations leaves, and every operation it could not fold.
func Forward(from schema.Snapshot, ops ir.Operations) (schema.Snapshot, []Refusal) {
	f := &folder{held: from.Canonical()}

	for i, op := range ops {
		f.apply(int32(i)+1, op)
	}

	return f.held.Canonical(), f.refusals
}

// folder holds one projection: the schema so far, and every operation it could not apply to it.
type folder struct {
	// held is the schema as the operations folded so far leave it, in canonical order.
	held schema.Snapshot
	// refusals are the operations this folder could not apply, in the order it reached them.
	refusals []Refusal
}

// This is the opposite direction from internal/diff, and the two are each other's inverse over a
// list the differ produced: Forward(a, Diff(a, b)) is b. A package of its own rather than a file
// next door, because ADR 0070 F says the differ's names-only availability model must not grow into
// a projection -- and rather than internal/schema, which may never import internal/ir (ADR 0065).
//
// It folds and it does not validate. An operation naming something that is not there is a refusal
// rather than a panic or a silent create, which is what makes the same fold readable as M10-T3's
// reference-integrity check: there, the refusals are the findings.

// apply folds one operation onto the schema, recording the ones it cannot.
func (f *folder) apply(at int32, op ir.Operation) {
	if op == nil {
		f.refuse(at, "", "", "the list carries no operation at this position")

		return
	}

	if err := op.Validate(); err != nil {
		f.refuse(at, op.Kind(), "", "it is not a usable operation: "+err.Error())

		return
	}

	f.fold(at, op)
}

// fold folds one validated operation, dispatching on what it is.
func (f *folder) fold(at int32, op ir.Operation) {
	switch o := op.(type) {
	case ir.CreateTable:
		f.createTable(at, o)
	case ir.DropTable:
		f.dropTable(at, o)
	case ir.RenameTable:
		f.renameTable(at, o)
	case ir.AddColumn:
		f.addColumn(at, o)
	case ir.DropColumn:
		f.dropColumn(at, o)
	case ir.RenameColumn:
		f.renameColumn(at, o)
	case ir.AlterColumn:
		f.alterColumn(at, o)
	case ir.AddIndex:
		f.addIndex(at, o)
	case ir.DropIndex:
		f.dropIndex(at, o)
	case ir.AddUnique:
		f.addUnique(at, o)
	case ir.DropUnique:
		f.dropUnique(at, o)
	case ir.AddCheck:
		f.addCheck(at, o)
	case ir.DropCheck:
		f.dropCheck(at, o)
	case ir.AddPrimaryKey:
		f.addPrimaryKey(at, o)
	case ir.DropPrimaryKey:
		f.dropPrimaryKey(at, o)
	case ir.AddForeignKey:
		f.addForeignKey(at, o)
	case ir.DropForeignKey:
		f.dropForeignKey(at, o)
	case ir.CreateView:
		f.createView(at, o)
	case ir.DropView:
		f.dropView(at, o)
	case ir.Raw:
		f.refuse(at, o.Kind(), o.Driver, unfoldable)
	case ir.DataCopy:
		f.dataCopy(at, o)
	case ir.DataBackfill:
		f.dataBackfill(at, o)
	case ir.DataTransform:
		f.dataTransform(at, o)
	default:
		f.refuse(at, op.Kind(), "", "nothing here knows how to fold it")
	}
}

// unfoldable is why engine SQL ends a projection: nothing can read what a statement did.
const unfoldable = "it is engine SQL, whose effect on the schema nothing here can read -- so the " +
	"schema from this point on is not knowable by folding the operations at all"

// table returns the table an operation names and its place, reporting one the schema does not hold.
func (f *folder) table(at int32, kind ir.Kind, name string) (int, bool) {
	for i := range f.held.Tables {
		if f.held.Tables[i].Name == name {
			return i, true
		}
	}

	f.refuse(at, kind, name, "this schema holds no such table")

	return 0, false
}

// rewrite applies a change to the table an operation names, reporting one that is not there.
func (f *folder) rewrite(at int32, kind ir.Kind, name string,
	change func(schema.Table) (schema.Table, string),
) {
	i, held := f.table(at, kind, name)
	if !held {
		return
	}

	rewritten, problem := change(f.held.Tables[i])
	if problem != "" {
		f.refuse(at, kind, name, problem)

		return
	}

	f.held.Tables[i] = rewritten
}
