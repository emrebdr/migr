package sqlite

import (
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
)

// declared is what this driver says each operation costs a live database.
var declared = map[ir.Kind]capability.LockImpact{
	ir.KindCreateTable:    capability.BriefExclusive,
	ir.KindCreateView:     capability.BriefExclusive,
	ir.KindDropView:       capability.BriefExclusive,
	ir.KindAddColumn:      capability.BriefExclusive,
	ir.KindRenameTable:    capability.BriefExclusive,
	ir.KindRenameColumn:   capability.BriefExclusive,
	ir.KindDropTable:      capability.LongExclusive,
	ir.KindAddIndex:       capability.LongExclusive,
	ir.KindDropIndex:      capability.LongExclusive,
	ir.KindAddUnique:      capability.LongExclusive,
	ir.KindDropUnique:     capability.LongExclusive,
	ir.KindDropColumn:     capability.TableRewrite,
	ir.KindAlterColumn:    capability.TableRewrite,
	ir.KindAddForeignKey:  capability.TableRewrite,
	ir.KindDropForeignKey: capability.TableRewrite,
	ir.KindAddCheck:       capability.TableRewrite,
	ir.KindDropCheck:      capability.TableRewrite,
	ir.KindAddPrimaryKey:  capability.TableRewrite,
	ir.KindDropPrimaryKey: capability.TableRewrite,
	ir.KindRaw:            capability.LockUnknown,
}

func TestEveryOperationCostsSomethingThisDriverCanName(t *testing.T) {
	for _, kind := range ir.Kinds() {
		want, listed := declared[kind]
		if !listed {
			t.Fatalf("%s is an operation this driver's lock table says nothing about", kind)
		}

		if got := lockImpact(representative(t, kind)); got != want {
			t.Errorf("lockImpact(%s) = %s, want %s", kind, got, want)
		}

		if !want.Valid() {
			t.Errorf("%s is declared %q, which no ceiling recognizes", kind, want)
		}
	}

	t.Log("The table is walked from ir.Kinds() rather than from the driver's own map, so a")
	t.Log("twenty-first operation fails here instead of falling to the default arm and reading")
	t.Log("`unknown` for the rest of its life.")
}

func TestNothingOnThisEngineIsLockFree(t *testing.T) {
	for _, kind := range ir.Kinds() {
		if got := lockImpact(representative(t, kind)); got == capability.LockFree {
			t.Errorf("%s is declared lock_free", kind)
		}
	}

	t.Log("SQLite takes one database-wide write lock and has no per-table locking at all, so even")
	t.Log("CREATE TABLE stops every other writer for as long as it runs -- and in the default")
	t.Log("rollback-journal mode, which this driver does not change (ADR 0050), it stops readers")
	t.Log("too. PostgreSQL calls create_table lock_free because it blocks nothing that already")
	t.Log("exists; here that would pass --require-safe for an operation that halts the database.")
}

func TestTheSevenEmulatedOperationsCostWhatTheRebuildCosts(t *testing.T) {
	for _, kind := range ir.Kinds() {
		if !needsRebuild[kind] {
			continue
		}

		op := representative(t, kind)

		if got := lockImpact(op); got != rebuildImpact {
			t.Errorf("%s is declared %s, want %s", kind, got, rebuildImpact)
		}

		verdict := linked(t).verdict(op)
		if verdict.Lock() != rebuildImpact {
			t.Errorf("%s resolves to lock %s, want %s", kind, verdict.Lock(), rebuildImpact)
		}

		if verdict.Strategy() != rebuildStrategy {
			t.Errorf("%s names strategy %q, want %q", kind, verdict.Strategy(), rebuildStrategy)
		}
	}

	t.Log("M8-T3 wrote these verdicts while the seven still refused, so nothing could reach them")
	t.Log("through Resolve: a refusal carries LockUnknown by construction. They are reachable now,")
	t.Log("and the value the declaration reports is the one that was measured a task ago.")
}

func TestAnOperationThisDriverCannotPlaceCostsTheWorst(t *testing.T) {
	if got := lockImpact(nil); got != capability.LockUnknown {
		t.Errorf("an unrecognized operation is declared %s", got)
	}

	if capability.LockUnknown.AtMost(capability.LongExclusive) {
		t.Error("the fallback satisfies a long_exclusive ceiling")
	}

	t.Log("Every kind ir.Kinds() lists is named in the switch, so this arm is only reachable by an")
	t.Log("operation this build does not know -- a twenty-first one, or nothing at all. Invariant")
	t.Log("#13 says that when a driver cannot determine the answer it reports the pessimistic")
	t.Log("verdict, and LockUnknown ranks with table_rewrite against every ceiling.")
}

func TestARawStatementCostsWhatNothingCanKnow(t *testing.T) {
	if got := lockImpact(ir.Raw{Driver: Name, SQL: "VACUUM"}); got != capability.LockUnknown {
		t.Errorf("raw is declared %s", got)
	}

	if capability.LockUnknown.AtMost(capability.LongExclusive) {
		t.Error("an unknown impact satisfies a long_exclusive ceiling")
		t.Log("LockUnknown is a declaration of ignorance ranked as pessimistically as it honestly")
		t.Log("can be, so --require-safe refuses a raw statement rather than waving it through.")
	}
}
