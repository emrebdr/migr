package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
)

// waitingProject stages a shadowed project whose hold was detached, so its migration waits for a cutover.
func waitingProject(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := shadowedProject(t)
	f.captures, f.shadows, f.killAt = true, true, 3

	if _, _, err := shadowed(t, dir, drivers, "up"); ExitCode(err) != ExitAwaiting {
		t.Fatalf("staging a waiting migration exited %d: %v", ExitCode(err), err)
	}

	f.killAt, f.polls = 0, 0

	return dir, f, drivers
}

// at runs one command over a shadowed project with the clock starting at a given instant.
func at(t *testing.T, dir string, drivers *driver.Registry, now time.Time, args ...string) (string, string, error) {
	t.Helper()

	return runOptions(t, dir, &options{clock: ticking(now), drivers: drivers,
		runtimes: declaringRegistry(t, actIndexes, declaresShadow)}, args...)
}

func TestCutoverListsAWaitingMigrationWithTheLagItsHolderLastMeasured(t *testing.T) {
	dir, _, drivers := waitingProject(t)

	stdout, _, err := at(t, dir, drivers, noon, "cutover")
	if err != nil {
		t.Fatalf("cutover: %v", err)
	}

	for _, want := range []string{versionAt(0) + "  ", "waiting", "held in sync through the shadow table " + usersShadow,
		"trailed the log by 0s when it last read it", "migr cutover --confirm " + versionAt(0),
		"1 waiting for a cutover confirmation, 0 still copying"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("cutover does not say %q:\n%s", want, stdout)
		}
	}
}

func TestCutoverSaysNoRunIsHoldingACopyWhoseLastReadingIsOld(t *testing.T) {
	dir, _, drivers := waitingProject(t)

	stdout, _, err := at(t, dir, drivers, noon.Add(3*time.Hour), "cutover")
	if err != nil {
		t.Fatalf("cutover: %v", err)
	}

	for _, want := range []string{"no run is holding the shadow table " + usersShadow, "2h59m",
		"changes since are not applied, and migr up resumes the hold"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("a stale reading is not reported as one: missing %q:\n%s", want, stdout)
			t.Log("A lag figure from a holder that died hours ago is the claim the operator acts on,")
			t.Log("so it is shown with its age and never as current (M13-T14).")
		}
	}
}

func TestCutoverWithNothingWaitingSaysSo(t *testing.T) {
	dir, _, drivers := shadowedProject(t)

	stdout, _, err := at(t, dir, drivers, noon, "cutover")
	if err != nil {
		t.Fatalf("cutover: %v", err)
	}

	if !strings.Contains(stdout, "nothing is waiting for a cutover") {
		t.Errorf("an empty cutover report says:\n%s", stdout)
	}
}

func TestConfirmingACutoverRecordsItAndTheNextUpSwapsTheCopyIn(t *testing.T) {
	dir, f, drivers := waitingProject(t)

	stdout, _, err := at(t, dir, drivers, noon.Add(time.Hour), "cutover", "--confirm", versionAt(0))
	if err != nil {
		t.Fatalf("cutover --confirm exited %d: %v", ExitCode(err), err)
	}

	if !f.states[versionAt(0)].Confirmed {
		t.Fatal("cutover --confirm recorded no confirmation")
	}

	for _, want := range []string{"confirmed the cutover of " + versionAt(0), "At its last read the copy trailed the log by 0s",
		"No run is holding it in sync, so nothing swaps it yet: migr up resumes the hold"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the confirmation does not say %q:\n%s", want, stdout)
		}
	}

	again, _, err := at(t, dir, drivers, noon.Add(time.Hour), "cutover", "--confirm", versionAt(0))
	if err != nil || !strings.Contains(again, "already confirmed the cutover") {
		t.Errorf("a second confirmation exited %d (%v):\n%s", ExitCode(err), err, again)
	}

	up, stderr, err := shadowed(t, dir, drivers, "up")
	if err != nil {
		t.Fatalf("the next up exited %d: %v\n%s", ExitCode(err), err, stderr)
	}

	if !strings.Contains(up, "cut over through a shadow table") || f.states[versionAt(0)].Stage != history.StageCutOver {
		t.Errorf("the next up did not cut the confirmed migration over:\n%s", up)
	}
}

func TestConfirmingAMigrationThatIsStillCopyingIsRefused(t *testing.T) {
	dir, f, drivers := waitingProject(t)

	state := f.states[versionAt(0)]
	state.Stage = history.StageCopying
	f.states[versionAt(0)] = state

	list, _, err := at(t, dir, drivers, noon, "cutover")
	if err != nil || !strings.Contains(list, "still copying into the shadow table "+usersShadow) {
		t.Errorf("a copying migration is listed as (%v):\n%s", err, list)
	}

	_, _, err = at(t, dir, drivers, noon, "cutover", "--confirm", versionAt(0))
	if ExitCode(err) != ExitRuntime || !errors.Is(err, errNotWaiting) {
		t.Errorf("confirming a copy still being filled exited %d: %v", ExitCode(err), err)
	}

	if f.states[versionAt(0)].Confirmed {
		t.Error("a refused confirmation was recorded anyway")
	}
}

func TestConfirmingAVersionNothingCarriesIsATypo(t *testing.T) {
	dir, _, drivers := waitingProject(t)

	_, _, err := at(t, dir, drivers, noon, "cutover", "--confirm", "20990101T000000")
	if ExitCode(err) != ExitValidation || !errors.Is(err, errUnknownCutover) {
		t.Errorf("confirming a version nothing carries exited %d: %v", ExitCode(err), err)
	}
}

