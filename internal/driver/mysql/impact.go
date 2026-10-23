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

// The most instant row versions MySQL lets one table accumulate before it rebuilds it instead.
//
// Every instant ADD COLUMN and DROP COLUMN takes one, from the release where instant drops began, and
// at this many a bare ADD COLUMN silently rebuilds the table -- measured on 8.0.46, the TABLE_ID moved
// and the count went back to 0, where ALGORITHM=INSTANT is error 4092. MariaDB keeps no such count.
const maxRowVersions = 64

// The conditions an instant change holds on, each read from the table by Assess.
const (
	instantTableCondition = "the table is not ROW_FORMAT=COMPRESSED and carries no FULLTEXT index"
	rowVersionCondition   = ", and has fewer than 64 instant row versions -- which only a session " +
		"holding PROCESS can read, so without it this stays a rewrite"
	indexedColumnCondition = ", and the column is in no index"
)

// dependOnTable reports an instant change at its rebuild cost, unless a session shows the table permits it.
func (f serverFacts) dependOnTable(op ir.Operation, v capability.Verdict) capability.Verdict {
	condition := f.version.instantCondition(op)
	if condition == "" || v.Lock() != capability.BriefExclusive {
		return v
	}

	return capability.Native(capability.TableRewrite).Unless(capability.BriefExclusive, condition)
}

// instantCondition says what the table must be for this server to change it instantly, or nothing.
func (v serverVersion) instantCondition(op ir.Operation) string {
	switch op.(type) {
	case ir.AddColumn:
		return instantTableCondition + v.describeRowVersions()
	case ir.DropColumn:
		return instantTableCondition + v.describeRowVersions() + v.describeIndexedColumn()
	default:
		return ""
	}
}

// describeRowVersions names the row-version ceiling on a server that keeps one, and nothing elsewhere.
func (v serverVersion) describeRowVersions() string {
	if !v.countsRowVersions() {
		return ""
	}

	return rowVersionCondition
}

// describeIndexedColumn names the index rule on MySQL, which rebuilds where MariaDB drops in place.
func (v serverVersion) describeIndexedColumn() string {
	if v.variant == MariaDB {
		return ""
	}

	return indexedColumnCondition
}

// countsRowVersions reports a server that numbers every instant change and rebuilds at the ceiling.
func (v serverVersion) countsRowVersions() bool {
	return v.AtLeastPatch(MySQL, instantDropMajor, instantDropMinor, instantDropPatch)
}

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
