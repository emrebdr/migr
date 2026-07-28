package engine

import (
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

// statements builds a list from a spelling where an uppercase letter cannot run in a transaction.
func statements(spec string) []driver.Statement {
	var stmts []driver.Statement

	for i, c := range spec {
		stmts = append(stmts, driver.Statement{
			SQL:           string(c) + string(rune('0'+i)),
			NoTransaction: c >= 'A' && c <= 'Z',
		})
	}

	return stmts
}

// shape renders a grouping as one letter per group: t for transactional, n for not.
func shape(groups []Group) string {
	out := ""

	for _, group := range groups {
		letter := "n"
		if group.Transactional {
			letter = "t"
		}

		for range group.Statements {
			out += letter
		}

		out += "|"
	}

	return out
}

func TestStatementsAreGroupedByWhetherTheyCanShareATransaction(t *testing.T) {
	cases := []struct {
		spec string
		want string
	}{
		{spec: "", want: ""},
		{spec: "a", want: "t|"},
		{spec: "A", want: "n|"},
		{spec: "abc", want: "ttt|"},
		{spec: "ABC", want: "nnn|"},
		{spec: "Abc", want: "n|tt|"},
		{spec: "abC", want: "tt|n|"},
		{spec: "aBc", want: "t|n|t|"},
		{spec: "abCCde", want: "tt|nn|tt|"},
		{spec: "AbCdE", want: "n|t|n|t|n|"},
	}

	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			if got := shape(Groups(statements(c.spec))); got != c.want {
				t.Errorf("Groups(%q) grouped as %q, want %q", c.spec, got, c.want)
			}
		})
	}
}

func TestAnEmptyMigrationYieldsNoGroupRatherThanAnEmptyOne(t *testing.T) {
	if groups := Groups(nil); len(groups) != 0 {
		t.Errorf("Groups(nil) returned %d groups, want none", len(groups))
		t.Log("One empty group would read as a migration with a transaction and nothing in it, and")
		t.Log("Apply would open one on the server rather than refusing a migration that does nothing.")
	}
}

func TestGroupingKeepsEveryStatementInOrder(t *testing.T) {
	for _, spec := range []string{"a", "A", "abc", "AbCdE", "abCCde", "aaBBBcc"} {
		t.Run(spec, func(t *testing.T) {
			stmts := statements(spec)

			var regrouped []driver.Statement
			for _, group := range Groups(stmts) {
				regrouped = append(regrouped, group.Statements...)
			}

			if !slices.Equal(regrouped, stmts) {
				t.Errorf("concatenating the groups gave %v, want the input %v", regrouped, stmts)
				t.Log("Grouping decides where transactions begin and end. A statement it drops is a")
				t.Log("schema change that silently never runs; one it reorders is a different migration.")
			}
		})
	}
}

func TestNoTwoAdjacentGroupsAgreeOnWhetherTheyAreTransactional(t *testing.T) {
	for _, spec := range []string{"a", "A", "abc", "AbCdE", "abCCde", "aaBBBcc", "aBcDeFg"} {
		t.Run(spec, func(t *testing.T) {
			groups := Groups(statements(spec))

			for i := 1; i < len(groups); i++ {
				if groups[i].Transactional == groups[i-1].Transactional {
					t.Fatalf("groups %d and %d are both Transactional=%v, so they should have been one",
						i-1, i, groups[i].Transactional)
				}
			}

			if len(groups) == 0 {
				t.Fatal("a non-empty migration produced no groups")
			}
		})
	}
}

func TestEveryStatementIsInAGroupThatMatchesIt(t *testing.T) {
	groups := Groups(statements("aBcDD"))

	for i, group := range groups {
		for j, stmt := range group.Statements {
			if group.Transactional == stmt.NoTransaction {
				t.Errorf("group %d is Transactional=%v but holds statement %d (%q) with "+
					"NoTransaction=%v", i, group.Transactional, j, stmt.SQL, stmt.NoTransaction)
				t.Log("A statement in the wrong group runs on the wrong side of a transaction")
				t.Log("boundary, which is the one thing the flag exists to prevent.")
			}
		}
	}
}
