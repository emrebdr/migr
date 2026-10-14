package postgres

import (
	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
)

// missingValueRelease is the release that stores a column default instead of rewriting the table.
const missingValueRelease = 11

// rawImpact is permanent rather than pending: the framework cannot know what a raw statement does.
const rawImpact = capability.LockUnknown

// lockImpact reports what an operation costs a live database on the server that was detected.
func (f serverFacts) lockImpact(op ir.Operation) capability.LockImpact {
	switch o := op.(type) {
	case ir.CreateTable:
		return capability.LockFree
	case ir.CreateView:
		return viewImpact(o)
	case ir.AddColumn:
		return f.addColumnImpact(o)
	case ir.AlterColumn:
		return alterColumnImpact(o)
	case ir.AddIndex, ir.AddUnique, ir.AddPrimaryKey, ir.AddForeignKey, ir.AddCheck:
		return capability.LongExclusive
	case ir.DropTable, ir.RenameTable, ir.DropColumn, ir.RenameColumn, ir.DropIndex,
		ir.DropForeignKey, ir.DropUnique, ir.DropCheck, ir.DropPrimaryKey, ir.DropView:
		return capability.BriefExclusive
	case ir.Raw:
		return rawImpact
	case ir.DataCopy:
		return capability.LongExclusive
	case ir.DataBackfill, ir.DataTransform:
		return capability.TableRewrite
	default:
		return capability.LockUnknown
	}
}

// viewImpact reports a new view as free, and a replacement as locking the view it overwrites.
func viewImpact(op ir.CreateView) capability.LockImpact {
	if op.OrReplace {
		return capability.BriefExclusive
	}

	return capability.LockFree
}

// addColumnImpact reports whether this server can store the default instead of rewriting rows.
func (f serverFacts) addColumnImpact(op ir.AddColumn) capability.LockImpact {
	if op.Column.Default == nil {
		return capability.BriefExclusive
	}

	if f.rewritesForDefault(op.Column.Default) {
		return capability.TableRewrite
	}

	return capability.BriefExclusive
}

// rewritesForDefault reports whether every existing row has to be given a value of its own.
func (f serverFacts) rewritesForDefault(d *ir.Default) bool {
	if !f.version.AtLeast(missingValueRelease) {
		return true
	}

	return f.volatileDefault(d)
}

// volatileDefault reports a default that produces a different value per row, as the server says.
func (f serverFacts) volatileDefault(d *ir.Default) bool {
	if d.Function != ir.FuncUUID {
		return false
	}

	return f.uuidDefault.volatile
}

// alterColumnImpact reports the worst of the changes an alteration asks for, since it runs them all.
func alterColumnImpact(op ir.AlterColumn) capability.LockImpact {
	if op.Type != nil {
		return capability.TableRewrite
	}

	if op.Nullable != nil && !*op.Nullable {
		return capability.LongExclusive
	}

	return capability.BriefExclusive
}
