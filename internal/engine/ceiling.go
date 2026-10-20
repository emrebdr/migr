package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// ErrLockImpact marks operations that cost a live database more than safety.max_lock_impact allows.
var ErrLockImpact = errors.New("an operation exceeds the lock ceiling")

// Breach is one operation whose cost to a live database is above the configured ceiling.
type Breach struct {
	// Operation is the operation's place in the half it was emitted for, counting from one.
	Operation int32
	// Kind is the operation at fault.
	Kind ir.Kind
	// Subject is the object it acts on, named the way the operation names it.
	Subject string
	// Lock is what the operation costs as it would run, a rewrite's cost where it has one.
	Lock capability.LockImpact
	// Rewrite is the safer sequence it would run as, empty where it runs as declared.
	Rewrite driver.RewriteName
	// Assessed reports a verdict a session read the table for, rather than one declared at its worst.
	Assessed bool
}

// Describe renders one breach as a line, naming the operation, its kind, and what it costs.
func (b Breach) Describe() string {
	line := fmt.Sprintf("operation %d (%s) on %q: %s", b.Operation, b.Kind, b.Subject, b.Lock)

	switch {
	case b.Rewrite != "":
		line += fmt.Sprintf(", even rewritten (safety.rewrites.%s)", b.Rewrite)
	case b.Assessed:
		line += ", having read the table"
	}

	return line
}

// Exceeding returns every operation in a plan that costs more than the ceiling, in the order emitted.
func Exceeding(compiled plan.Plan, ceiling capability.LockImpact) []Breach {
	var found []Breach

	for _, step := range compiled.Steps {
		if step.Verdict.Support() == capability.SupportUnsupported || step.Lock().AtMost(ceiling) {
			continue
		}

		found = append(found, breachOf(step))
	}

	return found
}

// breachOf records one step whose cost is over the ceiling.
func breachOf(step plan.Step) Breach {
	b := Breach{
		Operation: step.Index,
		Kind:      kindOf(step.Op),
		Subject:   Subject(step.Op),
		Lock:      step.Lock(),
		Assessed:  step.Assessed,
	}

	if step.Rewrite != nil {
		b.Rewrite = step.Rewrite.Name
	}

	return b
}

// Exceeded is one migration holding operations over the ceiling, and which ones.
type Exceeded struct {
	// Migration is the file at fault, so the failure names it (invariant #9).
	Migration migration.Migration
	// Direction is the half that was judged.
	Direction ir.Direction
	// Found is every operation in that half over the ceiling, in the order the migration emitted them.
	Found []Breach
}

// LockImpactError fails a gate that found operations costing more than safety.max_lock_impact allows.
type LockImpactError struct {
	// Ceiling is the most a live database may be made to pay, as migr.yaml sets it.
	Ceiling capability.LockImpact
	// Exceeded is every migration holding an operation over it, in the order they would run.
	Exceeded []Exceeded
}

// lockImpactTemplate names the ceiling, every operation over it, and the two ways to settle it.
const lockImpactTemplate = `%s %s safety.max_lock_impact (%s):

%s

Each would block a live database's reads or writes for longer than this project allows. Make the
operation cheaper -- a rewrite, an expand and contract, or a smaller step -- or raise the ceiling in
migr.yaml, which is a decision a review can see. A raw statement is unknown, which counts as
table_rewrite.`

// Error names the ceiling and every operation over it, each beside the migration it is in.
func (e *LockImpactError) Error() string {
	verb := "exceed"
	if e.count() == 1 {
		verb = "exceeds"
	}

	return fmt.Sprintf(lockImpactTemplate, describeOperations(e.count()), verb, e.Ceiling, e.describe())
}

// Unwrap reports the sentinel so errors.Is(err, ErrLockImpact) succeeds, and nothing else.
func (e *LockImpactError) Unwrap() error {
	return ErrLockImpact
}

// count is how many operations are over the ceiling, across every migration.
func (e *LockImpactError) count() int {
	n := 0
	for _, m := range e.Exceeded {
		n += len(m.Found)
	}

	return n
}

// describe lists every operation over the ceiling, each under the migration and half it is in.
func (e *LockImpactError) describe() string {
	lines := make([]string, 0, e.count())

	for _, m := range e.Exceeded {
		for _, b := range m.Found {
			lines = append(lines, fmt.Sprintf("  %s (%s), %s", m.Migration.Filename(), m.Direction,
				b.Describe()))
		}
	}

	return strings.Join(lines, "\n")
}
