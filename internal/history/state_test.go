package history

import (
	"slices"
	"testing"
)

func TestEveryReconciledStateIsValid(t *testing.T) {
	for _, state := range States() {
		if !state.Valid() {
			t.Errorf("%s is one of the reconciled states and did not validate", state)
		}
	}
}

func TestTheZeroStateIsNotAState(t *testing.T) {
	var zero State

	if zero.Valid() {
		t.Error("the zero State validated, so a status nothing set would read as reconciled")
		t.Log("Every status this package emits is given a state. The zero value has to be")
		t.Log("invalid, or a Status a caller built by hand would claim to have been resolved.")
	}
}

func TestAValueNamingNoStateIsNotAState(t *testing.T) {
	if State("blocked").Valid() {
		t.Error(`State("blocked") validated, and the fifth state is spelled "dirty"`)
	}
}

func TestEveryStateRendersItsWireName(t *testing.T) {
	want := []string{"applied", "pending", "missing", "drifted", "dirty", "waiting"}

	rendered := make([]string, 0, len(States()))
	for _, state := range States() {
		rendered = append(rendered, state.String())
	}

	if !slices.Equal(rendered, want) {
		t.Errorf("the states render as %v, want %v", rendered, want)
		t.Log("These strings are what --json emits and what a status table prints, so they")
		t.Log("are a surface rather than an implementation detail (M5-T3).")
	}
}

func TestTheDirtyStateAndTheDirtyOutcomeAgreeOnTheirName(t *testing.T) {
	if StateDirty.String() != Dirty.String() {
		t.Errorf("the state renders as %q and the outcome as %q", StateDirty, Dirty)
		t.Log("They are two axes and they name the same condition, exactly as applied does")
		t.Log("on both. Two spellings would make an operator think they were different.")
	}
}

func TestStatesIsACopy(t *testing.T) {
	States()[0] = "tampered"

	if States()[0] != StateApplied {
		t.Error("States handed out the package's own slice, so a caller edited the state set")
	}
}
