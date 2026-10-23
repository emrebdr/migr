package zerodowntime

import (
	"fmt"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
)

// measured is one direction of one case: what it declared and what its clients waited.
type measured struct {
	// Case names the case.
	Case string
	// Direction is up or down.
	Direction ir.Direction
	// Declared is the worst step's cost, which is what the direction promised.
	Declared capability.LockImpact
	// Worst is the longest any client query waited while the direction ran.
	Worst time.Duration
	// Query names that query.
	Query string
	// Queries is how many client queries ran in the window, which a load that did nothing would make zero.
	Queries int
}

// verdict is what the judgment made of one direction.
type verdict struct {
	// Failed is why the direction fails, empty where it passes.
	Failed string
	// Note is what the run logs about a passing direction, empty where there is nothing to say.
	Note string
}

// judge holds one direction to its declared cost: a promise of no wait must keep every query inside the budget.
func judge(m measured, budget time.Duration) verdict {
	where := fmt.Sprintf("%s %s, declared %s", m.Case, m.Direction, m.Declared)

	switch {
	case m.Declared == "":
		return verdict{Note: fmt.Sprintf("%s %s runs no operation, and the worst client query took %s",
			m.Case, m.Direction, m.Worst)}
	case m.Queries == 0:
		return verdict{Failed: where + ": no client query ran while it did, so nothing was measured"}
	case !promisesNoWait(m.Declared):
		if m.Worst <= budget {
			return verdict{Note: fmt.Sprintf("%s, and the worst client query took %s: an over-report, "+
				"which passes", where, m.Worst)}
		}

		return verdict{Note: fmt.Sprintf("%s, and a client query waited %s: honest", where, m.Worst)}
	case m.Worst > budget:
		return verdict{Failed: fmt.Sprintf("%s, and a client query waited %s, past the %s budget: %s",
			where, m.Worst, budget, m.Query)}
	default:
		return verdict{Note: fmt.Sprintf("%s, and the worst client query took %s: kept", where, m.Worst)}
	}
}

// promisesNoWait reports a cost that says no client waits on it, which the budget holds.
func promisesNoWait(lock capability.LockImpact) bool {
	return lock == capability.LockFree || lock == capability.BriefExclusive
}

// worse returns the costlier of two impacts, an unset one being no cost yet.
func worse(a, b capability.LockImpact) capability.LockImpact {
	if a == "" || (b != "" && a.AtMost(b)) {
		return b
	}

	return a
}
