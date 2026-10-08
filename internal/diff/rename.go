package diff

import (
	"fmt"
	"slices"

	"github.com/emrebdr/migr/internal/schema"
)

// A rename cannot be inferred. Two snapshots that differ by one record the same thing as two that
// differ by a drop and an unrelated create, so the only honest reading is a heuristic -- and acting
// on a wrong one either destroys data a rename would have kept or keeps data a drop was meant to
// destroy. So detection and emission are two calls with a person between them: Candidates proposes,
// something asks, and Diff is told. Diff takes the answer as a parameter rather than taking the
// asker, because invariant #8 forbids a planning path whose output depends on anything but its input.

// Renames is every rename an operator confirmed, which is the only way one reaches the operations.
type Renames struct {
	// Tables are the confirmed table renames, each naming the schema it is leaving and joining.
	Tables []TableRename
	// Columns are the confirmed column renames, each naming the table it happens in.
	Columns []ColumnRename
}

// TableRename is one table confirmed to be the same table under two names.
type TableRename struct {
	// From is the name the schema being changed holds it under.
	From string
	// To is the name the target schema holds it under.
	To string
}

// A column rename names its table by the name the TARGET schema holds, and that is load-bearing in
// both directions. A table rename runs first, so by the time a column rename runs the table already
// has its new name -- and in a derived down the column rename runs before the table rename undoes
// itself, where the table still has its new name. One rule, correct forwards and backwards.

// ColumnRename is one column confirmed to be the same column under two names.
type ColumnRename struct {
	// Table is the table it belongs to, under the name the target schema holds that table under.
	Table string
	// From is the name the schema being changed holds the column under.
	From string
	// To is the name the target schema holds it under.
	To string
}

// Empty reports whether nothing was confirmed, which is what every caller before M11-T5 passes.
func (r Renames) Empty() bool {
	return len(r.Tables) == 0 && len(r.Columns) == 0
}

// Candidate is one rename this differ thinks likely, which nothing acts on until it is confirmed.
type Candidate struct {
	// Table is the table a column candidate belongs to, and is empty for a table candidate.
	Table string
	// From is the name the schema being changed holds the object under.
	From string
	// To is the name the target schema holds it under.
	To string
	// Evidence says why the pair is likely, in the words a prompt puts to a person.
	Evidence string
}

// Column reports whether the candidate renames a column rather than a table.
func (c Candidate) Column() bool {
	return c.Table != ""
}

// Describe renders one candidate as the line a report prints, the evidence after the pair.
func (c Candidate) Describe() string {
	if c.Column() {
		return fmt.Sprintf("column %s -> %s: %s", qualified(c.Table, c.From),
			qualified(c.Table, c.To), c.Evidence)
	}

	return fmt.Sprintf("table %q -> %q: %s", c.From, c.To, c.Evidence)
}

// Rename returns the confirmation this candidate would become, which is what a caller collects.
func (c Candidate) Rename() Renames {
	if c.Column() {
		return Renames{Columns: []ColumnRename{{Table: c.Table, From: c.From, To: c.To}}}
	}

	return Renames{Tables: []TableRename{{From: c.From, To: c.To}}}
}

// A column rename inside a table that may itself be renamed is a candidate conditional on another
// candidate, which is not a question anybody can answer -- so detection reads a state where the
// renames already confirmed have been applied, and proposes one round deeper each time it is asked.
// Measured by hand: users -> accounts with two of its columns renamed proposes the table first, and
// then, once that is confirmed, the two column renames inside it. That is also why this takes the
// confirmed set rather than being called twice on different schemas: there is one pair of snapshots
// and the rewriting of one of them belongs here, where Diff already does it.

// Candidates returns the renames one comparison makes likely, given what is already confirmed.
func Candidates(from, to schema.Snapshot, confirmed Renames) []Candidate {
	before, after := renamedBy(from.Canonical(), to.Canonical(), confirmed), to.Canonical()

	found := tableCandidates(before, after)

	for _, table := range after.Tables {
		was, existed := before.Table(table.Name)
		if !existed {
			continue
		}

		found = append(found, columnCandidates(was, table)...)
	}

	return found
}

// Nothing filters out a rename that is already confirmed, because applying it is what removes it: the
// object is matched by name in the rewritten schema, so it is no longer a loss paired with a gain.
// A guard here was written first and measured unreachable -- even a confirmed rename this comparison
// refuses is gone from the candidate set, the half of it that did apply having matched the pair.

