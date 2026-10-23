package migr

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/sdk/go/internal/wire"
)

// stepped is a hand-written migration declaring itself one step of a change across deploys.
type stepped struct {
	plain
	// step is what it declares.
	step Phase
}

// Phase returns the declared step.
func (m stepped) Phase() Phase { return m.step }

// discovered asks a session what it loaded, and returns the one migration it reported.
func discovered(t *testing.T, m Migration) (wire.Discovered, string) {
	t.Helper()

	staged(t, map[string]Migration{file: m})

	e := dial(t)
	e.greet()

	answer := e.answer(e.send(wire.MigrationDiscover, struct{}{}), nil)
	if answer.Error != nil {
		t.Fatalf("discover failed: %v", answer.Error)
	}

	var found wire.DiscoverResult

	decode(t, answer, &found)

	return found.Migrations[0], string(answer.Result)
}

func TestDiscoverReportsTheStepAMigrationDeclaresItself(t *testing.T) {
	got, _ := discovered(t, stepped{step: Phase{Change: "users.email", Name: "contract", After: "swap"}})

	if got.Phase == nil || *got.Phase != (wire.Phase{Change: "users.email", Name: "contract", After: "swap"}) {
		t.Errorf("discover reported the phase %+v", got.Phase)
	}
}

func TestAMigrationDeclaringNoStepSendsNoPhase(t *testing.T) {
	if _, raw := discovered(t, plain{}); strings.Contains(raw, `"phase"`) {
		t.Errorf("discover rendered %s for a migration declaring no step", raw)
		t.Log("An optional member an engine reading an older protocol ignores, and one an SDK that")
		t.Log("declares nothing omits -- so a project using no phase sends the bytes it always sent.")
	}
}

func TestAStepWaitingOnNothingSendsNoAfter(t *testing.T) {
	if _, raw := discovered(t, stepped{step: Phase{Change: "users.email", Name: "expand"}}); strings.Contains(raw,
		`"after"`) {
		t.Errorf("discover rendered %s, want the after member omitted", raw)
	}
}

func TestAPhaseNoDeployCouldCarryOutIsARegistrationError(t *testing.T) {
	for name, p := range map[string]Phase{
		"no change":     {Name: "expand"},
		"no step":       {Change: "users.email"},
		"waits on self": {Change: "users.email", Name: "swap", After: "swap"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := accept(stepped{step: p}, file)
			if !errors.Is(err, ErrInvalidPhase) || !strings.Contains(err.Error(), file) {
				t.Errorf("registration returned %v, want ErrInvalidPhase naming the file", err)
			}
		})
	}
}

func TestEachPhaseOfAReplacementWaitsOnTheOneBeforeIt(t *testing.T) {
	r := ReplaceColumn("users", "email", String(255)).With(String(320))

	for _, c := range []struct {
		m    Migration
		want Phase
	}{
		{r.Expand(), Phase{Change: "users.email", Name: "expand"}},
		{r.Backfill(), Phase{Change: "users.email", Name: "backfill", After: "expand"}},
		{r.Swap(), Phase{Change: "users.email", Name: "swap", After: "backfill"}},
		{r.Contract(), Phase{Change: "users.email", Name: "contract", After: "swap"}},
		{r.Through(TransformLower).Contract(), Phase{Change: "users.email", Name: "contract", After: "swap"}},
	} {
		got, declares := phaseOf(c.m)
		if !declares || got != c.want {
			t.Errorf("%T declares %+v (%v), want %+v", c.m, got, declares, c.want)
		}
	}

	t.Log("The four phases are a release apart each: a backfill in the deploy that expanded misses")
	t.Log("the rows the old app writes, and a contract in the deploy that swapped drops a column the")
	t.Log("old app still reads.")
}
