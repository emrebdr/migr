package mysql

import (
	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
)

// rawImpact is permanent rather than pending: the framework cannot know what a raw statement does.
const rawImpact = capability.LockUnknown

// The release where MySQL began adding a column without rewriting the table.
//
// Below it an ADD COLUMN is an INPLACE rebuild. MariaDB has added instantly since 10.4.3, below this
// driver's floor, so the gate is MySQL's alone and MariaDB never reaches the rewrite branch.
const (
	instantAddMajor = 8
	instantAddMinor = 0
	instantAddPatch = 12
)

// The release where MySQL began dropping a column without rewriting the table.
//
// Below it a DROP COLUMN is an INPLACE rebuild, which is the one verdict that separates the two
// variants across this driver's whole supported range: MariaDB has dropped instantly since 10.4.3.
const (
	instantDropMajor = 8
	instantDropMinor = 0
	instantDropPatch = 29
)

// lockImpact reports what an operation costs a live database on the server that was detected.
func (f serverFacts) lockImpact(op ir.Operation) capability.LockImpact {
	switch o := op.(type) {
	case ir.CreateTable:
		return capability.LockFree
	case ir.CreateView:
		return viewImpact(o)
	case ir.AddColumn:
		return f.addColumnImpact(o)
	case ir.DropColumn:
		return f.dropColumnImpact()
	case ir.AlterColumn:
		return alterColumnImpact(o)
	case ir.AddIndex, ir.AddUnique:
		return capability.LongExclusive
	case ir.DropTable:
		return capability.LongExclusive
	case ir.AddCheck, ir.AddForeignKey, ir.AddPrimaryKey, ir.DropPrimaryKey:
		return capability.TableRewrite
	case ir.RenameTable, ir.RenameColumn, ir.DropIndex, ir.DropUnique, ir.DropForeignKey,
		ir.DropCheck, ir.DropView:
		return capability.BriefExclusive
	case ir.Raw:
		return rawImpact
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

// addColumnImpact reports whether this server stores the new column's default or writes every row.
func (f serverFacts) addColumnImpact(op ir.AddColumn) capability.LockImpact {
	if !f.version.addsInstantly() || f.version.materializesDefault(op.Column) {
		return capability.TableRewrite
	}

	return capability.BriefExclusive
}

// dropColumnImpact reports whether this server drops a column by metadata or by rebuilding.
func (f serverFacts) dropColumnImpact() capability.LockImpact {
	if !f.version.dropsInstantly() {
		return capability.TableRewrite
	}

	return capability.BriefExclusive
}

// addsInstantly reports a server that adds a column without rewriting the rows already there.
func (v serverVersion) addsInstantly() bool {
	return v.AtLeastPatch(MySQL, instantAddMajor, instantAddMinor, instantAddPatch) ||
		v.variant == MariaDB
}

// dropsInstantly reports a server that drops a column without rewriting the rows already there.
func (v serverVersion) dropsInstantly() bool {
	return v.AtLeastPatch(MySQL, instantDropMajor, instantDropMinor, instantDropPatch) ||
		v.variant == MariaDB
}

// materializesDefault reports a default this server writes into every existing row (doc.go).
func (v serverVersion) materializesDefault(c ir.Column) bool {
	if v.variant == MariaDB {
		return perRowDefault(c.Default)
	}

	return isExpressionDefault(c)
}

// perRowDefault reports a default whose value differs per row, so no single value can be stored.
func perRowDefault(d *ir.Default) bool {
	return d != nil && d.Literal == nil && d.Function == ir.FuncUUID
}

// alterColumnImpact reports the redefinition as a rewrite unless only the default is moving.
func alterColumnImpact(op ir.AlterColumn) capability.LockImpact {
	if op.Type != nil || op.Nullable != nil {
		return capability.TableRewrite
	}

	return capability.BriefExclusive
}
