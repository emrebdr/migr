package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
)

// shadowedProject stages one Go migration indexing users, declared to run through a shadow table.
func shadowedProject(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := goProject(t, "index_users_plan")
	f.live = usersSnapshot()

	return dir, f, drivers
}

// shadowed runs one command over a shadowed project.
func shadowed(t *testing.T, dir string, drivers *driver.Registry, args ...string) (string, string, error) {
	t.Helper()

	return runDeclaring(t, dir, actIndexes, declaresShadow, drivers, args...)
}

// The copy, retired original and capture the staged migration derives.
var (
	usersShadow  = engine.ShadowName("users", versionAt(0))
	usersRetired = engine.RetiredName("users", versionAt(0))
	usersCapture = engine.CaptureName("users", versionAt(0))
)

func TestUpHoldsAShadowTableInSyncSaysSoAndCutsOverOnceConfirmed(t *testing.T) {
	dir, f, drivers := shadowedProject(t)
	f.captures, f.shadows, f.confirmAfter = true, true, 4

	stdout, stderr, err := shadowed(t, dir, drivers, "up")
	if err != nil {
		t.Fatalf("up exited %d: %v\n%s", ExitCode(err), err, stderr)
	}

	if !strings.Contains(stderr, "holding the shadow table in sync until its cutover is confirmed") {
		t.Errorf("a hold waiting on a confirmation was silent:\n%s", stderr)
	}

	for _, want := range []string{"cut over through a shadow table", "the original kept as " + usersRetired} {
		if !strings.Contains(stdout, want) {
			t.Errorf("up does not say %q:\n%s", want, stdout)
		}
	}

	if f.polls < 4 || !f.asked("cutover") || !f.asked("release "+usersCapture) {
		t.Errorf("the run held for %d reads and asked %v", f.polls, f.calls)
	}

	if state := f.states[versionAt(0)]; state.Stage != history.StageCutOver {
		t.Errorf("the state reads %q after the cutover", state.Stage)
	}
}

func TestAStoppedHoldIsReportedByStatusAndCarriedOnByTheNextUp(t *testing.T) {
	dir, f, drivers := shadowedProject(t)
	f.captures, f.shadows, f.killAt = true, true, 3

	if _, _, err := shadowed(t, dir, drivers, "up"); !errors.Is(err, errStopped) {
		t.Fatalf("the stopped run returned %v", err)
	}

	if !f.slots[usersCapture] || !f.asked("detach "+usersCapture) {
		t.Fatalf("stopping the hold released the capture: slots %v, calls %v", f.slots, f.calls)
	}

	status, _, err := shadowed(t, dir, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	for _, want := range []string{"stopped syncing through the shadow table " + usersShadow,
		"migr up carries it on", "migr resolve --rolled-back abandons it"} {
		if !strings.Contains(status, want) {
			t.Errorf("status does not say %q:\n%s", want, status)
		}
	}

	f.killAt, f.confirmAfter, f.polls = 0, 2, 0

	stdout, stderr, err := shadowed(t, dir, drivers, "up")
	if err != nil {
		t.Fatalf("the next up exited %d: %v\n%s", ExitCode(err), err, stderr)
	}

	if !strings.Contains(stdout, "cut over through a shadow table") || !strings.Contains(stdout, "resumed") {
		t.Errorf("the next up does not report a resumed cutover:\n%s", stdout)
	}

	if f.count("copy-chunk") != 1 {
		t.Errorf("the next up copied again, though the stopped run had finished the copy: %v", f.calls)
	}
}

func TestResolveRolledBackAbandonsAStoppedShadowTableMigrationAndSaysWhatItDrops(t *testing.T) {
	dir, f, drivers := shadowedProject(t)
	f.captures, f.shadows, f.killAt = true, true, 2

	if _, _, err := shadowed(t, dir, drivers, "up"); !errors.Is(err, errStopped) {
		t.Fatalf("the stopped run returned %v", err)
	}

	planned, _, err := shadowed(t, dir, drivers, "resolve", "--rolled-back", "--reason", "too slow", "--dry-run")
	if err != nil {
		t.Fatalf("resolve --dry-run: %v", err)
	}

	if !strings.Contains(planned, "will release the change stream "+usersCapture+" and drop the shadow "+
		"table "+usersShadow) {
		t.Errorf("the dry run does not say what it abandons:\n%s", planned)
	}

	if _, _, err := shadowed(t, dir, drivers, "resolve", "--rolled-back", "--reason", "too slow"); err != nil {
		t.Fatalf("resolve --rolled-back: %v", err)
	}

	if len(f.slots) != 0 || len(f.states) != 0 || !f.asked("release "+usersCapture) {
		t.Errorf("abandoning left the slots %v and the states %v", f.slots, f.states)
	}

	if !slicesContain(f.statements, "-- drop "+usersShadow) {
		t.Errorf("abandoning did not drop the copy: %v", f.statements)
	}
}

// slicesContain reports whether a list of strings holds one.
func slicesContain(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}

	return false
}

