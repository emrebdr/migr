package validate

import (
	"slices"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// Unchecked is one half of one migration a run could not judge, and what stopped it.
type Unchecked struct {
	// Migration is the migration that could not be checked, so this names it too (invariant #9).
	Migration migration.Migration
	// Direction is which of its two halves went unchecked.
	Direction ir.Direction
	// Reason states what stopped the check, in terms an operator can act on.
	Reason string
}

// Report is everything wrong with a set of migrations, in the order the migrations were checked.
type Report struct {
	// problems are the problems found, ordered by migration and then by where in it each sits.
	problems []Problem
	// unchecked are the halves this run could not judge at all, which are not problems.
	unchecked []Unchecked
	// checked is how many migrations were looked at, which a report of no problems still states.
	checked int
}

// Problems returns every problem found, in the order they were found, as a copy.
func (r Report) Problems() []Problem {
	return slices.Clone(r.problems)
}

// Unchecked returns every half this run could not judge, in the order it reached them, as a copy.
func (r Report) Unchecked() []Unchecked {
	return slices.Clone(r.unchecked)
}

// Checked returns how many migrations were looked at.
func (r Report) Checked() int {
	return r.checked
}

// Clean reports whether nothing at all was found wrong.
func (r Report) Clean() bool {
	return len(r.problems) == 0
}

// Migrations returns how many distinct migrations carry a problem, walking the ordered list.
func (r Report) Migrations() int {
	total, previous := 0, ""

	for _, problem := range r.problems {
		if version := problem.Migration.Version; version != previous {
			previous = version
			total++
		}
	}

	return total
}

// add records one problem, keeping the order the checks found them in.
func (r *Report) add(problem Problem) {
	r.problems = append(r.problems, problem)
}

// skip records one half nothing could judge, which is reported rather than counted as passing.
func (r *Report) skip(unchecked Unchecked) {
	r.unchecked = append(r.unchecked, unchecked)
}
