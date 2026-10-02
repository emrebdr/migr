package history

import "slices"

// Outcome is how one migration run ended, which is not the same axis as its reconciled state.
type Outcome string

// The run outcomes. The zero Outcome names none of them and is not an outcome.
const (
	Applied  Outcome = "applied"
	Failed   Outcome = "failed"
	Dirty    Outcome = "dirty"
	Resolved Outcome = "resolved"
)

// outcomes lists every run outcome, from the one that changed the schema to the one that may have.
var outcomes = []Outcome{Applied, Failed, Dirty, Resolved}

// Outcomes returns every outcome a run may be recorded with, as a copy.
func Outcomes() []Outcome {
	return slices.Clone(outcomes)
}

// Valid reports whether a value names one of the three run outcomes.
func (o Outcome) Valid() bool {
	return slices.Contains(outcomes, o)
}

// String returns the outcome's wire name.
func (o Outcome) String() string {
	return string(o)
}

// Blocking reports whether this outcome stops the next migration until it is resolved.
func (o Outcome) Blocking() bool {
	return o == Dirty
}