func TestAServerThatCannotCaptureIsRefusedNamingWhatItLacksBeforeAnythingIsCreated(t *testing.T) {
	dir, f, drivers := shadowedProject(t)

	_, _, err := shadowed(t, dir, drivers, "up")
	if code := ExitCode(err); code != ExitRuntime || !errors.Is(err, engine.ErrCannotShadow) ||
		!strings.Contains(err.Error(), lackedCapture) {
		t.Fatalf("up exited %d: %v", code, err)
	}

	if f.asked("capture "+usersCapture) || len(f.statements) != 0 || len(f.runs) != 0 {
		t.Errorf("the refusal came after something was created: %v", f.calls)
	}
}

func TestPlanSaysHowAShadowTableMigrationRunsAndWhatItCostsALiveTable(t *testing.T) {
	dir, f, drivers := shadowedProject(t)
	f.captures, f.shadows = true, true

	stdout, _, err := shadowed(t, dir, drivers, "plan", "--safety")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	for _, want := range []string{"through a shadow table: copies users into " + usersShadow,
		"keeps the original as " + usersRetired, "(through a shadow table: the rename at cutover)",
		"a stopped run carries on only while the server keeps its log: " + fakeRetention} {
		if !strings.Contains(stdout, want) {
			t.Errorf("plan does not say %q:\n%s", want, stdout)
		}
	}

	if strings.Contains(stdout, "-- add_index") {
		t.Errorf("plan printed the plain statement, which is not what a shadow-table migration runs:\n%s", stdout)
	}

	if f.asked("capture "+usersCapture) || len(f.states) != 0 {
		t.Errorf("plan created something: %v", f.calls)
	}
}

func TestPlanMarksAShadowTableMigrationThisServerRefuses(t *testing.T) {
	dir, _, drivers := shadowedProject(t)

	stdout, _, err := shadowed(t, dir, drivers, "plan", "--json")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	var doc struct {
		Migrations []struct {
			Shadow *shadowDoc `json:"shadow"`
		} `json:"migrations"`
	}

	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decoding the plan: %v\n%s", err, stdout)
	}

	if len(doc.Migrations) != 1 || doc.Migrations[0].Shadow == nil ||
		!strings.Contains(doc.Migrations[0].Shadow.Refusal, lackedCapture) {
		t.Errorf("the plan reported %s", stdout)
	}
}

func TestValidateConnectReportsAShadowTableMigrationTheServerCannotRun(t *testing.T) {
	for _, tc := range []struct {
		name     string
		captures bool
		says     string
	}{
		{name: "no capture", says: lackedCapture},
		{name: "no strategy", captures: true, says: "driver does not perform shadow-table migration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, f, drivers := shadowedProject(t)
			f.captures = tc.captures

			stdout, _, err := shadowed(t, dir, drivers, "validate", "--connect")
			if ExitCode(err) != ExitValidation || !strings.Contains(stdout, tc.says) {
				t.Errorf("validate --connect exited %d:\n%s", ExitCode(err), stdout)
			}
		})
	}
}

func TestAnUnknownOnlineStrategyIsRefusedAtDiscovery(t *testing.T) {
	dir, _, drivers := shadowedProject(t)

	_, _, err := runDeclaring(t, dir, actIndexes, declaresStrayOnline, drivers, "up")
	if ExitCode(err) != ExitValidation || !strings.Contains(err.Error(), `"triggers"`) {
		t.Errorf("up exited %d: %v", ExitCode(err), err)
	}
}
