package validate

import "slices"

// Report is everything wrong with a set of migrations, in the order the migrations were checked.
type Report struct {
	// problems are the problems found, ordered by migration and then by where in it each sits.
	problems []Problem
	// checked is how many migrations were looked at, which a report of no problems still states.
	checked int
}

// Problems returns every problem found, in the order they were found, as a copy.
func (r Report) Problems() []Problem {
	return slices.Clone(r.problems)
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
