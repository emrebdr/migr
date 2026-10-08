package driver

import (
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/diff"
	"github.com/emrebdr/migr/internal/ir"
)

// diffSuite returns a diff suite over a session that answers one staged snapshot.
func diffSuite(t *testing.T) Diff {
	t.Helper()

	suite, session := snapshotSuite(t, nil)
	expected := suite.expected(t)
	session.live = &expected

	return Diff(suite)
}

func TestTheMutationDiffersFromTheSchemaItWasGiven(t *testing.T) {
	suite := diffSuite(t)
	before := suite.base().expected(t)

	after := suite.mutate(t, before)

	if after.Equal(before) {
		t.Fatal("the mutation returned the schema it was given, so this suite asks for nothing")
	}

	ops, refused := diff.Diff(before, after, diff.Renames{})

	for _, one := range refused {
		t.Errorf("the mutation holds something the differ will not propose: %s", one.Describe())
	}

	wanted := []ir.Kind{
		ir.KindDropIndex, ir.KindDropColumn, ir.KindAlterColumn, ir.KindCreateTable,
		ir.KindAddColumn, ir.KindAddUnique, ir.KindAddIndex, ir.KindAddForeignKey,
	}

	proposed := make([]ir.Kind, 0, len(ops))
	for _, op := range ops {
		proposed = append(proposed, op.Kind())
	}

	for _, kind := range wanted {
		if !slices.Contains(proposed, kind) {
			t.Errorf("the mutation no longer reaches %s, so no engine is held to it", kind)
		}
	}

	t.Log("What this suite proves is only as wide as what the mutation asks for, and the engines")
	t.Log("cannot say so: a mutation that quietly stopped reaching add_foreign_key would still")
	t.Log("pass on all four servers. This is what holds it.")
}

func TestTheDiffSuiteRefusesToRunWithoutWhatItNeeds(t *testing.T) {
	suite := diffSuite(t)
	suite.Prefix = ""

	if problems := suite.base().checkSuite(); len(problems) == 0 {
		t.Error("a diff suite with no prefix reported nothing")
		t.Log("It scopes every comparison to the prefix, so without one it would diff whatever a")
		t.Log("previous run left in a database several of them share.")
	}
}

func TestTheDiffSuiteNoticesASchemaTheDiffDidNotReach(t *testing.T) {
	suite := diffSuite(t)
	reached := suite.base().expected(t)

	if problem := suite.difference(t, "the forward diff", reached, reached); problem != "" {
		t.Errorf("two equal schemas were reported as a difference: %s", problem)
	}

	short := reached.Canonical()
	short.Tables = short.Tables[:1]

	problem := suite.difference(t, "the forward diff", reached, short)

	if problem == "" {
		t.Error("an engine that reached a different schema was reported as having reached this one")
		t.Log("This comparison is the whole verdict, so a suite that could not report a mismatch")
		t.Log("would pass on every driver whatever the differ emitted.")

		return
	}

	if !strings.Contains(problem, "the forward diff") {
		t.Errorf("the problem %q does not say which diff reached the wrong schema", problem)
	}
}
