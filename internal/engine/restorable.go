package engine

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// Sense is which of the two ways an operation and the live schema can disagree.
type Sense string

// The two senses, which differ in what they are about and in what permits them.
const (
	// SenseStated is a recorded prior definition the live schema contradicts, which ADR 0008 named.
	SenseStated Sense = "stated"
	// SenseImplied is a change the live schema reveals and no recorded prior definition can restore.
	SenseImplied Sense = "implied"
)

// Disagreement is one operation whose record of the schema the live schema contradicts.
type Disagreement struct {
	// Operation is the operation's place in the half it was emitted for, counting from one.
	Operation int32
	// Kind is the operation at fault.
	Kind ir.Kind
	// Subject is the object it acts on, named the way the operation names it.
	Subject string
	// Sense is which way it and the live schema disagree.
	Sense Sense
	// Detail states the disagreement in terms the migration's author can act on.
	Detail string
}

// Describe renders one disagreement as a line, naming the operation before what is wrong with it.
func (d Disagreement) Describe() string {
	return fmt.Sprintf("operation %d (%s) on %q: %s", d.Operation, d.Kind, d.Subject, d.Detail)
}

// Implied reports whether any of these disagreements is one an authored down could have handled.
func Implied(found []Disagreement) bool {
	for _, one := range found {
		if one.Sense == SenseImplied {
			return true
		}
	}

	return false
}

// Stated returns the disagreements that stand whatever a migration's down says.
func Stated(found []Disagreement) []Disagreement {
	kept := make([]Disagreement, 0, len(found))

	for _, one := range found {
		if one.Sense == SenseStated {
			kept = append(kept, one)
		}
	}

	return kept
}

// Two senses, and they answer different questions. A stated disagreement is ADR 0008's trust
// problem: nine operations carry a from payload describing what they replace, nothing has ever
// verified one, and a payload describing the wrong thing produces a reverse that restores the wrong
// schema. An implied disagreement is what the live schema reveals an operation will change that no
// payload records at all -- the index that goes with a dropped column, and the nullability a primary
// key implies.
//
// It reasons only about what the IR records against what the snapshot holds, and names no engine:
// CLAUDE.md §11 puts an engine difference in the capability system. So MySQL's own residue -- the
// index it creates for a foreign key over an unindexed column -- is not here, because that is
// something a server adds rather than something the operation set cannot record.
//
// One operation is judged only where nothing earlier in the same list touched what it names, and
// nothing at all is judged after a raw statement. An earlier operation moves the very state this
// compares against, and a raw statement can move anything, so judging past either would refuse
// correct migrations -- which is strictly worse than missing one.

// Unrestorable returns every operation in a list this live schema shows cannot be undone as recorded.
func Unrestorable(ops ir.Operations, live *schema.Snapshot) []Disagreement {
	if live == nil {
		return nil
	}

	var (
		found  []Disagreement
		ledger = newLedger()
		index  int32
		opaque bool
	)

	for _, op := range ops {
		index++

		if opaque {
			break
		}

		if op == nil {
			continue
		}

		if _, isRaw := op.(ir.Raw); isRaw {
			opaque = true

			continue
		}

		found = append(found, judge(op, index, live, ledger)...)
		ledger.touch(op)
	}

	return found
}

// judge reports what one operation and the live schema disagree about, where it can be judged.
func judge(op ir.Operation, index int32, live *schema.Snapshot, ledger *ledger) []Disagreement {
	if ledger.moved(op) {
		return nil
	}

	var found []Disagreement

	for _, detail := range statedBy(op, live) {
		found = append(found, Disagreement{Operation: index, Kind: op.Kind(),
			Subject: Subject(op), Sense: SenseStated, Detail: detail})
	}

	for _, detail := range impliedBy(op, live) {
		found = append(found, Disagreement{Operation: index, Kind: op.Kind(),
			Subject: Subject(op), Sense: SenseImplied, Detail: detail})
	}

	return found
}

// describeFindings counts disagreements with the right noun, since one operation can make two.
func describeFindings(n int) string {
	if n == 1 {
		return "1 disagreement"
	}

	return fmt.Sprintf("%d disagreements", n)
}

// describeDisagreements lists what each operation disagrees about, one indented line apiece.
func describeDisagreements(found []Disagreement) string {
	lines := make([]string, 0, len(found))
	for _, one := range found {
		lines = append(lines, "  "+one.Describe())
	}

	return strings.Join(lines, "\n")
}