// Only a pair that would otherwise be a drop and a create is a candidate at all: anything matched by
// name is already matched, and a rename is the reading of a loss and a gain that happen together.

// tableCandidates pairs the tables one schema loses with the ones the other gains.
func tableCandidates(before, after schema.Snapshot) []Candidate {
	lost := lostTables(before, after)
	gained := lostTables(after, before)

	return pairBest(lost, gained, func(was, is schema.Table) (int, string) {
		return tableLikeness(was, is)
	}, func(was, is schema.Table, evidence string) Candidate {
		return Candidate{From: was.Name, To: is.Name, Evidence: evidence}
	})
}

// lostTables names every table one schema holds that the other does not, in canonical order.
func lostTables(held, other schema.Snapshot) []schema.Table {
	var lost []schema.Table

	for _, table := range held.Tables {
		if _, kept := other.Table(table.Name); !kept {
			lost = append(lost, table)
		}
	}

	return lost
}

// columnCandidates pairs the columns one table loses with the ones it gains, within one matched table.
func columnCandidates(before, after schema.Table) []Candidate {
	lost := lostColumns(before, after)
	gained := lostColumns(after, before)

	return pairBest(lost, gained, columnLikeness,
		func(was, is schema.Column, evidence string) Candidate {
			return Candidate{Table: after.Name, From: was.Name, To: is.Name, Evidence: evidence}
		})
}

// lostColumns names every column one table holds that the other does not, in canonical order.
func lostColumns(held, other schema.Table) []schema.Column {
	var lost []schema.Column

	for _, column := range held.Columns {
		if _, kept := other.Column(column.Name); !kept {
			lost = append(lost, column)
		}
	}

	return lost
}

// A floor rather than a ranking alone, because a candidate below it is not a question worth asking:
// a person shown ten implausible pairs stops reading the plausible one. And pairing is greedy and
// one-to-one, so N losses against M gains ask at most min(N, M) questions rather than N*M.

// pairBest returns the best one-to-one pairing of what was lost against what was gained.
func pairBest[T any](lost, gained []T, likeness func(T, T) (int, string),
	candidate func(T, T, string) Candidate,
) []Candidate {
	scored := scorePairs(lost, gained, likeness)
	slices.SortStableFunc(scored, byScoreThenName(lost, gained, candidate))

	takenLost := make(map[int]bool, len(lost))
	takenGained := make(map[int]bool, len(gained))

	var found []Candidate

	for _, pair := range scored {
		if takenLost[pair.lost] || takenGained[pair.gained] {
			continue
		}

		takenLost[pair.lost], takenGained[pair.gained] = true, true
		found = append(found, candidate(lost[pair.lost], gained[pair.gained], pair.evidence))
	}

	return found
}

// pair is one possible rename and how likely this differ thinks it, by index into the two lists.
type pair struct {
	// lost indexes what the schema being changed holds and the target does not.
	lost int
	// gained indexes what the target holds and the schema being changed does not.
	gained int
	// score ranks the pair against every other, a higher one being more likely.
	score int
	// evidence is what a prompt says about this pair, which is prose rather than the score.
	evidence string
}

// scorePairs scores every possible pairing, dropping the ones no heuristic would propose at all.
func scorePairs[T any](lost, gained []T, likeness func(T, T) (int, string)) []pair {
	var scored []pair

	for i, was := range lost {
		for j, is := range gained {
			score, evidence := likeness(was, is)
			if score <= 0 {
				continue
			}

			scored = append(scored, pair{lost: i, gained: j, score: score, evidence: evidence})
		}
	}

	return scored
}

// byScoreThenName orders pairs by likelihood, breaking every tie by name so the list is one list.
func byScoreThenName[T any](lost, gained []T, candidate func(T, T, string) Candidate,
) func(pair, pair) int {
	named := func(p pair) string {
		one := candidate(lost[p.lost], gained[p.gained], "")

		return one.From + "\x00" + one.To
	}

	return func(a, b pair) int {
		if a.score != b.score {
			return b.score - a.score
		}

		return compareStrings(named(a), named(b))
	}
}

// compareStrings orders two names, which is every tiebreak in this package.
func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// With returns these renames plus one more, which is how a caller collects what it confirmed.
func (r Renames) With(one Candidate) Renames {
	added := one.Rename()

	return Renames{
		Tables:  append(slices.Clone(r.Tables), added.Tables...),
		Columns: append(slices.Clone(r.Columns), added.Columns...),
	}
}
