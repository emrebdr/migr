package ir

import (
	"slices"
	"testing"
)

func TestTheDestructiveKindsAreTheOnesThatReportThemselves(t *testing.T) {
	for _, f := range fixtures(t) {
		if got, want := f.kind.CanDestroy(), f.op.Destructive(); got != want {
			t.Errorf("%s: CanDestroy() = %v, but the operation reports Destructive() = %v",
				f.kind, got, want)
			t.Log("The list exists because an operator permits destruction by naming a kind, and a")
			t.Log("verdict needs a payload: alter_column destroys only when it retypes. Nothing")
			t.Log("else may disagree with the method, which is the thing that decides one.")
		}
	}

	if len(DestructiveKinds()) != 6 {
		t.Errorf("%d kinds can destroy, and docs/ir.md names 6", len(DestructiveKinds()))
	}
}

func TestEveryDestructiveKindIsAKind(t *testing.T) {
	for _, kind := range DestructiveKinds() {
		if !kind.Valid() {
			t.Errorf("%q can destroy and is not in the operation set", kind)
		}
	}
}

func TestDestructiveKindsCannotBeEditedThroughTheCopyItReturns(t *testing.T) {
	got := DestructiveKinds()
	got[0] = KindCreateTable

	if slices.Contains(DestructiveKinds(), KindCreateTable) {
		t.Error("editing the returned slice changed what the package reports")
	}
}
