package validate

import (
	"slices"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/rule"
)

// Check names what a problem was found by, which is also what it stopped this migration at.
type Check string

// The checks, in the order they run: a migration must be read before anything can judge what it does.
const (
	CheckFormat      Check = "format"
	CheckDestructive Check = "destructive"
	CheckCompile     Check = "compile"
	CheckCapability  Check = "capability"
	CheckLock        Check = "lock"
	CheckReverse     Check = "reverse"
	CheckReference   Check = "reference"
	CheckLint        Check = "lint"
)

// reference sits here rather than beside capability, the other check that wants a database, because
// this list is the order the checks run in: it needs both halves of a migration, and the down half
// is what reverse has just worked out.

// checks lists every check a run performs, in the order it performs them.
var checks = []Check{CheckFormat, CheckDestructive, CheckCompile, CheckCapability, CheckLock,
	CheckReverse, CheckReference, CheckLint}

// Checks returns every check a validation run performs, in order, as a copy.
func Checks() []Check {
	return slices.Clone(checks)
}

// Valid reports whether a value names one of the checks.
func (c Check) Valid() bool {
	return slices.Contains(checks, c)
}

// String returns the check's wire name, which is what --json renders and a report prints.
func (c Check) String() string {
	return string(c)
}

// Problem is one thing wrong with one migration, and where in it.
type Problem struct {
	// Migration is the migration at fault, so every message names it (invariant #9).
	Migration migration.Migration
	// Check is what found the problem.
	Check Check
	// Rule is the lint rule that found it, empty for a check that is not a rule.
	Rule rule.ID
	// Direction is which of a migration's two halves the problem is in.
	Direction ir.Direction
	// Operation is the operation's place in that half, counting from one, zero when it is not one.
	Operation int32
	// Kind is the operation at fault, empty when the problem is not about one.
	Kind ir.Kind
	// Detail states what is wrong, in terms its author can act on.
	Detail string
}