func TestCutoverJSONCarriesTheLagTheReadingAndWhatWasConfirmed(t *testing.T) {
	dir, _, drivers := waitingProject(t)

	stdout, _, err := at(t, dir, drivers, noon, "--json", "cutover", "--confirm", versionAt(0))
	if err != nil {
		t.Fatalf("cutover --json: %v", err)
	}

	var doc struct {
		Shadows []struct {
			Version string        `json:"version"`
			State   history.State `json:"state"`
			Shadow  struct {
				Behind    *float64   `json:"behind_ms"`
				ReadAt    *time.Time `json:"read_at"`
				Held      *bool      `json:"held"`
				Confirmed bool       `json:"confirmed"`
			} `json:"shadow"`
		} `json:"shadows"`
		Confirmed *struct {
			Version string `json:"version"`
		} `json:"confirmed"`
	}

	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("cutover --json is not a document: %v\n%s", err, stdout)
	}

	if len(doc.Shadows) != 1 || doc.Shadows[0].State != history.StateWaiting {
		t.Fatalf("cutover --json lists %+v", doc.Shadows)
	}

	row := doc.Shadows[0].Shadow
	if row.Behind == nil || row.ReadAt == nil || row.Held == nil || !*row.Held || !row.Confirmed {
		t.Errorf("the waiting row carries %+v", row)
	}

	if doc.Confirmed == nil || doc.Confirmed.Version != versionAt(0) {
		t.Errorf("the document does not say what was confirmed: %+v", doc.Confirmed)
	}
}

func TestStatusJSONCarriesAWaitingRowsLagAndReading(t *testing.T) {
	dir, _, drivers := waitingProject(t)

	stdout, _, err := at(t, dir, drivers, noon, "--json", "status")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}

	var doc struct {
		Statuses []struct {
			State  history.State `json:"state"`
			Shadow struct {
				Behind *float64 `json:"behind_ms"`
				Held   *bool    `json:"held"`
			} `json:"shadow"`
		} `json:"statuses"`
		Counts map[string]int `json:"counts"`
	}

	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status --json: %v", err)
	}

	if len(doc.Statuses) != 1 || doc.Statuses[0].State != history.StateWaiting ||
		doc.Statuses[0].Shadow.Behind == nil || doc.Statuses[0].Shadow.Held == nil {
		t.Errorf("status --json reports %+v", doc.Statuses)
	}

	if doc.Counts["waiting"] != 1 || doc.Counts["dirty"] != 0 {
		t.Errorf("the counts are %v", doc.Counts)
	}
}

func TestADetachedUpExitsSixWithTheWaitingDocumentUnderJSON(t *testing.T) {
	dir, f, drivers := shadowedProject(t)
	f.captures, f.shadows, f.killAt = true, true, 3

	_, stderr, err := shadowed(t, dir, drivers, "--json", "up")
	if ExitCode(err) != ExitAwaiting {
		t.Fatalf("the detached up exited %d: %v", ExitCode(err), err)
	}

	if !strings.Contains(stderr, `"code":6`) || !strings.Contains(stderr, `"waiting":{`) ||
		!strings.Contains(stderr, engine.ErrAwaitingCutover.Error()) {
		t.Errorf("the --json failure is not the waiting document:\n%s", stderr)
	}
}

func TestUnattendedCutsOverWithoutAConfirmationByFlagOrByEnvironment(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		env  string
	}{
		{name: "flag", args: []string{"up", "--unattended"}},
		{name: "environment", args: []string{"up"}, env: "true"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(unattendedVariable, c.env)

			dir, f, drivers := shadowedProject(t)
			f.captures, f.shadows = true, true

			stdout, stderr, err := shadowed(t, dir, drivers, c.args...)
			if err != nil {
				t.Fatalf("an unattended up exited %d: %v\n%s", ExitCode(err), err, stderr)
			}

			if !strings.Contains(stdout, "cut over unattended through a shadow table") {
				t.Errorf("the run does not say it cut over unattended:\n%s", stdout)
			}

			if f.states[versionAt(0)].Confirmed || strings.Contains(stderr, "until its cutover is confirmed") {
				t.Errorf("an unattended run waited for or wrote a confirmation:\n%s", stderr)
			}
		})
	}
}

func TestAnUnreadableUnattendedVariableIsATypo(t *testing.T) {
	t.Setenv(unattendedVariable, "sometimes")

	dir, f, drivers := shadowedProject(t)
	f.captures, f.shadows = true, true

	_, _, err := shadowed(t, dir, drivers, "up")
	if ExitCode(err) != ExitValidation || !strings.Contains(err.Error(), unattendedVariable) {
		t.Errorf("MIGR_UNATTENDED=sometimes exited %d: %v", ExitCode(err), err)
	}

	if len(f.states) != 0 {
		t.Error("the run started a shadow table before refusing its own environment")
	}
}

func TestCommandsThatNeedASettledDatabaseNameTheCutoverOverAWaitingMigration(t *testing.T) {
	for _, command := range []string{"down", "diff"} {
		t.Run(command, func(t *testing.T) {
			dir, _, drivers := waitingProject(t)

			_, _, err := at(t, dir, drivers, noon.Add(time.Hour), command)
			if ExitCode(err) != ExitRuntime || !strings.Contains(err.Error(), "migr cutover --confirm") {
				t.Errorf("migr %s over a waiting migration exited %d: %v", command, ExitCode(err), err)
			}
		})
	}
}
