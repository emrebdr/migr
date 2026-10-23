package zerodowntime

import (
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
)

func TestAPromiseOfNoWaitFailsPastTheBudgetAndAnHonestWorstNeverFails(t *testing.T) {
	budget := 500 * time.Millisecond

	for name, tc := range map[string]struct {
		declared capability.LockImpact
		worst    time.Duration
		fails    bool
		notes    string
	}{
		"free and quiet":         {capability.LockFree, 40 * time.Millisecond, false, "kept"},
		"free and blocking":      {capability.LockFree, 2 * time.Second, true, ""},
		"brief and blocking":     {capability.BriefExclusive, 600 * time.Millisecond, true, ""},
		"brief at the budget":    {capability.BriefExclusive, budget, false, "kept"},
		"rewrite and blocking":   {capability.TableRewrite, 9 * time.Second, false, "honest"},
		"long and blocking":      {capability.LongExclusive, 3 * time.Second, false, "honest"},
		"unknown and blocking":   {capability.LockUnknown, 3 * time.Second, false, "honest"},
		"rewrite that was quiet": {capability.TableRewrite, 10 * time.Millisecond, false, "over-report"},
	} {
		t.Run(name, func(t *testing.T) {
			v := judge(measured{Case: "c", Direction: ir.Up, Declared: tc.declared, Worst: tc.worst,
				Query: "a read", Queries: 10}, budget)

			if (v.Failed != "") != tc.fails {
				t.Errorf("declared %s and waited %s judged %+v, want failed %v", tc.declared, tc.worst, v, tc.fails)
			}

			if tc.fails && !strings.Contains(v.Failed, "a read") {
				t.Errorf("the failure %q does not name the query that waited", v.Failed)
			}

			if !strings.Contains(v.Note, tc.notes) {
				t.Errorf("the note %q does not say %q", v.Note, tc.notes)
			}
		})
	}

	if v := judge(measured{Declared: ""}, budget); v.Failed != "" || !strings.Contains(v.Note, "no operation") {
		t.Errorf("a direction that runs nothing judged %+v, want a note saying so", v)
	}

	if v := judge(measured{Declared: capability.LockFree}, budget); v.Failed == "" {
		t.Error("a direction no client query overlapped passed, having measured nothing")
	}
}

func TestTheWorstOfADirectionIsItsCostliestStep(t *testing.T) {
	got := worse(worse(worse("", capability.LockFree), capability.TableRewrite), capability.BriefExclusive)
	if got != capability.TableRewrite {
		t.Errorf("free, rewrite and brief together cost %s, want table_rewrite", got)
	}
}

func TestAQueryStillInFlightCountsForAsLongAsItHasWaited(t *testing.T) {
	done := []sample{
		{start: 0, end: 10 * time.Millisecond, query: "before the window"},
		{start: 95 * time.Millisecond, end: 120 * time.Millisecond, query: "inside"},
		{start: 90 * time.Millisecond, end: 400 * time.Millisecond, query: "straddling"},
	}
	flying := map[int]sample{0: {start: 150 * time.Millisecond, query: "never answered"}}

	worst, which, count := worstOf(done, flying, 100*time.Millisecond, 200*time.Millisecond, 2*time.Second)
	if which != "never answered" || worst != 1850*time.Millisecond || count != 3 {
		t.Errorf("the worst is %s (%s) of %d, want the unanswered query's 1.85s of 3", worst, which, count)
		t.Log("A query blocked until the operation ends would otherwise not be counted at all.")
	}
}

func TestTheBudgetIsTheDefaultOrAValidOverride(t *testing.T) {
	if got, err := budgetFrom(""); err != nil || got != DefaultBudget {
		t.Errorf("an unset budget is %s (%v), want %s", got, err, DefaultBudget)
	}

	if got, err := budgetFrom("2s"); err != nil || got != 2*time.Second {
		t.Errorf("2s read as %s (%v)", got, err)
	}

	for _, bad := range []string{"two seconds", "0s", "-1s"} {
		if _, err := budgetFrom(bad); err == nil {
			t.Errorf("%q was accepted as a budget", bad)
		}
	}
}

func TestEveryKindIsReachedByACaseGoingUpOrComingDown(t *testing.T) {
	ty, err := readTypes()
	if err != nil {
		t.Fatal(err)
	}

	n := names{main: "m", side: "s", other: "o"}
	reached := map[ir.Kind]bool{}

	for _, sc := range scenarios() {
		for _, op := range sc.change(n, ty, fakeLoad{}, "fake") {
			reached[op.Kind()] = true

			if sc.authored {
				continue
			}

			reverse, err := op.Reverse()
			if err != nil {
				t.Errorf("%s's change has no derived down: %v", sc.name, err)

				continue
			}

			reached[reverse.Kind()] = true
		}
	}

	if missing := unreached(reached); len(missing) > 0 {
		t.Errorf("no case reaches %v, so a driver's verdict on it would never be held", missing)
	}
}
