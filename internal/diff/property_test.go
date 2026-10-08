package diff

import (
	"bytes"
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

func TestEveryProposedOperationIsWellFormed(t *testing.T) {
	for _, one := range comparisons(t) {
		t.Run(one.name, func(t *testing.T) {
			ops, _ := Diff(one.from, one.to, one.confirmed)

			for i, op := range ops {
				if err := op.Validate(); err != nil {
					t.Errorf("operation %d (%s) is not well formed: %v", i+1, op.Kind(), err)
				}
			}
		})
	}
}

func TestEveryProposedListIsReversible(t *testing.T) {
	for _, one := range comparisons(t) {
		t.Run(one.name, func(t *testing.T) {
			ops, _ := Diff(one.from, one.to, one.confirmed)

			if _, err := ir.ReverseAll(ops); err != nil {
				t.Errorf("nothing could undo what this diff proposes: %v", err)
			}
		})
	}

	t.Log("This is what refusing a drop it cannot record buys: every operation carries a complete")
	t.Log("from payload, so a migration migr diff writes is reversible by derivation and passes")
	t.Log("migr validate's own reverse check rather than failing the gate it was written for.")
}

func TestNoProposedListIsOneTheRunWouldRefuse(t *testing.T) {
	for _, one := range comparisons(t) {
		t.Run(one.name, func(t *testing.T) {
			ops, _ := Diff(one.from, one.to, one.confirmed)

			live := one.from

			for _, found := range engine.Unrestorable(ops, &live) {
				t.Errorf("migr up would refuse what this diff proposes: %s", found.Describe())
			}

			reversed, err := ir.ReverseAll(ops)
			if err != nil {
				t.Fatalf("nothing could undo what this diff proposes: %v", err)
			}

			target := one.to

			for _, found := range engine.Stated(engine.Unrestorable(reversed, &target)) {
				t.Errorf("migr down would refuse the inverse of what this diff proposes: %s",
					found.Describe())
			}
		})
	}

	t.Log("The gate internal/engine reads before anything compiles is the one a generated migration")
	t.Log("has to get past, in both directions. A diff that emitted a bare drop_table would be")
	t.Log("refused here, which is what makes refusing it in the differ the cheaper answer.")
	t.Log("The rollback half reads Stated alone because that is how the engine reads it: an add is")
	t.Log("inverted by name, so the inverse of a create_table is a drop that records nothing -- and")
	t.Log("ADR 0069 permits the implied half of a rollback for the reason §7 judges only an up's")
	t.Log("destruction. Both directions are still judged in full, as two cases rather than one.")
}

func TestItProposesTheSameThingTwice(t *testing.T) {
	for _, one := range comparisons(t) {
		t.Run(one.name, func(t *testing.T) {
			first, firstRefused := Diff(one.from, one.to, one.confirmed)
			second, secondRefused := Diff(one.from, one.to, one.confirmed)

			left, err := ir.Marshal(first)
			if err != nil {
				t.Fatalf("encoding what the first diff proposed: %v", err)
			}

			right, err := ir.Marshal(second)
			if err != nil {
				t.Fatalf("encoding what the second diff proposed: %v", err)
			}

			if !bytes.Equal(left, right) {
				t.Errorf("two diffs of one pair proposed different operations\n%s\n%s", left, right)
			}

			if describeAll(firstRefused) != describeAll(secondRefused) {
				t.Errorf("two diffs of one pair refused different things\n%s\n%s",
					describeAll(firstRefused), describeAll(secondRefused))
			}

			shuffled, _ := Diff(reordered(one.from), reordered(one.to), one.confirmed)

			third, err := ir.Marshal(shuffled)
			if err != nil {
				t.Fatalf("encoding what the reordered diff proposed: %v", err)
			}

			if !bytes.Equal(left, third) {
				t.Errorf("the same pair in a different order proposed different operations"+
					"\n%s\n%s", left, third)
				t.Log("A driver reports a schema in whatever order its catalog answers, so what the")
				t.Log("differ emits must not depend on that -- which is what canonicalizing both")
				t.Log("sides first buys, and all invariant #8 asks for here.")
			}
		})
	}

	t.Log("Invariant #8: nothing here reads a clock or iterates a map, and both schemas are")
	t.Log("canonicalized before anything is compared.")
}

// reordered returns a schema holding exactly what it was given, in the opposite order throughout.
func reordered(snapshot schema.Snapshot) schema.Snapshot {
	flipped := schema.Snapshot{
		Tables: slices.Clone(snapshot.Tables),
		Views:  slices.Clone(snapshot.Views),
	}

	slices.Reverse(flipped.Tables)
	slices.Reverse(flipped.Views)

	for i := range flipped.Tables {
		flipped.Tables[i] = reorderedTable(flipped.Tables[i])
	}

	return flipped
}

// reorderedTable returns a table holding what it was given, with every set reversed.
func reorderedTable(table schema.Table) schema.Table {
	table.Columns = reversed(table.Columns)
	table.Indexes = reversed(table.Indexes)
	table.Uniques = reversed(table.Uniques)
	table.Checks = reversed(table.Checks)
	table.ForeignKeys = reversed(table.ForeignKeys)

	return table
}

// reversed returns a copy of a set in the opposite order, which no driver promises anything about.
func reversed[T any](items []T) []T {
	flipped := slices.Clone(items)
	slices.Reverse(flipped)

	return flipped
}
