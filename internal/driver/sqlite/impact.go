package sqlite

import (
	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
)

// rawImpact is permanent rather than pending: the framework cannot know what a raw statement does.
const rawImpact = capability.LockUnknown

// rebuildImpact is what the table rebuild costs, and is what M8-T4's Emulated verdicts carry.
const rebuildImpact = capability.TableRewrite

// lockImpact reports what an operation costs a live database, measured rather than assumed.
func lockImpact(op ir.Operation) capability.LockImpact {
	switch op.(type) {
	case ir.CreateTable, ir.CreateView, ir.DropView, ir.AddColumn,
		ir.RenameTable, ir.RenameColumn:
		return capability.BriefExclusive
	case ir.DropTable, ir.AddIndex, ir.DropIndex, ir.AddUnique, ir.DropUnique:
		return capability.LongExclusive
	case ir.DropColumn:
		return capability.TableRewrite
	case ir.AlterColumn, ir.AddForeignKey, ir.DropForeignKey, ir.AddCheck, ir.DropCheck,
		ir.AddPrimaryKey, ir.DropPrimaryKey:
		return rebuildImpact
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
