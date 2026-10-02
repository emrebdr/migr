package history

import (
	"slices"
	"testing"
)

func TestTheZeroOutcomeIsNotAnOutcome(t *testing.T) {
	var zero Outcome

	if zero.Valid() {
		t.Error("the zero Outcome names a run outcome")
		t.Log("A record whose outcome was never set must not pass validation. If the zero value")
		t.Log("were valid it would read as whichever outcome it happens to equal, and a driver")
		t.Log("that forgot the field would record a run as though it had ended that way.")
	}

	if zero.Blocking() {
		t.Error("the zero Outcome blocks the next migration")
	}
}

func TestOnlyADirtyRunBlocksTheNext(t *testing.T) {
	blocking := map[Outcome]bool{Applied: false, Failed: false, Dirty: true, Resolved: false}

	for _, outcome := range Outcomes() {
		if outcome.Blocking() != blocking[outcome] {
			t.Errorf("%s.Blocking() = %v, want %v", outcome, outcome.Blocking(), blocking[outcome])
			t.Log("This is the whole reason the outcome is several values rather than a bool.")
			t.Log("A failed run left the schema unchanged and the next up may proceed; a dirty")
			t.Log("one did not, and M4-T9 requires the next up to refuse until it is resolved.")
			t.Log("Resolved is what stops blocking: it is the run that settles a dirty one (M9-T6).")
		}
	}
}

func TestOutcomesIsACopy(t *testing.T) {
	first := Outcomes()
	first[0] = "tampered"

	if slices.Contains(Outcomes(), "tampered") {
		t.Error("a caller mutated the package's own outcome list")
	}

	if len(Outcomes()) != 4 {
		t.Errorf("Outcomes() returned %d outcomes, want 4", len(Outcomes()))
	}
}
