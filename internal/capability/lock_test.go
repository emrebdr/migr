package capability

import (
	"slices"
	"testing"
)

// atMostTable is the whole of AtMost written out rather than computed, so that any reordering,
// any off-by-one, and any misranking of LockUnknown flips at least one cell.
var atMostTable = map[LockImpact][]bool{
	//              lock_free  brief   long   rewrite
	LockFree:       {true, true, true, true},
	BriefExclusive: {false, true, true, true},
	LongExclusive:  {false, false, true, true},
	TableRewrite:   {false, false, false, true},
	LockUnknown:    {false, false, false, true},
}

func TestAThresholdAcceptsExactlyWhatItSays(t *testing.T) {
	for _, verdict := range Impacts() {
		row, covered := atMostTable[verdict]
		if !covered {
			t.Errorf("%s is a verdict a driver may report but the table does not cover it", verdict)

			continue
		}

		for i, ceiling := range Ceilings() {
			if got := verdict.AtMost(ceiling); got != row[i] {
				t.Errorf("%s.AtMost(%s) = %v, want %v", verdict, ceiling, got, row[i])
			}
		}
	}
}

func TestAnUnresolvedVerdictIsAsSevereAsATableRewrite(t *testing.T) {
	for _, ceiling := range []LockImpact{LockFree, BriefExclusive, LongExclusive} {
		if LockUnknown.AtMost(ceiling) {
			t.Errorf("an unresolved verdict passed a %s ceiling", ceiling)
			t.Log("Until lock classification lands, every verdict is unknown. If unknown compares")
			t.Log("as cheap the gate approves an unclassified table rewrite, which is worse than")
			t.Log("having no gate at all, because its presence reads as evidence of a check.")
		}
	}

	if !LockUnknown.AtMost(TableRewrite) {
		t.Error("an unresolved verdict was refused by the loosest ceiling there is")
		t.Log("table_rewrite is the documented \"I accept any lock cost\" setting. Ranking unknown")
		t.Log("above it leaves no opt-out, and a safety gate with no opt-out is switched off.")
	}
}

func TestAnUnresolvedVerdictNeverRendersAsAMeasuredOne(t *testing.T) {
	if LockUnknown.String() == TableRewrite.String() {
		t.Error("an unresolved verdict renders as a measured table rewrite")
		t.Log("Unknown and table_rewrite share a severity so the gate errs safely. They must not")
		t.Log("share a rendering: claiming a rewrite the driver never detected is the confidently")
		t.Log("wrong answer the lock axis exists to prevent, and it hides M13-T1's own worklist.")
	}

	if LockUnknown.String() != "unknown" {
		t.Errorf("LockUnknown renders as %q, want %q", LockUnknown, "unknown")
	}
}

func TestAnUnrecognizedVerdictIsWorseThanAnAdmittedlyUnknownOne(t *testing.T) {
	for _, impact := range []LockImpact{"", "lock-free", "LOCK_FREE", "instant"} {
		if impact.AtMost(TableRewrite) {
			t.Errorf("%q passed the loosest ceiling there is", impact)
			t.Log("A declared unknown earns the table_rewrite opt-out because the driver made a")
			t.Log("statement. A value this build cannot rank is a bug, and a bug earns nothing.")
			t.Log("The zero LockImpact is the case that matters: a driver that never set the")
			t.Log("field must not be read as safe.")
		}

		if impact.Valid() {
			t.Errorf("%q reported itself as a verdict a driver may report", impact)
		}
	}
}

func TestUnknownIsAVerdictButNeverAThreshold(t *testing.T) {
	if !LockUnknown.Valid() {
		t.Error("a driver cannot report an unresolved verdict at all")
	}

	if slices.Contains(Ceilings(), LockUnknown) {
		t.Errorf("Ceilings() = %v, which offers unknown as a threshold", Ceilings())
		t.Log("\"Block anything worse than unknown\" is not a sentence with a meaning. Keeping the")
		t.Log("two sets separate is what lets config validate against Ceilings() with no special")
		t.Log("case, so max_lock_impact: unknown is rejected by the same code path as a typo.")
	}

	if !slices.Contains(Impacts(), LockUnknown) {
		t.Errorf("Impacts() = %v, which omits the verdict every driver reports today", Impacts())
	}

	for _, verdict := range Impacts() {
		if verdict.AtMost(LockUnknown) {
			t.Errorf("%s was accepted against an unknown ceiling", verdict)
		}
	}
}

func TestTheCeilingsRunCheapestToMostDisruptive(t *testing.T) {
	want := []LockImpact{LockFree, BriefExclusive, LongExclusive, TableRewrite}

	if !slices.Equal(Ceilings(), want) {
		t.Errorf("Ceilings() = %v, want %v", Ceilings(), want)
		t.Log("The order is the API: config renders this list in its \"expected\" message and the")
		t.Log("published safety tables read in it.")
	}

	if got := Impacts(); !slices.Equal(got, append(want, LockUnknown)) {
		t.Errorf("Impacts() = %v, want the ceilings followed by unknown", got)
	}
}

func TestTheVerdictSetsAreCopies(t *testing.T) {
	Ceilings()[0] = "tampered"
	Impacts()[0] = "tampered"
	Supports()[0] = "tampered"

	if Ceilings()[0] != LockFree || Impacts()[0] != LockFree {
		t.Errorf("Ceilings() = %v after a caller wrote to an earlier result", Ceilings())
	}

	if Supports()[0] != SupportNative {
		t.Errorf("Supports() = %v after a caller wrote to an earlier result", Supports())
	}
}

func TestEveryVerdictRendersAsItsFrozenWireName(t *testing.T) {
	impacts := map[LockImpact]string{
		LockFree:       "lock_free",
		BriefExclusive: "brief_exclusive",
		LongExclusive:  "long_exclusive",
		TableRewrite:   "table_rewrite",
		LockUnknown:    "unknown",
	}

	for impact, want := range impacts {
		if impact.String() != want {
			t.Errorf("%q renders as %q, want %q", string(impact), impact, want)
		}
	}

	if len(impacts) != len(Impacts()) {
		t.Errorf("%d verdicts exist but %d spellings are pinned", len(Impacts()), len(impacts))
	}

	supports := map[Support]string{
		SupportNative:      "native",
		SupportEmulated:    "emulated",
		SupportUnsupported: "unsupported",
	}

	for support, want := range supports {
		if support.String() != want {
			t.Errorf("%q renders as %q, want %q", string(support), support, want)
		}
	}

	t.Log("These reach migr.yaml, host.capability.get, and the published per-driver tables, so")
	t.Log("they are a compatibility surface rather than display text. The four lock spellings")
	t.Log("were already frozen by internal/config and its testdata before this package existed.")
}

func TestOnlyDeclaredSupportVerdictsAreValid(t *testing.T) {
	for _, support := range Supports() {
		if !support.Valid() {
			t.Errorf("%q is listed as a verdict but reports itself invalid", support)
		}
	}

	for _, support := range []Support{"", "NATIVE", "emulate", "supported"} {
		if support.Valid() {
			t.Errorf("%q reported itself as a verdict a driver may report", support)
		}
	}
}
