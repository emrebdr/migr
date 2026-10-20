package migration

import (
	"errors"
	"strings"
	"testing"
)

// fourSteps are one change's four migrations, a minute apart, each declaring the step before it.
func fourSteps() ([]Migration, Phases) {
	found := []Migration{at(0, "expand"), at(1, "backfill"), at(2, "swap"), at(3, "contract")}
	phases := Phases{
		found[0].Version: {Change: "users.email", Name: "expand"},
		found[1].Version: {Change: "users.email", Name: "backfill", After: "expand"},
		found[2].Version: {Change: "users.email", Name: "swap", After: "backfill"},
		found[3].Version: {Change: "users.email", Name: "contract", After: "swap"},
	}

	return found, phases
}

func TestEveryWaitingStepResolvesToTheMigrationItWaitsOn(t *testing.T) {
	found, phases := fourSteps()

	gates, err := Gates(found, phases)
	if err != nil {
		t.Fatalf("Gates: %v", err)
	}

	if len(gates) != 3 {
		t.Fatalf("%d steps wait, want the three after expand: %+v", len(gates), gates)
	}

	for i, step := range found[1:] {
		if got := gates[step.Version].Prior; got.Version != found[i].Version {
			t.Errorf("%s waits on %s, want %s", step.Filename(), got.Filename(), found[i].Filename())
		}
	}

	if _, waits := gates[found[0].Version]; waits {
		t.Error("expand waits on something, and it is the first step")
	}
}

func TestADeclarationNoDeployCouldCarryOutIsRefusedNamingEveryFile(t *testing.T) {
	found, phases := fourSteps()
	phases[found[1].Version] = Phase{Change: "users.email", Name: "backfill", After: "expnad"}
	phases[found[2].Version] = Phase{Change: "users.email", Name: "contract", After: "swap"}
	phases[found[3].Version] = Phase{Change: "", Name: "contract"}

	_, err := Gates(found, phases)
	if !errors.Is(err, ErrPhase) {
		t.Fatalf("Gates returned %v, want ErrPhase", err)
	}

	for _, want := range []string{found[1].Filename() + `: its phase "backfill" of "users.email" waits on "expnad"`,
		found[3].Filename() + ": its phase names no change",
		`waits on "swap", and no migration here declares that step`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

func TestTwoMigrationsDeclaringOneStepAreRefused(t *testing.T) {
	found, phases := fourSteps()
	phases[found[3].Version] = Phase{Change: "users.email", Name: "swap", After: "backfill"}

	if _, err := Gates(found, phases); err == nil || !strings.Contains(err.Error(), "is also declared by") {
		t.Errorf("Gates returned %v, want the repeated step refused", err)
	}
}

func TestAStepWaitingOnOneThatRunsAfterItIsRefused(t *testing.T) {
	found, phases := fourSteps()
	phases[found[0].Version], phases[found[3].Version] = Phase{Change: "users.email", Name: "contract",
		After: "swap"}, Phase{Change: "users.email", Name: "expand"}

	if _, err := Gates(found, phases); err == nil || !strings.Contains(err.Error(), "which runs after it") {
		t.Errorf("Gates returned %v, want a step waiting on a later one refused", err)
	}
}

func TestAStepWaitingOnItselfIsRefused(t *testing.T) {
	if err := (Phase{Change: "users.email", Name: "swap", After: "swap"}).Validate(); err == nil {
		t.Error("a step waiting on itself was accepted, and no deploy could ever run it")
	}
}
