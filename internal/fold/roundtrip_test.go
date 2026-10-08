package fold_test

import (
	"testing"

	"github.com/emrebdr/migr/internal/diff"
	"github.com/emrebdr/migr/internal/fold"
	"github.com/emrebdr/migr/internal/schema"
)

// Nothing can judge a projection correct on its own, so the thing that judges it is the differ: the
// two are each other's inverse over a list the differ produced. Forward(a, Diff(a, b)) is b, for
// every pair internal/diff holds itself to -- which is the pure half of M11-T6's done-when, reached
// without a database. It is an external test package because it imports internal/diff, and that
// package imports nothing from here.

func TestFoldingADiffOntoItsSourceReachesItsTarget(t *testing.T) {
	for _, one := range comparisons(t) {
		t.Run(one.name, func(t *testing.T) {
			ops, refused := diff.Diff(one.from, one.to, one.confirmed)

			for _, problem := range refused {
				t.Fatalf("the differ would not propose part of this: %s", problem.Describe())
			}

			reached, unfoldable := fold.Forward(one.from, ops)

			for _, problem := range unfoldable {
				t.Errorf("the projection could not fold what the differ emitted: %s",
					problem.Describe())
			}

			want := one.to.Canonical()

			if reduced := fold.Comparable(want, reached); !reduced.Equal(want) {
				t.Errorf("folding the diff did not reach the target\n\nwant:\n%s\n\ngot:\n%s",
					render(t, want), render(t, reduced))
			}
		})
	}

	t.Log("internal/diff is snapshot -> IR and this is IR -> snapshot, so over a list the differ")
	t.Log("produced they compose to the identity. Nothing else can say the projection is right:")
	t.Log("a hand-written expectation per operation says only that it matches itself.")
	t.Log("The comparison goes through Comparable for the reason every caller's does -- a view's")
	t.Log("body and a check's expression are engine text, and a projection has never seen an engine.")
}

// render prints a snapshot for a failure, which is the whole of what one of these says.
func render(t *testing.T, held schema.Snapshot) string {
	t.Helper()

	data, err := schema.Marshal(held)
	if err != nil {
		t.Fatalf("rendering a snapshot: %v", err)
	}

	return string(data)
}
