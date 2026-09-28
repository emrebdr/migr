package cli

import (
	"strings"
	"testing"
)

func TestARunCompilesAnEmulatedOperationAgainstTheSession(t *testing.T) {
	dir, f, drivers := project(t, twoTables()[:1]...)
	f.expands = true
	f.emulates = "table rebuild"

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if !f.asked("expand") {
		t.Fatal("the session was never asked to compile the operation")
	}

	if len(f.statements) == 0 || f.statements[0] != "-- read the schema" {
		t.Errorf("the run executed %v, not what the session produced", f.statements)
	}

	t.Log("internal/cli is what hands the session to the planner, and it is the only place that")
	t.Log("can: the planner holds a compiler, which needs no database, and the session is the")
	t.Log("thing that has one. A driver that emulates nothing is handed nothing (ADR 0053).")
}

func TestAPreviewShowsEveryStatementAnEmulationBecomes(t *testing.T) {
	dir, f, drivers := project(t, twoTables()[:1]...)
	f.expands = true
	f.emulates = "table rebuild"

	stdout, _, err := runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	for _, fragment := range []string{"emulated: table rebuild", "-- read the schema"} {
		if !strings.Contains(stdout, fragment) {
			t.Errorf("plan does not show %q:\n%s", fragment, stdout)
		}
	}

	t.Log("CLAUDE.md §4.2: a capability is never silently degraded. The label says a rebuild is")
	t.Log("about to happen and the statements say what it is -- an operator can read the whole of")
	t.Log("it before anything runs, which is what makes the emulation something to inspect rather")
	t.Log("than something to trust.")
}

func TestADriverThatEmulatesNothingIsHandedNoSession(t *testing.T) {
	dir, f, drivers := project(t, twoTables()[:1]...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if f.asked("expand") {
		t.Error("a driver that compiles everything was asked to expand something")
	}
}
