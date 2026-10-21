package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

// leftInvalid is the INVALID index the tests stage, as PostgreSQL would report one.
var leftInvalid = driver.InvalidIndex{Table: "users", Index: "users_email",
	Definition: "CREATE INDEX users_email ON public.users USING btree (email)"}

func TestStatusListsEveryInvalidIndexWithBothRemedies(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.invalid = []driver.InvalidIndex{leftInvalid}

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	for _, want := range []string{"INVALID indexes", "users_email on users",
		`REINDEX INDEX CONCURRENTLY "users_email"`, `DROP INDEX CONCURRENTLY "users_email"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status does not say %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = runWith(t, dir, noon, drivers, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}

	var doc struct {
		Invalid []invalidIndex `json:"invalid_indexes"`
	}

	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || len(doc.Invalid) != 1 ||
		doc.Invalid[0].Index != "users_email" || doc.Invalid[0].Definition == "" {
		t.Errorf("status --json carried %+v (%v):\n%s", doc.Invalid, err, stdout)
	}
}

func TestStatusSaysNothingOfIndexesWhereNoneIsInvalid(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if strings.Contains(stdout, "INVALID") {
		t.Errorf("status reported INVALID indexes where none is:\n%s", stdout)
	}
}

func TestPlanListsEveryInvalidIndex(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.invalid = []driver.InvalidIndex{leftInvalid}

	stdout, _, err := runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if !strings.Contains(stdout, "!  users_email on users") {
		t.Errorf("plan does not list the INVALID index:\n%s", stdout)
	}
}

func TestUpWarnsOfAnInvalidIndexItLeavesBehind(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.invalid = []driver.InvalidIndex{leftInvalid}

	_, stderr, err := runWith(t, dir, noon, drivers, "up")
	if err != nil {
		t.Fatalf("up: %v", err)
	}

	if !strings.Contains(stderr, "an INVALID index is maintained on every write") ||
		!strings.Contains(stderr, "users_email") {
		t.Errorf("up did not warn of the INVALID index:\n%s", stderr)
	}
}

func TestUpTriesARepairedStandaloneStatementAgainAndSaysWhatItRepaired(t *testing.T) {
	dir, f, drivers := contending(t, 1)
	f.outsideTx = contendedTable
	f.repaired = &driver.Repaired{Action: "dropped the INVALID index users_email it left behind", Rerunnable: true}

	stdout, stderr, err := runWith(t, dir, noon, drivers, "up")
	if err != nil {
		t.Fatalf("up: %v\n%s", err, stderr)
	}

	if !strings.Contains(stdout, "repaired: statement 1: dropped the INVALID index users_email") {
		t.Errorf("up does not say what it repaired:\n%s", stdout)
	}

	if !strings.Contains(stderr, "cleaned up what a statement outside any transaction left behind") {
		t.Errorf("up did not warn of the repair:\n%s", stderr)
	}

	if !f.asked("repair after_failure " + contendedTable) {
		t.Errorf("the session was never asked to repair: %v", f.calls)
	}
}

func TestUpDoesNotTryAStandaloneStatementAgainWithoutARepair(t *testing.T) {
	dir, f, drivers := contending(t, 1)
	f.outsideTx = contendedTable

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err == nil ||
		!strings.Contains(err.Error(), "not tried again") {
		t.Errorf("up returned %v, want the standalone statement left dirty", err)
	}
}
