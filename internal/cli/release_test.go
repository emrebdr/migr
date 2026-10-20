package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
)

// stepping stages two Go migrations, the second a step of one change waiting on the first.
func stepping(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	return goProject(t, "swap_email", "contract_email")
}

// deploy runs one command over the stepping project, as a deploy of the given release would.
func deploy(t *testing.T, dir string, drivers *driver.Registry, args ...string) (string, string, error) {
	t.Helper()

	return runDeclaring(t, dir, actEmits, declaresSteps, drivers, args...)
}

func TestARunStopsAtAStepWaitingForALaterReleaseAndSucceeds(t *testing.T) {
	dir, f, drivers := stepping(t)

	stdout, _, err := deploy(t, dir, drivers, "up", "--release", "r1")
	if err != nil {
		t.Fatalf("up --release r1 exited %d: %v", ExitCode(err), err)
	}

	for _, want := range []string{"applied 1 migration", "\u23f8 held: " + goFileAt(1, "contract_email") +
		" waits for a later release", `belongs to release "r1", which is this one`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("up does not say %q:\n%s", want, stdout)
		}
	}

	if len(f.releases) != 1 || f.releases[0].Name != "r1" {
		t.Errorf("the run recorded the releases %+v, want r1 once", f.releases)
	}

	if f.count("record-release") != 1 {
		t.Errorf("the release was not written inside the record's transaction: %v", f.calls)
	}

	t.Log("A change partway through its deploys is the normal state of one, so a hold is reported")
	t.Log("and the run succeeds -- failing would fail every deploy until the change finished.")
}

func TestTheSameReleaseHoldsAgainAndALaterOneRunsTheStep(t *testing.T) {
	dir, _, drivers := stepping(t)

	if _, _, err := deploy(t, dir, drivers, "up", "--release", "r1"); err != nil {
		t.Fatalf("up --release r1: %v", err)
	}

	stdout, _, err := deploy(t, dir, drivers, "--json", "up", "--release", "r1")
	if err != nil {
		t.Fatalf("up --release r1 again: %v", err)
	}

	var again runSummary
	if err := json.Unmarshal([]byte(stdout), &again); err != nil {
		t.Fatalf("decoding the summary: %v\n%s", err, stdout)
	}

	if len(again.Runs) != 0 || again.Held == nil || again.Held.Phase != "step1" ||
		again.Held.After != "step0" || again.Held.PriorRelease != "r1" {
		t.Errorf("a second run under r1 reported %+v (held %+v)", again.Runs, again.Held)
	}

	stdout, _, err = deploy(t, dir, drivers, "up", "--release", "r2")
	if err != nil || !strings.Contains(stdout, "applied 1 migration") || strings.Contains(stdout, "held") {
		t.Errorf("up --release r2 did not run the step (%v):\n%s", err, stdout)
	}
}

func TestReachingAStepWithNoReleaseIsAValidationFailureBeforeAnythingRuns(t *testing.T) {
	dir, f, drivers := stepping(t)

	_, _, err := deploy(t, dir, drivers, "up")
	if !errors.Is(err, engine.ErrNoRelease) || ExitCode(err) != ExitValidation {
		t.Fatalf("up with no release returned %v (exit %d), want ErrNoRelease, exit 2", err, ExitCode(err))
	}

	if len(f.runs) != 0 {
		t.Errorf("a run naming no release applied %d migrations before refusing", len(f.runs))
	}

	if !strings.Contains(err.Error(), "--release") || !strings.Contains(err.Error(), releaseVariable) {
		t.Errorf("the refusal does not name the remedy: %v", err)
	}
}

func TestTheReleaseMayComeFromTheEnvironment(t *testing.T) {
	dir, f, drivers := stepping(t)
	t.Setenv(releaseVariable, "from-ci")

	if _, _, err := deploy(t, dir, drivers, "up"); err != nil {
		t.Fatalf("up with %s set: %v", releaseVariable, err)
	}

	if len(f.releases) != 1 || f.releases[0].Name != "from-ci" {
		t.Errorf("the run recorded %+v, want the release the environment named", f.releases)
	}
}

