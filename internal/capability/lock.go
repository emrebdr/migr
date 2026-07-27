package capability

import "slices"

// LockImpact is what an operation costs a live database while it runs.
type LockImpact string

// The lock verdicts. LockUnknown is a declaration of ignorance, not a fifth cost.
const (
	LockFree       LockImpact = "lock_free"
	BriefExclusive LockImpact = "brief_exclusive"
	LongExclusive  LockImpact = "long_exclusive"
	TableRewrite   LockImpact = "table_rewrite"
	LockUnknown    LockImpact = "unknown"
)

// Severities, where an unrecognized verdict outranks even a declared LockUnknown.
const (
	severityLockFree = iota + 1
	severityBriefExclusive
	severityLongExclusive
	severityTableRewrite
	severityUnrecognized
)

// ceilings lists every verdict safety.max_lock_impact may be set to, cheapest first.
var ceilings = []LockImpact{LockFree, BriefExclusive, LongExclusive, TableRewrite}

// Ceilings returns the verdicts a threshold may be set to, cheapest first, as a copy.
func Ceilings() []LockImpact {
	return slices.Clone(ceilings)
}

// Impacts returns every verdict a driver may report, including LockUnknown, as a copy.
func Impacts() []LockImpact {
	return append(Ceilings(), LockUnknown)
}

// Valid reports whether a value names a lock verdict this build recognizes.
func (l LockImpact) Valid() bool {
	return l.severity() != severityUnrecognized
}

// String returns the verdict's wire name.
func (l LockImpact) String() string {
	return string(l)
}

// severity ranks a verdict, with LockUnknown ranked as pessimistically as it can honestly be.
func (l LockImpact) severity() int {
	switch l {
	case LockFree:
		return severityLockFree
	case BriefExclusive:
		return severityBriefExclusive
	case LongExclusive:
		return severityLongExclusive
	case TableRewrite, LockUnknown:
		return severityTableRewrite
	default:
		return severityUnrecognized
	}
}

// AtMost reports whether this verdict is within a configured ceiling, refusing if it cannot tell.
func (l LockImpact) AtMost(ceiling LockImpact) bool {
	if !slices.Contains(ceilings, ceiling) {
		return false
	}

	return l.severity() <= ceiling.severity()
}