func TestAPreviewStopsWhereTheRunWouldAndSaysWhy(t *testing.T) {
	dir, _, drivers := stepping(t)

	if _, _, err := deploy(t, dir, drivers, "up", "--release", "r1"); err != nil {
		t.Fatalf("up --release r1: %v", err)
	}

	for _, args := range [][]string{{"plan", "--release", "r1"}, {"up", "--dry-run", "--release", "r1"}} {
		stdout, _, err := deploy(t, dir, drivers, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}

		if !strings.Contains(stdout, "held: "+goFileAt(1, "contract_email")) {
			t.Errorf("%v does not show the hold:\n%s", args, stdout)
		}
	}

	stdout, _, err := deploy(t, dir, drivers, "--json", "up", "--dry-run", "--release", "r1")
	if err != nil {
		t.Fatalf("up --dry-run --json: %v", err)
	}

	var dry planSummary
	if err := json.Unmarshal([]byte(stdout), &dry); err != nil {
		t.Fatalf("decoding the dry run: %v\n%s", err, stdout)
	}

	if len(dry.Steps) != 0 || dry.Held == nil {
		t.Errorf("the dry run would run %d steps past a hold at %+v", len(dry.Steps), dry.Held)
	}

	stdout, _, err = deploy(t, dir, drivers, "plan", "--release", "r2")
	if err != nil || strings.Contains(stdout, "held") {
		t.Errorf("a plan under a later release still holds (%v):\n%s", err, stdout)
	}
}

func TestAPreviewNamingNoReleaseSaysUpWouldRefuse(t *testing.T) {
	dir, _, drivers := stepping(t)

	stdout, _, err := deploy(t, dir, drivers, "plan")
	if err != nil {
		t.Fatalf("plan exited %d, and a preview refuses nothing: %v", ExitCode(err), err)
	}

	if !strings.Contains(stdout, "migr up would refuse this run") {
		t.Errorf("the plan does not say migr up needs a release:\n%s", stdout)
	}
}

func TestAStepWaitingOnNothingAnyMigrationDeclaresIsRefused(t *testing.T) {
	dir, _, drivers := stepping(t)

	for _, command := range []string{"up", "plan", "validate"} {
		_, _, err := runDeclaring(t, dir, actEmits, declaresStrayStep, drivers, command, "--release", "r1")
		if command == "validate" {
			_, _, err = runDeclaring(t, dir, actEmits, declaresStrayStep, drivers, command)
		}

		if ExitCode(err) != ExitValidation || !strings.Contains(fmtErr(err), `waits on "nothing"`) {
			t.Errorf("migr %s exited %d: %v", command, ExitCode(err), err)
		}
	}
}

// fmtErr renders an error, and nothing for none.
func fmtErr(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

func TestARollbackIsNeverHeldByAStep(t *testing.T) {
	dir, f, drivers := stepping(t)

	for _, release := range []string{"r1", "r2"} {
		if _, _, err := deploy(t, dir, drivers, "up", "--release", release); err != nil {
			t.Fatalf("up --release %s: %v", release, err)
		}
	}

	if _, _, err := deploy(t, dir, drivers, "down", "--step", "2"); err != nil {
		t.Fatalf("down --step 2 exited %d: %v", ExitCode(err), err)
	}

	if last := f.runs[len(f.runs)-1]; last.Version != versionAt(0) || last.Direction != "down" {
		t.Errorf("the rollback ended at %s going %s, want the first step undone", last.Version, last.Direction)
	}
}

// goFileAt returns the filename the nth staged Go migration carries.
func goFileAt(n int, name string) string {
	return versionAt(n) + "_" + name + ".go"
}
